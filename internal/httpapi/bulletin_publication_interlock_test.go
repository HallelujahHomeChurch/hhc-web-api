package httpapi

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type interlockPublicationRepository struct{ apiRepository }

func (r *interlockPublicationRepository) StartPublishSelection(context.Context, string, bulletins.PublishInput, int64, string, time.Time) (bulletins.Workflow, error) {
	return bulletins.Workflow{}, bulletins.ErrAutomaticPublicationActive
}

func TestOrdinaryPDFInterlockReturns409(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/publish", strings.NewReader(`{"locale":"zh-Hant"}`))
	trustedBulletinAudit(request, "cms:bulletins:publish")
	request.Header.Set("If-Match", `"3"`)
	response := httptest.NewRecorder()
	NewWithContent(bulletins.NewService(&interlockPublicationRepository{}, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
	if response.Code != 409 || !strings.Contains(response.Body.String(), `"code":"automatic_publication_active"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestExplicitPDFOnlyRemainsHumanOnlyAtExistingTrustBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, scope string
		status      int
	}{
		{"valid-service-principal", "cms:recordings:read cms:recordings:write cms:recordings:publish", 403},
		{"untrusted-service-bulletin-scope", "cms:bulletins:publish", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := serviceRequest("POST", "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/publish")
			request.Body = io.NopCloser(strings.NewReader(`{"locale":"zh-Hant","onlinePublication":"pdf_only","onlineVersion":0}`))
			request.Header.Set("X-HHC-Scopes", tc.scope)
			request.Header.Set("If-Match", `"1"`)
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil, "api-gateway", "", false).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("service principal entered bulletin publication: %d %s", response.Code, response.Body.String())
			}
		})
	}
}
