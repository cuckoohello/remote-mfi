package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cuckoohello/remote-mfi/internal/biz"
	"github.com/cuckoohello/remote-mfi/internal/transport"
)

func TestRemoteMFIFlowAndIdempotency(t *testing.T) {
	driver := &apiFakeDriver{certificate: []byte{1, 2, 3, 4}}
	handler := newTestHandler(t, driver, "secret")
	server := httptest.NewServer(handler)
	defer server.Close()

	reset := doRequest(t, http.MethodPost, server.URL+"/mfi/reset", "{}", "secret", "application/json")
	assertStatusAndBody(t, reset, http.StatusOK, `{"detail":""}`)

	certificateHTTPResponse := doRequest(t, http.MethodGet, server.URL+"/mfi/certificate", "", "secret", "application/json")
	if certificateHTTPResponse.StatusCode != http.StatusOK {
		t.Fatalf("certificate status = %d", certificateHTTPResponse.StatusCode)
	}
	var certificate certificateResponse
	decodeResponse(t, certificateHTTPResponse, &certificate)
	digest := sha256.Sum256(driver.certificate)
	if certificate.ProtocolMajor != 3 ||
		certificate.Certificate != base64.StdEncoding.EncodeToString(driver.certificate) ||
		certificate.CertificateSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected certificate response: %+v", certificate)
	}

	requestID := uuid.NewString()
	body := `{"challenge":"AQIDBA==","requestId":"` + requestID + `"}`
	first := doRequest(t, http.MethodPost, server.URL+"/mfi/sign", body, "secret", "application/json")
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first sign status = %d body=%s", first.StatusCode, readBody(t, first))
	}
	var firstResult signResponse
	decodeResponse(t, first, &firstResult)

	second := doRequest(t, http.MethodPost, server.URL+"/mfi/sign", body, "secret", "application/json")
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second sign status = %d body=%s", second.StatusCode, readBody(t, second))
	}
	var secondResult signResponse
	decodeResponse(t, second, &secondResult)
	if firstResult.Signature != secondResult.Signature {
		t.Fatal("idempotent retry returned a different signature")
	}
	if got := driver.signCalls.Load(); got != 1 {
		t.Fatalf("chip sign calls = %d, want 1", got)
	}
}

func TestBearerAuthenticationAndDebugUnauthorizedPage(t *testing.T) {
	handler := newTestHandler(t, &apiFakeDriver{}, "secret")
	server := httptest.NewServer(handler)
	defer server.Close()

	response := doRequest(t, http.MethodGet, server.URL+"/mfi/certificate", "", "", "application/json")
	assertStatusAndBody(t, response, http.StatusUnauthorized, `{"detail":"unauthorized"}`)

	response = doRequest(t, http.MethodGet, server.URL+"/debug/usb", "", "", "text/html")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("debug unauthorized status = %d", response.StatusCode)
	}
	body := readBody(t, response)
	if !strings.Contains(body, "Authorization required") || !strings.Contains(body, "Bearer") {
		t.Fatalf("unauthorized HTML does not contain guidance: %s", body)
	}

	response = doRequest(t, http.MethodGet, server.URL+"/debug/usb?token=secret", "", "", "text/html")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("debug query token status = %d", response.StatusCode)
	}
	if body := readBody(t, response); !strings.Contains(body, "CANDIDATE CH341") {
		t.Fatalf("debug HTML missing candidate marker")
	}
}

func TestRecentRequestsExcludeDebugAndHealth(t *testing.T) {
	handler := newTestHandler(t, &apiFakeDriver{}, "")
	server := httptest.NewServer(handler)
	defer server.Close()

	for range 3 {
		response := doRequest(t, http.MethodPost, server.URL+"/mfi/reset", "{}", "", "application/json")
		_ = readBody(t, response)
	}
	for range 5 {
		response := doRequest(t, http.MethodGet, server.URL+"/healthz", "", "", "application/json")
		_ = readBody(t, response)
		response = doRequest(t, http.MethodGet, server.URL+"/debug/usb", "", "", "application/json")
		_ = readBody(t, response)
	}

	response := doRequest(t, http.MethodGet, server.URL+"/debug/usb", "", "", "application/json")
	var data debugData
	decodeResponse(t, response, &data)
	if len(data.RecentRequests) != 3 {
		t.Fatalf("recent request count = %d, want 3", len(data.RecentRequests))
	}
	for _, recent := range data.RecentRequests {
		if recent.Path != "/mfi/reset" || recent.Note != "noop" {
			t.Fatalf("unexpected recent request: %+v", recent)
		}
	}
}

