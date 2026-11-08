package httpapi

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBroadcastIngestTrustedCallerAndNoOAuthFallback(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "dapr-test", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now)).Routes()
	for _, tc := range []struct {
		path, method, caller, token, body string
		want                              int
	}{
		{"/api/ingest/streams/open", "POST", "", "", `{}`, 401},
		{"/api/ingest/streams/open", "POST", "api-gateway", "dapr-test", `{}`, 422},
		{"/api/ingest/streams/open?key=x", "POST", "api-gateway", "dapr-test", `{}`, 400},
		{"/api/ingest/streams/open", "POST", "api-gateway", "dapr-test", `{"protocolVersion":"k4-2026-10-15.rc1","outputSessionId":"bad","streamKey":"bad"}`, 400},
		{"/api/ingest/broadcast-source/resolve", "POST", "", "", `{"streamKey":"wrong"}`, 401},
		{"/api/ingest/broadcast-source/resolve", "POST", "other", "dapr-test", `{"streamKey":"wrong"}`, 401},
		{"/api/ingest/broadcast-source/resolve", "POST", "api-gateway", "dapr-test", `{"streamKey":"wrong"}`, 403},
		{"/api/ingest/broadcast-source/resolve?streamKey=wrong", "POST", "api-gateway", "dapr-test", `{}`, 400},
		{"/api/ingest/broadcast-source/resolve", "POST", "api-gateway", "dapr-test", `{"streamKey":"wrong","title":"test"}`, 400},
		{"/api/ingest/broadcasts/018f0000-0000-7000-8000-000000000002/captures/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "GET", "api-gateway", "dapr-test", "", 401},
		{"/api/ingest/broadcasts/018f0000-0000-7000-8000-000000000002/start", "POST", "api-gateway", "dapr-test", `{}`, 404},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("X-Internal-Caller-App-Id", tc.caller)
		req.Header.Set("dapr-api-token", tc.token)
		req.Header.Set("X-HHC-User-ID", "018f0000-0000-7000-8000-000000000001")
		req.Header.Set("X-HHC-Scopes", "*")
		req.Header.Set("Authorization", "Bearer account-jwt")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(w.Body.String(), "wrong") {
			t.Fatal("credential privacy")
		}
	}
}
