package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
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
	repo := &contentRepository{public: []content.PublicItem{{ID: "video-1", Title: "為祢而闖"}}}
	response := httptest.NewRecorder()
	contentTestHandler(repo).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/videos?locale=zh-Hant", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if response.Header().Get("Cache-Control") == "" {
		t.Fatal("missing cache policy")
	}
}

func TestNewsPublishRequiresOwnedCleanProcessedCover(t *testing.T) {
	repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Status: content.StatusDraft, Version: 2, Slug: "news", DisplayDate: "2026-07-13", CoverAssetID: "asset-1", Translations: []content.Translation{{Locale: "zh-Hant", Title: "消息"}}}}
	uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-1", Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: "news-1", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready"}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/publish", nil)
	trusted(request, "cms:publish")
	request.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.grantID != "grant-1" {
		t.Fatalf("grant=%q", repo.grantID)
	}
}

func contentTestHandler(repo content.Repository) http.Handler {
	return contentTestHandlerWithAssets(repo, nil)
}
func contentTestHandlerWithAssets(repo content.Repository, uploads assetUploads) http.Handler {
	bulletinService := bulletins.NewService(&apiRepository{}, time.Now)
	return NewWithContent(bulletinService, content.NewService(repo, time.Now), nil, uploads).Routes()
}

type contentRepository struct {
	item    content.Item
	public  []content.PublicItem
	grantID string
}

func (r *contentRepository) CreateContent(_ context.Context, module content.Module, input content.WriteInput, actor, key string, now time.Time) (content.Item, error) {
	r.item = content.Item{ID: "item-1", Module: module, Status: content.StatusDraft, Version: 1, YouTubeVideoID: input.YouTubeVideoID, HomeEligible: input.HomeEligible, Translations: input.Translations, CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now}
	return r.item, nil
}
func (r *contentRepository) ListContent(context.Context, content.Module, int, int, string) (content.Page, error) {
	return content.Page{}, nil
}
func (r *contentRepository) GetContent(context.Context, content.Module, string) (content.Item, error) {
	return r.item, nil
}
func (r *contentRepository) UpdateContent(context.Context, content.Module, string, int64, content.WriteInput, string, time.Time) (content.Item, error) {
	return r.item, nil
}
func (r *contentRepository) PublishContent(_ context.Context, _ content.Module, _ string, _ int64, _, grantID string, _ time.Time) (content.Item, error) {
	r.grantID = grantID
	r.item.Status = content.StatusPublished
	return r.item, nil
}
func (r *contentRepository) UnpublishContent(context.Context, content.Module, string, int64, string, time.Time) (content.Item, error) {
	return r.item, nil
}
func (r *contentRepository) ContentRevisions(context.Context, content.Module, string) ([]content.Revision, error) {
	return nil, nil
}
func (r *contentRepository) RestoreContent(context.Context, content.Module, string, int64, int64, string, time.Time) (content.Item, error) {
	return r.item, nil
}
func (r *contentRepository) PublicContent(context.Context, content.Module, string, int) ([]content.PublicItem, error) {
	return r.public, nil
}
