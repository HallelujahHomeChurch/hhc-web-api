package httpapi

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCaptureRoutesRequireSeparatePermissions(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).Routes()
	for _, tc := range []struct{ method, path, scope string }{
		{"POST", "", "cms:recordings:read"},
		{"GET", "/" + strings.Repeat("a", 32), "cms:recordings:write"},
		{"POST", "/" + strings.Repeat("a", 32) + "/stop", "cms:recordings:read"},
		{"DELETE", "/" + strings.Repeat("a", 32) + "/live", "cms:recordings:write"},
	} {
		r := httptest.NewRequest(tc.method, "/api/admin/recordings/11111111-1111-4111-8111-111111111111/captures"+tc.path, strings.NewReader(`{}`))
		trusted(r, tc.scope)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s %s got %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

func TestCaptureQueryRejectsUnknownDuplicateAndMalformedKeys(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now)).Routes()
	for _, query := range []string{"limit=1&limit=2", "unknown=1", "cursor=a&cursor=b", "limit=%xx", "limit=abc"} {
		r := httptest.NewRequest("GET", "/api/admin/recordings/11111111-1111-4111-8111-111111111111/captures/"+strings.Repeat("a", 32)+"?"+query, nil)
		trusted(r, "cms:recordings:read")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "capture_invalid") {
			t.Fatalf("%s: %d %s", query, w.Code, w.Body.String())
		}
	}
}
func TestCaptureRequiredBooleansAndBodyLimit(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now)).Routes()
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"operationKey":"create","expectedVersion":1,"liveEnabled":false}`, 400},
		{`{"operationKey":"create","expectedVersion":1,"autoPublish":false}`, 400},
		{`{"operationKey":"` + strings.Repeat("a", 256<<10) + `","expectedVersion":1,"autoPublish":false,"liveEnabled":false}`, 413},
	} {
		r := httptest.NewRequest("POST", "/api/admin/recordings/11111111-1111-4111-8111-111111111111/captures", strings.NewReader(tc.body))
		trusted(r, "cms:recordings:write")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), "capture_") {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
}
