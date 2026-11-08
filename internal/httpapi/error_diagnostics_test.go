package httpapi

import (
	"bytes"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/logging"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestErrorClassificationLoggedWithoutBody(t *testing.T) {
	var output bytes.Buffer
	previous := logging.Access
	logging.Access = slog.New(slog.NewJSONHandler(&output, nil))
	defer func() { logging.Access = previous }()
	h := requestID(accessLog(captureErrors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, 503, "authorization_unavailable", "private-body-must-not-be-logged")
	}))))
	r := httptest.NewRequest("GET", "/api/admin/recordings/fixture/captures", nil)
	r.Header.Set("X-HHC-Request-ID", "traceable-request")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	for _, want := range []string{`"application_error_code":"authorization_unavailable"`, `"request_id":"traceable-request"`, `"status":503`} {
		if !strings.Contains(output.String(), want) {
			t.Fatal(output.String())
		}
	}
	if strings.Contains(output.String(), "private-body") || strings.Contains(output.String(), "fixture") {
		t.Fatal("private content logged")
	}
	if !strings.Contains(w.Body.String(), "capture_unavailable") {
		t.Fatal("wire adapter changed")
	}
}

func TestErrorsModeRetainsDeniedAndSlowSuccess(t *testing.T) {
	t.Setenv("ACCESS_LOG_MODE", "errors")
	var logs bytes.Buffer
	previous := logging.Access
	logging.Access = slog.New(slog.NewJSONHandler(&logs, nil))
	defer func() { logging.Access = previous }()
	for _, status := range []int{200, 403, 503} {
		accessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if status == 200 {
				time.Sleep(time.Second)
			}
			w.WriteHeader(status)
		})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/fixture", nil))
	}
	if strings.Count(logs.String(), "http request") != 3 {
		t.Fatal(logs.String())
	}
}
