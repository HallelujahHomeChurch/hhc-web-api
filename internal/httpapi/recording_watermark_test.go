package httpapi

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type recordingLookupStub struct {
	recordings.Repository
	calls int
}

func (s *recordingLookupStub) LookupRecordingWatermark(_ context.Context, actor, id, code string, now time.Time) (recordings.WatermarkReceipt, error) {
	s.calls++
	return recordings.WatermarkReceipt{ID: "00000000-0000-4000-8000-000000000001", UserID: actor, RecordingID: id, Code: code, IssuedAt: now, ExpiresAt: now.Add(recordings.WatermarkRetention)}, nil
}
func TestRecordingWatermarkLookupHumanPermissionBoundary(t *testing.T) {
	repo := &recordingLookupStub{}
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(repo, "", nil, time.Now).WithWatermarkTrace(true)).Routes()
	for _, scope := range []string{"cms:recordings:read", "cms:recordings:write", "cms:bulletins:investigate", "cms:recordings:investigate"} {
		r := httptest.NewRequest("POST", "/api/admin/recordings/watermark-lookups", strings.NewReader(`{"recordingId":"00000000-0000-4000-8000-000000000002","code":"01234ABCDE"}`))
		r.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
		r.Header.Set("X-HHC-Auth-Provider", "account-api")
		r.Header.Set("X-HHC-User-ID", "00000000-0000-4000-8000-000000000003")
		r.Header.Set("X-HHC-Scopes", scope)
		r.Header.Set("X-HHC-Request-ID", "lookup-http")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		expected := http.StatusForbidden
		if scope == "cms:recordings:investigate" {
			expected = 200
		}
		if w.Code != expected {
			t.Fatalf("%s: %d %s", scope, w.Code, w.Body)
		}
		if expected == 200 && (w.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(w.Body.String(), "01234ABCDE")) {
			t.Fatal("lookup leaked code or is cacheable")
		}
	}
	r := serviceRequest("POST", "/api/admin/recordings/watermark-lookups")
	r.Header.Set("X-HHC-Scopes", "cms:recordings:investigate")
	r.Body = io.NopCloser(strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 && w.Code != 401 {
		t.Fatal("service lookup allowed", w.Code)
	}
	if repo.calls != 1 {
		t.Fatal("permission boundary bypassed", repo.calls)
	}
}
