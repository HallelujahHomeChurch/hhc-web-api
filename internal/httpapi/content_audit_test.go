package httpapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

func TestAdminContentReadsPersistAuditBeforeResponse(t *testing.T) {
	now := time.Date(2026, 9, 16, 4, 0, 0, 0, time.UTC)
	contentID := "018f47d2-e5d1-4f3f-8f18-6c8e32621f74"
	assetID := strings.Repeat("b", 32)
	tests := []struct {
		name, path, action, resourceID string
		item                           content.Item
		asset                          assetclient.Asset
		metadata                       auditclient.Metadata
	}{
		{"news list", "/api/admin/content/news?status=draft&q=week&page=2&sort=displayDate", "cms.content.list", "", content.Item{}, assetclient.Asset{}, auditclient.Metadata{"module": "news"}},
		{"page-family read", "/api/admin/content/history/" + strings.ToUpper(contentID), "cms.content.read", contentID, content.Item{ID: contentID, Module: content.ModuleHistory, Version: 7}, assetclient.Asset{}, auditclient.Metadata{"module": "pages"}},
		{"page revisions", "/api/admin/content/pages/" + strings.ToUpper(contentID) + "/revisions", "cms.content.revisions.read", contentID, content.Item{}, assetclient.Asset{}, auditclient.Metadata{"module": "pages"}},
		{"news asset", "/api/admin/content/news/" + contentID + "/assets/" + assetID, "cms.content.asset_status.read", assetID, content.Item{}, assetclient.Asset{ID: assetID, Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: contentID, Purpose: "news_detail_cover"}, auditclient.Metadata{"module": "news"}},
		{"page asset", "/api/admin/content/pages/" + contentID + "/assets/" + assetID, "cms.content.asset_status.read", assetID, content.Item{ID: contentID, Module: content.ModulePages, PageKey: "home", PageTemplate: "home.v2", RoutePath: "/"}, assetclient.Asset{ID: assetID, Namespace: "cms.home.banner", OwnerService: "hhc-web-api", OwnerType: "page", OwnerID: contentID, Purpose: "home_banner"}, auditclient.Metadata{"module": "pages"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &contentRepository{item: test.item}
			uploads := &apiUploads{completed: test.asset}
			outbox := &bulletinAuditOutbox{}
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			trustedBulletinAudit(request, "cms:news:read cms:pages:read")
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), content.NewService(repo, time.Now), nil, uploads, "api-gateway", "", false).
				WithAuditOutbox(outbox, func() time.Time { return now }).Routes().ServeHTTP(response, request)
			if response.Code != http.StatusOK || len(outbox.events) != 1 {
				t.Fatalf("status=%d events=%d body=%s", response.Code, len(outbox.events), response.Body.String())
			}
			event := outbox.events[0]
			if event.Action != test.action || event.ResourceID != test.resourceID || event.ActorID != bulletinAuditActor || event.RequestID != bulletinAuditRequest || !reflect.DeepEqual(event.Metadata, test.metadata) {
				t.Fatalf("event=%#v", event)
			}
		})
	}
}

func TestAdminContentAuditFailureDoesNotReturnData(t *testing.T) {
	contentID := "018f47d2-e5d1-4f3f-8f18-6c8e32621f74"
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/news/"+contentID, nil)
	trustedBulletinAudit(request, "cms:news:read cms:pages:read")
	response := httptest.NewRecorder()
	NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), content.NewService(&contentRepository{item: content.Item{ID: contentID, Module: content.ModuleNews, Version: 7}}, time.Now), nil, nil, "api-gateway", "", false).
		WithAuditOutbox(&bulletinAuditOutbox{err: content.ErrConflict}, time.Now).Routes().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("ETag") != "" || strings.Contains(response.Body.String(), contentID) || !strings.Contains(response.Body.String(), `"code":"audit_unavailable"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestAssetOwnedContentMutationsDoNotEmitHHCAuditEvents(t *testing.T) {
	contentID := "018f47d2-e5d1-4f3f-8f18-6c8e32621f74"
	assetID := strings.Repeat("b", 32)
	tests := []struct {
		name, path, body string
		status           int
	}{
		{"upload session", "/api/admin/content/news/" + contentID + "/upload-sessions", `{"fileName":"cover.jpg","mimeType":"image/jpeg","sizeBytes":128}`, http.StatusCreated},
		{"scan retry", "/api/admin/content/news/" + contentID + "/assets/" + assetID + "/scan/retry", "", http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			uploads := &apiUploads{completed: assetclient.Asset{ID: assetID, Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: contentID, Purpose: "news_detail_cover", ScanStatus: "failed"}}
			outbox := &bulletinAuditOutbox{}
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewBufferString(test.body))
			request.Header.Set("Idempotency-Key", "content-asset-audit-test")
			trustedBulletinAudit(request, "cms:news:write")
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), content.NewService(&contentRepository{item: content.Item{ID: contentID, Module: content.ModuleNews}}, time.Now), nil, uploads, "api-gateway", "", false).
				WithAuditOutbox(outbox, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != test.status || len(outbox.events) != 0 {
				t.Fatalf("status=%d events=%d body=%s", response.Code, len(outbox.events), response.Body.String())
			}
		})
	}
}
