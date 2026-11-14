package httpapi

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExplicitBulletinControlsFailClosedUntilCapabilityEnabled(t *testing.T) {
	for _, route := range []string{"publish", "online/general/zh-Hant/recover-publication"} {
		t.Run(route, func(t *testing.T) {
			repo := &onlineStateRepository{}
			body := `{"locale":"zh-Hant","onlinePublication":"pdf_only","onlineVersion":0}`
			if route != "publish" {
				body = `{"canonicalVersion":1,"revision":1,"layoutValidationHash":"` + strings.Repeat("a", 64) + `"}`
			}
			request := httptest.NewRequest("POST", "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/"+route, strings.NewReader(body))
			request.Header.Set("If-Match", `"1"`)
			request.Header.Set("Idempotency-Key", "recovery-test")
			trustedBulletinAudit(request, "cms:bulletins:publish")
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != 503 || !strings.Contains(response.Body.String(), "online_publication_unavailable") {
				t.Fatalf("controls not gated: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestRecoveryRequiresCurrentHumanPublishScopeAndExactInputs(t *testing.T) {
	for _, tc := range []struct {
		name, scope, hash, key, match string
		status                        int
	}{
		{"no-publish", "cms:bulletins:write", strings.Repeat("a", 64), "key", `"1"`, 403},
		{"malformed-hash", "cms:bulletins:publish", strings.Repeat("z", 64), "key", `"1"`, 400},
		{"uppercase-hash", "cms:bulletins:publish", strings.Repeat("A", 64), "key", `"1"`, 400},
		{"missing-key", "cms:bulletins:publish", strings.Repeat("a", 64), "", `"1"`, 400},
		{"missing-cas", "cms:bulletins:publish", strings.Repeat("a", 64), "key", "", 428},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest("POST", "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/online/general/zh-Hant/recover-publication", strings.NewReader(`{"canonicalVersion":1,"revision":1,"layoutValidationHash":"`+tc.hash+`"}`))
			trustedBulletinAudit(request, tc.scope)
			request.Header.Set("If-Match", tc.match)
			if tc.key != "" {
				request.Header.Set("Idempotency-Key", tc.key)
			}
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(&onlineStateRepository{}, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}
