package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGranularCMSPermissionFamiliesDenySiblingRoutes(t *testing.T) {
	tests := []struct {
		name, path, scope string
		handler           http.Handler
	}{
		{"bulletin", "/api/admin/bulletins", "cms:news:read", testHandler(&apiRepository{})},
		{"news", "/api/admin/content/news", "cms:pages:read", contentTestHandler(&contentRepository{})},
		{"pages", "/api/admin/content/videos", "cms:news:read", contentTestHandler(&contentRepository{})},
		{"page settings", "/api/admin/site-settings", "cms:bulletins:read", siteSettingsTestHandler(&siteSettingsRepository{})},
		{"campaign", "/api/admin/campaigns", "cms:bulletins:read", campaignTestHandler()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			trusted(request, test.scope)
			response := httptest.NewRecorder()
			test.handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBulletinInvestigationPermissionCanListIssueOptions(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletins", nil)
	trusted(request, "cms:bulletins:investigate")
	response := httptest.NewRecorder()
	testHandler(&apiRepository{}).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func campaignTestHandler() http.Handler {
	return NewWithContent(nil, nil, nil, nil, "api-gateway", "", true, &engagementProxyFake{}).Routes()
}
