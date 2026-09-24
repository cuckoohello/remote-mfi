package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/biz"
)

const maximumRequestBodyBytes = 64 * 1024

//go:embed templates/*.tmpl
var templateFiles embed.FS

type Service interface {
	Certificate(ctx context.Context) (biz.Certificate, error)
	Sign(ctx context.Context, requestID string, challenge []byte) (biz.SignResult, error)
	Reset(ctx context.Context) error
	Health() biz.HealthStatus
	Diagnostics() biz.HealthStatus
	Runtime() biz.RuntimeStatus
}

type Server struct {
	service     Service
	logger      *slog.Logger
	bearerToken string
	location    *time.Location
	startedAt   time.Time
	recent      *recentStore
	debugPage   *template.Template
	authPage    *template.Template
}

type requestState struct {
	note      string
	requestID string
	chipWait  time.Duration
	chipTime  time.Duration
}

type requestStateKey struct{}

func NewHandler(
	service Service,
	logger *slog.Logger,
	bearerToken string,
	location *time.Location,
	recentCapacity int,
) (http.Handler, error) {
	if service == nil {
		return nil, fmt.Errorf("service is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger is required")
	}
	if location == nil {
		return nil, fmt.Errorf("location is required")
	}
	debugPage, err := template.ParseFS(templateFiles, "templates/debug.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse debug template: %w", err)
	}
	authPage, err := template.ParseFS(templateFiles, "templates/unauthorized.html.tmpl")
	if err != nil {
		return nil, fmt.Errorf("parse unauthorized template: %w", err)
	}

	server := &Server{
		service:     service,
		logger:      logger,
		bearerToken: bearerToken,
		location:    location,
		startedAt:   time.Now(),
		recent:      newRecentStore(recentCapacity, location),
		debugPage:   debugPage,
		authPage:    authPage,
	}
	mux := http.NewServeMux()
	mux.Handle("/mfi/certificate", server.withAuth(http.HandlerFunc(server.handleCertificate), false))
	mux.Handle("/mfi/sign", server.withAuth(http.HandlerFunc(server.handleSign), false))
	mux.Handle("/mfi/reset", server.withAuth(http.HandlerFunc(server.handleReset), false))
	mux.Handle("/debug/usb", server.withAuth(http.HandlerFunc(server.handleDebug), true))
	mux.HandleFunc("/healthz", server.handleHealth)
	return server.observe(mux), nil
}

func (s *Server) withAuth(next http.Handler, allowQueryToken bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.bearerToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		provided := ""
		if authorization := r.Header.Get("Authorization"); strings.HasPrefix(authorization, "Bearer ") {
			provided = strings.TrimPrefix(authorization, "Bearer ")
		}
		if provided == "" && allowQueryToken {
			provided = r.URL.Query().Get("token")
		}
		providedDigest := sha256.Sum256([]byte(provided))
		expectedDigest := sha256.Sum256([]byte(s.bearerToken))
		if subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		stateFrom(r).note = "unauthorized"
		if allowQueryToken && !acceptsJSON(r) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.WriteHeader(http.StatusUnauthorized)
			if err := s.authPage.Execute(w, nil); err != nil {
				s.logger.Error("render unauthorized page", "error", err)
			}
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"detail": "unauthorized"})
	})
}

func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		state := &requestState{}
		r = r.WithContext(context.WithValue(r.Context(), requestStateKey{}, state))
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		duration := time.Since(started)
		note := state.note
		if note == "" {
			note = defaultNote(recorder.status)
		}

		s.logger.Info(
			"http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.statusOrOK(),
			"duration_ms", duration.Milliseconds(),
			"note", note,
			"request_id", state.requestID,
			"chip_wait_ms", state.chipWait.Milliseconds(),
			"chip_duration_ms", state.chipTime.Milliseconds(),
		)
		if isBusinessPath(r.URL.Path) {
			s.recent.add(r.Method, r.URL.Path, recorder.statusOrOK(), duration, note)
		}
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maximumRequestBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("request body must contain exactly one JSON value")
	}
	return nil
}

func requireMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	stateFrom(r).note = "bad request"
	writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"detail": "method not allowed"})
	return false
}

func acceptsJSON(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json")
}

func stateFrom(r *http.Request) *requestState {
	state, _ := r.Context().Value(requestStateKey{}).(*requestState)
	if state == nil {
		return &requestState{}
	}
	return state
}

func isBusinessPath(path string) bool {
	return path == "/mfi/certificate" || path == "/mfi/sign" || path == "/mfi/reset"
}

func defaultNote(status int) string {
	switch {
	case status == http.StatusUnauthorized:
		return "unauthorized"
	case status >= 400 && status < 500:
		return "bad request"
	case status >= 500:
		return "error"
	default:
		return "ok"
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status != 0 {
		return
	}
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(body)
}

func (r *statusRecorder) statusOrOK() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}
