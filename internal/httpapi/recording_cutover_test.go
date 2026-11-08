package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestRetiredRecordingRoutesRejectBeforeDependencies(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).Routes()
	for _, tc := range []struct{ method, suffix string }{
		{"GET", "files"}, {"POST", "upload-sessions"}, {"GET", "upload-sessions/session"},
		{"GET", "upload-sessions/session/parts"}, {"POST", "upload-sessions/session/parts/1"},
		{"POST", "upload-sessions/session/complete"}, {"GET", "assets/version"},
	} {
		r := httptest.NewRequest(tc.method, "/api/admin/recordings/rec-a/"+tc.suffix, nil)
		trusted(r, "cms:recordings:read cms:recordings:write cms:recordings:publish")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Errorf("%s %s: got %d", tc.method, tc.suffix, w.Code)
		}
	}
}
