package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

func TestBulletinPublicationUpdateRequiresBothScopes(t *testing.T) {
	for _, tc := range []struct {
		name, scopes, key, match, body string
		status                         int
	}{
		{"writer", "cms:bulletins:write", "update-1", "1", "{}", http.StatusForbidden},
		{"publisher", "cms:bulletins:publish", "update-1", "1", "{}", http.StatusForbidden},
		{"no key", "cms:bulletins:write cms:bulletins:publish", "", "1", "{}", http.StatusBadRequest},
		{"no match", "cms:bulletins:write cms:bulletins:publish", "update-1", "", "{}", http.StatusPreconditionRequired},
		{"publish without token", "cms:bulletins:write cms:bulletins:publish", "update-1", "1", `{"action":"publish"}`, http.StatusBadRequest},
		{"null action", "cms:bulletins:write cms:bulletins:publish", "update-1", "1", `{"action":null}`, http.StatusBadRequest},
		{"invalid date", "cms:bulletins:write cms:bulletins:publish", "update-1", "1", `{"commandToken":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","metadata":{"issueNumber":1740,"issueDate":"2026-11-31","title":"Weekly","subtitle":""}}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/publication-update", strings.NewReader(tc.body))
			trustedBulletinAudit(request, tc.scopes)
			request.Header.Set("Idempotency-Key", tc.key)
			request.Header.Set("If-Match", tc.match)
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(&onlineStateRepository{}, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d want=%d %s", response.Code, tc.status, response.Body.String())
			}
		})
	}
}
