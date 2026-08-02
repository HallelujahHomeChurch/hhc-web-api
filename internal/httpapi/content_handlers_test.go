package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

func TestContentRoutesEnforceScopeAndConcurrency(t *testing.T) {
	handler := contentTestHandler(&contentRepository{})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/videos", bytes.NewBufferString(`{"youtubeVideoId":"K3ckFWeSQ-k","homeEligible":true,"translations":[{"locale":"zh-Hant","title":"為祢而闖"}]}`))
	trusted(request, "cms:write")
	request.Header.Set("Idempotency-Key", "video-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPut, "/api/admin/content/videos/item-1", bytes.NewBufferString(`{"youtubeVideoId":"K3ckFWeSQ-k","homeEligible":true,"translations":[{"locale":"zh-Hant","title":"為祢而闖"}]}`))
	trusted(request, "cms:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestPublicContentReadsProjectionWithoutAuthentication(t *testing.T) {
	repo := &contentRepository{public: []content.PublicItem{{ID: "video-1", Title: "為祢而闖"}}, publicTotal: 6}
	response := httptest.NewRecorder()
	contentTestHandler(repo).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/videos?locale=zh-Hant&page=2&pageSize=5", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if response.Header().Get("Cache-Control") == "" {
		t.Fatal("missing cache policy")
	}
	if !strings.Contains(response.Body.String(), `"meta":{"page":2,"pageSize":5,"total":6}`) {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestPublicNewsDetailUsesProjectionETag(t *testing.T) {
	repo := &contentRepository{
		publicNews: content.PublicItem{ID: "news-1", Title: "最新消息"},
		publicETag: "news-etag",
	}
	handler := contentTestHandler(repo)
	request := httptest.NewRequest(http.MethodGet, "/api/news/announcement?locale=zh-Hant", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"news-etag"` ||
		response.Header().Get("Cache-Control") != "public, no-cache" {
		t.Fatalf("status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/news/announcement?locale=zh-Hant", nil)
	request.Header.Set("If-None-Match", `"other", W/"news-etag"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotModified || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/news/announcement?locale=zh-Hant", nil)
	request.Header.Set("If-None-Match", "*")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotModified {
		t.Fatalf("wildcard status=%d", response.Code)
	}

	repo.publicNewsErr = content.ErrNotFound
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/news/missing?locale=zh-Hant", nil))
	if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "public, max-age=30" {
		t.Fatalf("status=%d cache=%q", response.Code, response.Header().Get("Cache-Control"))
	}
}

func TestPublicHomeIsCacheableAndSelectsVideosDeterministically(t *testing.T) {
	values := []content.PublicItem{
		{ID: "1", HomeEligible: true},
		{ID: "2", HomeEligible: false},
		{ID: "3", HomeEligible: true},
		{ID: "4", HomeEligible: true},
		{ID: "5", HomeEligible: true},
	}
	first := eligibleVideos(values, 3, "2026-07-31:zh-Hant")
	second := eligibleVideos(values, 3, "2026-07-31:zh-Hant")
	if len(first) != 3 || !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	for _, value := range first {
		if !value.HomeEligible {
			t.Fatalf("ineligible video selected: %#v", value)
		}
	}

	repo := &contentRepository{public: values}
	response := httptest.NewRecorder()
	contentTestHandler(repo).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/home?locale=zh-Hant", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") == "" {
		t.Fatalf("status=%d cache=%q", response.Code, response.Header().Get("Cache-Control"))
	}
}

func TestPublicHomeUsesLatestNewsOrder(t *testing.T) {
	values := []content.PublicItem{
		{ID: "newest"},
		{ID: "older", Featured: true},
		{ID: "oldest"},
		{ID: "ignored", Featured: true},
	}
	got := latestNews(values, 3)
	if len(got) != 3 || got[0].ID != "newest" || got[2].ID != "oldest" {
		t.Fatalf("news=%#v", got)
	}
}

func TestNewsPublishQueuesOwnedCoverWhileScanIsPending(t *testing.T) {
	repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Status: content.StatusDraft, Version: 2, Slug: "news", DisplayDate: "2026-07-13", CoverAssetID: "asset-1", Translations: []content.Translation{{Locale: "zh-Hant", Title: "消息", Summary: "消息摘要"}}}}
	uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-1", Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: "news-1", UploadStatus: "completed", ScanStatus: "pending", ProcessingStatus: "pending"}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/publish", nil)
	trusted(request, "cms:publish")
	request.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.item.Status != content.StatusPublishing {
		t.Fatalf("status=%q", repo.item.Status)
	}
}

