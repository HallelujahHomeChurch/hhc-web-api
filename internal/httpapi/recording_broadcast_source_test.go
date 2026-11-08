package httpapi

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBroadcastSourceRoutesPermissions(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).Routes()
	for _, tc := range []struct {
		method, path, scope string
		status              int
	}{{"GET", "", "read", 503}, {"GET", "", "write", 403}, {"PUT", "/stream-key", "write", 403}, {"PUT", "/target", "publish", 403}, {"POST", "/resolve", "read", 503}, {"POST", "/resolve", "write", 403}, {"POST", "/bindings", "write", 503}, {"POST", "/bindings", "read", 403}} {
		r := httptest.NewRequest(tc.method, "/api/admin/broadcast-source"+tc.path, nil)
		trustedBulletinAudit(r, "cms:recordings:"+tc.scope)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%+v: %d %s", tc, w.Code, w.Body)
		}
		r = serviceRequest(tc.method, "/api/admin/broadcast-source"+tc.path)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("service accepted %d", w.Code)
		}
	}
}
func TestBroadcastSourceInputPrivacy(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now).WithBroadcastWriters(true)).Routes()
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{{"POST", "/resolve", `{"streamKey":"wrong"}`, 403}, {"POST", "/bindings", `{"streamKey":"wrong"}`, 403}, {"PUT", "/stream-key", `{"streamKey":"wrong"}`, 400}, {"POST", "/resolve", `{"streamKey":"wrong","extra":true}`, 400}, {"GET", "?streamKey=wrong", "", 400}, {"POST", "/resolve", `{"streamKey":"` + strings.Repeat("A", 5000) + `"}`, 400}} {
		r := httptest.NewRequest(tc.method, "/api/admin/broadcast-source"+tc.path, strings.NewReader(tc.body))
		trustedBulletinAudit(r, "cms:recordings:read cms:recordings:write cms:recordings:publish")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "wrong") || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("privacy boundary")
		}
	}
}

type sourceHTTPRepository struct {
	recordings.Repository
	recordings.BroadcastRepository
	recordings.BroadcastSourceRepository
}

func (sourceHTTPRepository) GetBroadcastSource(context.Context) (recordings.BroadcastSource, error) {
	return recordings.BroadcastSource{Revision: 1, CanRotate: true}, nil
}
func TestBroadcastSourceReadUsesValidatedSingletonAudit(t *testing.T) {
	outbox := &bulletinAuditOutbox{}
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(sourceHTTPRepository{}, "", nil, time.Now)).WithAuditOutbox(outbox, time.Now).Routes()
	request := httptest.NewRequest("GET", "/api/admin/broadcast-source", nil)
	trustedBulletinAudit(request, "cms:recordings:read")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != 200 || len(outbox.events) != 1 {
		t.Fatalf("status %d audit %d body %s", response.Code, len(outbox.events), response.Body)
	}
	if outbox.events[0].Action != "cms.site_settings.read" || outbox.events[0].ResourceID != "singleton" {
		t.Fatalf("wrong singleton audit %+v", outbox.events[0])
	}
}
