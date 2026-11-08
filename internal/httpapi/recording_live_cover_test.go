package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLiveCoverSettingsWritesRequireHumanPublishCapability(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).Routes()
	for _, path := range []string{"/api/admin/recordings/live-cover-settings", "/api/admin/recordings/00000000-0000-4000-8000-000000000001/captures/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/cover-settings"} {
		for _, method := range []string{"PUT", "POST"} {
			route := path
			if method == "POST" {
				route += "/uploads"
			}
			req := httptest.NewRequest(method, route, strings.NewReader(`{"mode":"auto"}`))
			trusted(req, "cms:recordings:write")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != 403 {
				t.Fatalf("%s %s: %d %s", method, route, w.Code, w.Body)
			}
		}
	}
}
