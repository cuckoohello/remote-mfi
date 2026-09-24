package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/biz"
	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/chip"
	"github.com/cuckoohello/remote-mfi-for-xcertplay/internal/transport"
)

type certificateResponse struct {
	Type              string `json:"type"`
	ProtocolMajor     int    `json:"protocolMajor"`
	Certificate       string `json:"certificate"`
	CertificateSHA256 string `json:"certificateSha256"`
}

type signRequest struct {
	Challenge string `json:"challenge"`
	RequestID string `json:"requestId"`
}

type signResponse struct {
	Signature string `json:"signature"`
}

func (s *Server) handleCertificate(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	certificate, err := s.service.Certificate(r.Context())
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	state := stateFrom(r)
	state.note = "chip"
	state.chipWait = certificate.WaitDuration
	state.chipTime = certificate.ChipDuration
	digest := sha256.Sum256(certificate.Data)
	writeJSON(w, http.StatusOK, certificateResponse{
		Type:              "mfi",
		ProtocolMajor:     int(certificate.ProtocolMajor),
		Certificate:       base64.StdEncoding.EncodeToString(certificate.Data),
		CertificateSHA256: hex.EncodeToString(digest[:]),
	})
}

func (s *Server) handleSign(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var request signRequest
	if err := decodeJSON(w, r, &request); err != nil {
		stateFrom(r).note = "bad request"
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "invalid request body"})
		return
	}
	state := stateFrom(r)
	state.requestID = request.RequestID
	if request.RequestID == "" {
		state.note = "bad request"
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "requestId is required"})
		return
	}
	if request.Challenge == "" {
		state.note = "bad request"
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "challenge is required"})
		return
	}
	challenge, err := base64.StdEncoding.DecodeString(request.Challenge)
	if err != nil {
		state.note = "bad request"
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "challenge is not valid base64"})
		return
	}
	result, err := s.service.Sign(r.Context(), request.RequestID, challenge)
	if err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	state.chipWait = result.WaitDuration
	state.chipTime = result.ChipDuration
	if result.Cached {
		state.note = "idempotent-hit"
	} else {
		state.note = "chip"
	}
	writeJSON(w, http.StatusOK, signResponse{
		Signature: base64.StdEncoding.EncodeToString(result.Signature),
	})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	var body map[string]json.RawMessage
	if err := decodeJSON(w, r, &body); err != nil {
		stateFrom(r).note = "bad request"
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "invalid request body"})
		return
	}
	if body == nil || len(body) != 0 {
		stateFrom(r).note = "bad request"
		writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "body must be {}"})
		return
	}
	if err := s.service.Reset(r.Context()); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	stateFrom(r).note = "noop"
	writeJSON(w, http.StatusOK, map[string]string{"detail": ""})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	health := s.service.Health()
	response := map[string]any{
		"ok":   health.Chip == "ready",
		"chip": health.Chip,
	}
	if health.Reason != "" {
		response["reason"] = health.Reason
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleDebug(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	health := s.service.Diagnostics()
	runtime := s.service.Runtime()
	now := time.Now()
	data := debugData{
		Chip: debugChip{
			Status: health.Chip,
			Reason: health.Reason,
			VIDPID: health.VIDPID,
		},
		Uptime: debugUptime{
			StartedAt: s.startedAt.In(s.location).Format(time.RFC3339),
			Seconds:   int64(now.Sub(s.startedAt).Seconds()),
		},
		USBDevices: health.Devices,
		Runtime: debugRuntime{
			CacheEntries: runtime.CacheEntries,
			LockHolder:   optionalString(runtime.LockHolder),
			LockHeldMS:   runtime.LockHeld.Milliseconds(),
		},
		RecentRequests: s.recent.listNewestFirst(),
		CurrentTime:    now.In(s.location).Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if acceptsJSON(r) {
		writeJSON(w, http.StatusOK, data)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if err := s.debugPage.Execute(w, data); err != nil {
		s.logger.Error("render debug page", "error", err)
	}
}

func (s *Server) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	status, detail, note := serviceErrorResponse(err)
	stateFrom(r).note = note
	writeJSON(w, status, map[string]string{"detail": detail})
}

func serviceErrorResponse(err error) (status int, detail string, note string) {
	switch {
	case errors.Is(err, biz.ErrChallengeSize),
		errors.Is(err, biz.ErrRequestIDFormat),
		errors.Is(err, biz.ErrRequestIDReuse):
		return http.StatusBadRequest, err.Error(), "bad request"
	case errors.Is(err, biz.ErrChipBusy):
		return http.StatusServiceUnavailable, biz.ErrChipBusy.Error(), "chip busy"
	case errors.Is(err, biz.ErrChipMissing):
		return http.StatusServiceUnavailable, biz.ErrChipMissing.Error(), "error"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return http.StatusServiceUnavailable, "request cancelled", "error"
	case transport.IsKind(err, transport.ErrorPermission):
		return http.StatusServiceUnavailable, "chip unavailable: permission denied", "error"
	case transport.IsKind(err, transport.ErrorBusy):
		return http.StatusServiceUnavailable, "chip unavailable: USB device busy", "error"
	case transport.IsKind(err, transport.ErrorTimeout):
		return http.StatusInternalServerError, "chip operation timed out", "error"
	case transport.IsKind(err, transport.ErrorNACK):
		return http.StatusInternalServerError, "I2C NACK", "error"
	case chip.IsKind(err, chip.ErrorAuthFailed):
		var chipError *chip.Error
		if errors.As(err, &chipError) && chipError.Code != nil {
			return http.StatusInternalServerError, fmt.Sprintf("chip auth failed: 0x%02x", *chipError.Code), "error"
		}
		return http.StatusInternalServerError, "chip auth timeout", "error"
	case chip.IsKind(err, chip.ErrorInvalidData):
		return http.StatusInternalServerError, "invalid data returned by MFi chip", "error"
	default:
		return http.StatusInternalServerError, "MFi hardware operation failed", "error"
	}
}

type debugData struct {
	Chip           debugChip                 `json:"chip"`
	Uptime         debugUptime               `json:"uptime"`
	USBDevices     []transport.USBDeviceInfo `json:"usbDevices"`
	Runtime        debugRuntime              `json:"runtime"`
	RecentRequests []RecentRequest           `json:"recentRequests"`
	CurrentTime    string                    `json:"-"`
}

type debugChip struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	VIDPID string `json:"vidPid,omitempty"`
}

type debugUptime struct {
	StartedAt string `json:"startedAt"`
	Seconds   int64  `json:"seconds"`
}

type debugRuntime struct {
	CacheEntries int     `json:"cacheEntries"`
	LockHolder   *string `json:"lockHolder"`
	LockHeldMS   int64   `json:"lockHeldMs"`
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