func TestSignValidation(t *testing.T) {
	handler := newTestHandler(t, &apiFakeDriver{}, "")
	server := httptest.NewServer(handler)
	defer server.Close()

	tests := []struct {
		name   string
		body   string
		detail string
	}{
		{name: "missing requestId", body: `{"challenge":"AQ=="}`, detail: "requestId is required"},
		{name: "missing challenge", body: `{"requestId":"` + uuid.NewString() + `"}`, detail: "challenge is required"},
		{name: "bad base64", body: `{"challenge":"!","requestId":"` + uuid.NewString() + `"}`, detail: "challenge is not valid base64"},
		{name: "unknown field", body: `{"challenge":"AQ==","requestId":"` + uuid.NewString() + `","extra":1}`, detail: "invalid request body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := doRequest(t, http.MethodPost, server.URL+"/mfi/sign", test.body, "", "application/json")
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d", response.StatusCode)
			}
			var failure map[string]string
			decodeResponse(t, response, &failure)
			if failure["detail"] != test.detail {
				t.Fatalf("detail = %q, want %q", failure["detail"], test.detail)
			}
		})
	}
}

func TestResetRequiresEmptyObject(t *testing.T) {
	handler := newTestHandler(t, &apiFakeDriver{}, "")
	server := httptest.NewServer(handler)
	defer server.Close()

	for _, body := range []string{"null", "[]", `{"x":1}`} {
		response := doRequest(t, http.MethodPost, server.URL+"/mfi/reset", body, "", "application/json")
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s: status = %d, want 400", body, response.StatusCode)
		}
	}
}

func TestHealthIsUnauthenticated(t *testing.T) {
	handler := newTestHandler(t, &apiFakeDriver{}, "secret")
	server := httptest.NewServer(handler)
	defer server.Close()

	response := doRequest(t, http.MethodGet, server.URL+"/healthz", "", "", "application/json")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["ok"] != true || body["chip"] != "ready" {
		t.Fatalf("unexpected health response: %#v", body)
	}
}

func newTestHandler(t *testing.T, driver *apiFakeDriver, token string) http.Handler {
	t.Helper()
	service, err := biz.NewService(driver, apiFakeInspector{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	handler, err := NewHandler(
		service,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		token,
		time.FixedZone("UTC+8", 8*60*60),
		20,
	)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

type apiFakeDriver struct {
	certificate []byte
	signCalls   atomic.Int32
}

func (d *apiFakeDriver) ProtocolMajor(context.Context) (uint8, error) {
	return 3, nil
}

func (d *apiFakeDriver) ReadCertificate(context.Context) ([]byte, error) {
	if d.certificate == nil {
		return []byte{1, 2, 3}, nil
	}
	return append([]byte(nil), d.certificate...), nil
}

func (d *apiFakeDriver) SignChallenge(_ context.Context, challenge []byte) ([]byte, error) {
	d.signCalls.Add(1)
	digest := sha256.Sum256(challenge)
	return digest[:], nil
}

type apiFakeInspector struct{}

func (apiFakeInspector) InspectUSB(bool) transport.USBStatus {
	return transport.USBStatus{
		State:  "ready",
		VIDPID: "1a86:5512",
		Devices: []transport.USBDeviceInfo{{
			Bus:          1,
			Device:       7,
			Vendor:       "1a86",
			ProductID:    "5512",
			Manufacturer: "wch.cn",
			Product:      "USB-I2C",
			Speed:        "full",
			Class:        255,
			Candidate:    true,
		}},
	}
}

func doRequest(t *testing.T, method, url, body, token, accept string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	return response
}

func assertStatusAndBody(t *testing.T, response *http.Response, status int, want string) {
	t.Helper()
	if response.StatusCode != status {
		t.Fatalf("status = %d, want %d", response.StatusCode, status)
	}
	var got map[string]string
	decodeResponse(t, response, &got)
	var expected map[string]string
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatalf("decode expected body: %v", err)
	}
	if fmtMap(got) != fmtMap(expected) {
		t.Fatalf("body = %#v, want %#v", got, expected)
	}
}

func decodeResponse(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func readBody(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	return string(body)
}

func fmtMap(value map[string]string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