func TestNewsPublishRejectsUnknownAssetStates(t *testing.T) {
	tests := []struct {
		name       string
		scan       string
		processing string
	}{
		{name: "unknown scan", scan: "unknown", processing: "pending"},
		{name: "unknown processing", scan: "clean", processing: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Status: content.StatusDraft, Version: 2, Slug: "news", DisplayDate: "2026-07-13", CoverAssetID: "asset-1", Translations: []content.Translation{{Locale: "zh-Hant", Title: "消息", Summary: "消息摘要"}}}}
			uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-1", Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: "news-1", UploadStatus: "completed", ScanStatus: test.scan, ProcessingStatus: test.processing}}
			request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/publish", nil)
			trusted(request, "cms:publish")
			request.Header.Set("If-Match", `"2"`)
			response := httptest.NewRecorder()
			contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
			if response.Code != http.StatusUnprocessableEntity || repo.item.Status != content.StatusDraft {
				t.Fatalf("status=%d contentStatus=%q body=%s", response.Code, repo.item.Status, response.Body.String())
			}
		})
	}
}

func TestCompleteNewsCoverRejectsOwnerBeforeMutation(t *testing.T) {
	repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Version: 1}}
	uploads := &apiUploads{completed: assetclient.Asset{
		ID: "asset-1", Namespace: "cms.news.cover", OwnerService: "hhc-web-api",
		OwnerType: "news", OwnerID: "another-news",
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/assets/asset-1/complete", bytes.NewBufferString(`{"fileName":"cover.jpg","mimeType":"image/jpeg","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:write assets:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || uploads.completeCalls != 0 {
		t.Fatalf("status=%d completeCalls=%d", response.Code, uploads.completeCalls)
	}
}

func TestCompleteNewsCoverReportsAssetServiceUnavailable(t *testing.T) {
	uploads := &apiUploads{getError: assetclient.ErrUnavailable}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/assets/asset-1/complete", bytes.NewBufferString(`{"fileName":"cover.jpg","mimeType":"image/jpeg","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:write assets:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(&contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Version: 1}}, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || uploads.completeCalls != 0 {
		t.Fatalf("status=%d completeCalls=%d", response.Code, uploads.completeCalls)
	}
}

func TestNewsCoverStatusIsSafeAndFailedScanCanBeRetried(t *testing.T) {
	uploads := &apiUploads{completed: assetclient.Asset{
		ID: "asset-1", Namespace: "cms.news.cover", OwnerService: "hhc-web-api",
		OwnerType: "news", OwnerID: "news-1", UploadStatus: "completed",
		ScanStatus: "failed", ProcessingStatus: "pending",
	}}
	handler := contentTestHandlerWithAssets(&contentRepository{}, uploads)

	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/news/news-1/assets/asset-1", nil)
	trusted(request, "cms:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "ownerService") || !strings.Contains(response.Body.String(), `"retryable":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/assets/asset-1/scan/retry", nil)
	trusted(request, "cms:write assets:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || uploads.requeueCalls != 1 || !strings.Contains(response.Body.String(), `"scanStatus":"pending"`) {
		t.Fatalf("status=%d requeueCalls=%d body=%s", response.Code, uploads.requeueCalls, response.Body.String())
	}
}

