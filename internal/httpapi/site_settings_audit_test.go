package httpapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

func TestAdminSiteSettingsReadsPersistAuditBeforeResponse(t *testing.T) {
	now := time.Date(2026, 9, 16, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		path, action string
	}{
		{"/api/admin/site-settings", "cms.site_settings.read"},
		{"/api/admin/site-settings/revisions", "cms.site_settings.revisions.read"},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			outbox := &bulletinAuditOutbox{}
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			trustedBulletinAudit(request, "cms:pages:read")
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil, "api-gateway", "", false).
				WithSiteSettings(sitesettings.NewService(&siteSettingsRepository{settings: validSiteSettings()}, time.Now)).
				WithAuditOutbox(outbox, func() time.Time { return now }).Routes().ServeHTTP(response, request)
			if response.Code != http.StatusOK || len(outbox.events) != 1 {
				t.Fatalf("status=%d events=%d body=%s", response.Code, len(outbox.events), response.Body.String())
			}
			event := outbox.events[0]
			if event.Action != test.action || event.ResourceID != "singleton" || event.ActorID != bulletinAuditActor || event.RequestID != bulletinAuditRequest || !reflect.DeepEqual(event.Metadata, auditclient.Metadata{}) {
				t.Fatalf("event=%#v", event)
			}
		})
	}
}

func TestAdminSiteSettingsAuditFailureDoesNotReturnData(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/admin/site-settings", nil)
	trustedBulletinAudit(request, "cms:pages:read")
	response := httptest.NewRecorder()
	NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil, "api-gateway", "", false).
		WithSiteSettings(sitesettings.NewService(&siteSettingsRepository{settings: validSiteSettings()}, time.Now)).
		WithAuditOutbox(&bulletinAuditOutbox{err: errors.New("injected")}, time.Now).Routes().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("ETag") != "" || strings.Contains(response.Body.String(), `"id":"default"`) || !strings.Contains(response.Body.String(), `"code":"audit_unavailable"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}
