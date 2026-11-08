package httpapi

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBroadcastStreamKeyRoutesPermissions(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).Routes()
	base := "/api/admin/broadcasts/0199a000-0000-4000-8000-000000000003/"
	for _, tc := range []struct {
		method, path, scope string
		status              int
	}{
		{"GET", "stream-key", "cms:recordings:read", 503},
		{"GET", "stream-key", "cms:recordings:write", 403},
		{"PUT", "stream-key", "cms:recordings:write", 403},
		{"PUT", "stream-key", "cms:recordings:publish", 403},
		{"PUT", "stream-key", "cms:recordings:write cms:recordings:publish", 503},
		{"POST", "stream-key-bindings", "cms:recordings:write", 503},
		{"POST", "stream-key-bindings", "cms:recordings:read", 403},
	} {
		r := httptest.NewRequest(tc.method, base+tc.path, nil)
		trustedBulletinAudit(r, tc.scope)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v got %d %s", tc, w.Code, w.Body)
		}
		r = serviceRequest(tc.method, base+tc.path)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("service accepted %d", w.Code)
		}
	}
	w := httptest.NewRecorder()
	broadcastError(w, recordings.ErrBroadcastStreamKey)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "broadcast_stream_key_invalid") {
		t.Fatal(w.Body.String())
	}
}

func TestBroadcastStreamKeyStrictInputAndPrivacy(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now).WithBroadcastWriters(true)).Routes()
	base := "/api/admin/broadcasts/0199a000-0000-4000-8000-000000000003/"
	for _, tc := range []struct {
		method, path, body, code string
		status                   int
	}{
		{"POST", "stream-key-bindings", `{"operationKey":"bind","expectedRevision":1,"protocolVersion":"b1-2026-10-10.rc1"}`, "broadcast_stream_key_invalid", 403},
		{"POST", "stream-key-bindings", `{"operationKey":"bind","expectedRevision":1,"protocolVersion":"b1-2026-10-10.rc1","streamKey":"wrong"}`, "broadcast_stream_key_invalid", 403},
		{"PUT", "stream-key", `{"operationKey":"set","expectedRevision":1,"streamKey":"wrong"}`, "broadcast_invalid", 400},
		{"PUT", "stream-key", `{"operationKey":"set","expectedRevision":1}`, "broadcast_invalid", 400},
		{"PUT", "stream-key", `{"operationKey":"set","expectedRevision":1,"streamKey":null}`, "broadcast_invalid", 400},
		{"PUT", "stream-key", `{"operationKey":"set","expectedRevision":1,"streamKey":"wrong","extra":true}`, "broadcast_invalid", 400},
		{"GET", "stream-key?streamKey=never-in-query", ``, "broadcast_invalid", 400},
	} {
		r := httptest.NewRequest(tc.method, base+tc.path, strings.NewReader(tc.body))
		trustedBulletinAudit(r, "cms:recordings:read cms:recordings:write cms:recordings:publish")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("%+v got %d %s", tc, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "wrong") || strings.Contains(w.Body.String(), "never-in-query") {
			t.Fatal("input credential echoed")
		}
		if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("credential response cache boundary")
		}
	}
}