func TestNewsUnpublishDoesNotDependOnCurrentDraftCover(t *testing.T) {
	repo := &contentRepository{item: content.Item{
		ID: "news-1", Module: content.ModuleNews, Status: content.StatusDraft, Version: 3,
		Slug: "news", DisplayDate: "2026-07-13", IsPublished: true,
		Translations: []content.Translation{{Locale: "zh-Hant", Title: "消息"}},
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/unpublish", nil)
	trusted(request, "cms:publish")
	request.Header.Set("If-Match", `"3"`)
	response := httptest.NewRecorder()
	contentTestHandler(repo).ServeHTTP(response, request)
	if response.Code != http.StatusOK || repo.item.Status != content.StatusUnpublishing {
		t.Fatalf("status=%d item=%#v body=%s", response.Code, repo.item, response.Body.String())
	}
}

func TestContentDeleteRequiresWriteScopeAndVersion(t *testing.T) {
	repo := &contentRepository{
		item: content.Item{ID: "video-1", Module: content.ModuleVideos, Status: content.StatusDraft, Version: 2},
	}
	handler := contentTestHandler(repo)

	deleteRequest := httptest.NewRequest(http.MethodDelete, "/api/admin/content/videos/video-1", nil)
	trusted(deleteRequest, "cms:write")
	deleteRequest.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, deleteRequest)
	if response.Code != http.StatusNoContent || !repo.deleted {
		t.Fatalf("status=%d item=%#v body=%s", response.Code, repo.item, response.Body.String())
	}
}

func TestContentListRejectsInvalidFilters(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/news?status=unknown", nil)
	trusted(request, "cms:read")
	response := httptest.NewRecorder()
	contentTestHandler(&contentRepository{}).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func contentTestHandler(repo content.Repository) http.Handler {
	return contentTestHandlerWithAssets(repo, nil)
}
func contentTestHandlerWithAssets(repo content.Repository, uploads assetUploads) http.Handler {
	bulletinService := bulletins.NewService(&apiRepository{}, time.Now)
	return NewWithContent(bulletinService, content.NewService(repo, time.Now), nil, uploads, "api-gateway", "", false).Routes()
}

type contentRepository struct {
	item          content.Item
	public        []content.PublicItem
	publicTotal   int64
	publicNews    content.PublicItem
	publicETag    string
	publicNewsErr error
	deleted       bool
}

func (r *contentRepository) CreateContent(_ context.Context, module content.Module, input content.WriteInput, actor, key string, now time.Time) (content.Item, error) {
	r.item = content.Item{ID: "item-1", Module: module, Status: content.StatusDraft, Version: 1, YouTubeVideoID: input.YouTubeVideoID, HomeEligible: input.HomeEligible, Translations: input.Translations, CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now}
	return r.item, nil
}
func (r *contentRepository) ListContent(context.Context, content.Module, content.ListOptions) (content.Page, error) {
	return content.Page{}, nil
}
func (r *contentRepository) GetContent(context.Context, content.Module, string) (content.Item, error) {
	return r.item, nil
}
func (r *contentRepository) UpdateContent(context.Context, content.Module, string, int64, content.WriteInput, string, time.Time) (content.Item, error) {
	return r.item, nil
}
func (r *contentRepository) PublishContent(_ context.Context, _ content.Module, _ string, _ int64, _ string, _ time.Time) (content.Item, error) {
	r.item.Status = content.StatusPublishing
	return r.item, nil
}
func (r *contentRepository) UnpublishContent(context.Context, content.Module, string, int64, string, time.Time) (content.Item, error) {
	r.item.Status = content.StatusUnpublishing
	return r.item, nil
}
func (r *contentRepository) ContentRevisions(context.Context, content.Module, string) ([]content.Revision, error) {
	return nil, nil
}
func (r *contentRepository) RestoreContent(context.Context, content.Module, string, int64, int64, string, time.Time) (content.Item, error) {
	return r.item, nil
}
func (r *contentRepository) DeleteContent(context.Context, content.Module, string, int64, string, time.Time) error {
	r.deleted = true
	return nil
}
func (r *contentRepository) PublicContent(_ context.Context, _ content.Module, _ string, page, pageSize int) (content.PublicPage, error) {
	return content.PublicPage{Items: r.public, Page: page, PageSize: pageSize, Total: r.publicTotal}, nil
}
func (r *contentRepository) PublicNews(context.Context, string, string) (content.PublicItem, string, error) {
	return r.publicNews, r.publicETag, r.publicNewsErr
}
