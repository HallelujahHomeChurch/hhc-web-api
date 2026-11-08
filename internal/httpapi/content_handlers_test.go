package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestAdminContentGetIncludesPublicationProgress(t *testing.T) {
	tests := []struct {
		name      string
		item      content.Item
		uploads   *contentProgressUploads
		wantStage string
		wantState string
		wantCalls int
	}{
		{
			name:      "ordinary news",
			item:      content.Item{ID: "news-1", Module: content.ModuleNews, Status: content.StatusPublishing, Version: 2, CoverAssetID: "cover-1"},
			uploads:   &contentProgressUploads{assets: map[string]assetclient.Asset{"cover-1": progressAsset("cover-1", "clean", "ready")}},
			wantStage: "publishing",
			wantState: "running",
			wantCalls: 1,
		},
		{
			name:      "statement",
			item:      content.Item{ID: "statement-1", Module: content.ModuleNews, Kind: content.KindStatement, Status: content.StatusPublishing, Version: 4, Translations: []content.Translation{{Locale: "zh-Hant", BodyJSON: statementImageBody("inline-1")}}},
			uploads:   &contentProgressUploads{assets: map[string]assetclient.Asset{"inline-1": progressAsset("inline-1", "pending", "pending")}},
			wantStage: "security_check",
			wantState: "running",
			wantCalls: 1,
		},
		{
			name:      "published avoids asset lookup",
			item:      content.Item{ID: "news-1", Module: content.ModuleNews, Status: content.StatusPublished, Version: 3, CoverAssetID: "cover-1"},
			uploads:   &contentProgressUploads{assets: map[string]assetclient.Asset{}},
			wantStage: "published",
			wantState: "ready",
			wantCalls: 0,
		},
		{
			name:      "asset lookup failure is conservative",
			item:      content.Item{ID: "news-1", Module: content.ModuleNews, Status: content.StatusPublishing, Version: 2, CoverAssetID: "cover-1"},
			uploads:   &contentProgressUploads{err: errors.New("unavailable")},
			wantStage: "queued",
			wantState: "running",
			wantCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/admin/content/news/"+test.item.ID, nil)
			trusted(request, "cms:news:read")
			response := httptest.NewRecorder()
			contentTestHandlerWithAssets(&contentRepository{item: test.item}, test.uploads).ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Data content.Item `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Data.OperationProgress == nil || body.Data.OperationProgress.Stage != test.wantStage || body.Data.OperationProgress.Status != test.wantState || test.uploads.calls != test.wantCalls {
				t.Fatalf("progress=%#v calls=%d body=%s", body.Data.OperationProgress, test.uploads.calls, response.Body.String())
			}
		})
	}
}

func TestAdminContentGetOmitsProgressForDraft(t *testing.T) {
	uploads := &contentProgressUploads{assets: map[string]assetclient.Asset{}}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/news/news-1", nil)
	trusted(request, "cms:news:read")
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(&contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Status: content.StatusDraft, Version: 2}}, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "operationProgress") || uploads.calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, uploads.calls, response.Body.String())
	}
}

func TestContentRoutesEnforceScopeAndConcurrency(t *testing.T) {
	handler := contentTestHandler(&contentRepository{})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/videos", bytes.NewBufferString(`{"youtubeVideoId":"K3ckFWeSQ-k","homeEligible":true,"translations":[{"locale":"zh-Hant","title":"為祢而闖"}]}`))
	trusted(request, "cms:pages:write")
	request.Header.Set("Idempotency-Key", "video-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPut, "/api/admin/content/videos/item-1", bytes.NewBufferString(`{"youtubeVideoId":"K3ckFWeSQ-k","homeEligible":true,"translations":[{"locale":"zh-Hant","title":"為祢而闖"}]}`))
	trusted(request, "cms:pages:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestContentUpdateRejectsOmittedLocale(t *testing.T) {
	repo := &contentRepository{item: content.Item{
		ID: "video-1", Module: content.ModuleVideos, Version: 2, YouTubeVideoID: "K3ckFWeSQ-k",
		Translations: []content.Translation{{Locale: "zh-Hant", Title: "影片"}, {Locale: "en", Title: "Video"}},
	}}
	request := httptest.NewRequest(http.MethodPut, "/api/admin/content/videos/video-1", bytes.NewBufferString(`{"youtubeVideoId":"K3ckFWeSQ-k","translations":[{"locale":"zh-Hant","title":"影片更新"}]}`))
	trusted(request, "cms:news:write cms:pages:write")
	request.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()

	contentTestHandler(repo).ServeHTTP(response, request)

	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"locale_set_mismatch"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
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

func TestPublicLocationsReadsExactLocaleProjectionWithoutDraftFields(t *testing.T) {
	repo := &contentRepository{publicLocations: []content.PublicLocation{{
		ID: "taipei", Name: "台北哈利路亞家教會", Address: "仁愛路三段29號B1", MapHref: "https://maps.app.goo.gl/fDus6nVswbuhSEAd8", SortOrder: 10,
		ResolvedLocale: "ja", AvailableLocales: []string{"zh-Hant", "ja"},
	}}}
	response := httptest.NewRecorder()

	contentTestHandler(repo).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/locations?locale=ja", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.publicLocationsLocale != "ja" {
		t.Fatalf("locale=%q", repo.publicLocationsLocale)
	}
	var envelope struct {
		Data []struct {
			ID               string   `json:"id"`
			Name             string   `json:"name"`
			Address          string   `json:"address"`
			MapHref          string   `json:"mapHref"`
			SortOrder        int      `json:"sortOrder"`
			ResolvedLocale   string   `json:"resolvedLocale"`
			AvailableLocales []string `json:"availableLocales"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].ID != "taipei" || envelope.Data[0].Name != "台北哈利路亞家教會" || envelope.Data[0].Address != "仁愛路三段29號B1" || envelope.Data[0].MapHref != "https://maps.app.goo.gl/fDus6nVswbuhSEAd8" || envelope.Data[0].SortOrder != 10 || envelope.Data[0].ResolvedLocale != "ja" || !reflect.DeepEqual(envelope.Data[0].AvailableLocales, []string{"zh-Hant", "ja"}) {
		t.Fatalf("data=%#v", envelope.Data)
	}
	for _, draftField := range []string{"contentID", "status", "version", "createdBy", "updatedBy"} {
		if strings.Contains(response.Body.String(), `"`+draftField+`"`) {
			t.Fatalf("draft field %q exposed: %s", draftField, response.Body.String())
		}
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

func TestPublicEditorialPageUsesExactLocaleProjectionAndETag(t *testing.T) {
	repo := &contentRepository{publicEditorialPage: content.PublicEditorialPage{PageKey: "home", Template: "home.v1", RoutePath: "/", Indexable: true, Content: validHTTPPagePayload(), ResolvedLocale: "ja", AvailableLocales: []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}, Version: 3, PublishedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)}, publicEditorialETag: "page-etag"}
	handler := contentTestHandler(repo)
	request := httptest.NewRequest(http.MethodGet, "/api/pages/home?locale=ja", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"page-etag"` || response.Header().Get("Cache-Control") != "public, max-age=30, must-revalidate" || repo.publicEditorialLocale != "ja" || repo.publicEditorialKey != "home" {
		t.Fatalf("status=%d headers=%v key=%q locale=%q body=%s", response.Code, response.Header(), repo.publicEditorialKey, repo.publicEditorialLocale, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/pages/home?locale=ja", nil)
	request.Header.Set("If-None-Match", `W/"page-etag"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotModified || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPublicEditorialPageRejectsUnknownKey(t *testing.T) {
	response := httptest.NewRecorder()
	contentTestHandler(&contentRepository{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/pages/custom?locale=en", nil))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"not_found"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPagesRejectGenericCreateAndDelete(t *testing.T) {
	handler := contentTestHandler(&contentRepository{})
	create := httptest.NewRequest(http.MethodPost, "/api/admin/content/pages", bytes.NewBufferString(`{}`))
	trusted(create, "cms:news:write cms:pages:write")
	create.Header.Set("Idempotency-Key", "page-custom")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, create)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	remove := httptest.NewRequest(http.MethodDelete, "/api/admin/content/pages/page-1", nil)
	trusted(remove, "cms:news:write cms:pages:write")
	remove.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, remove)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPageUpdateReturnsValidationEnvelope(t *testing.T) {
	repo := &contentRepository{item: content.Item{ID: "page-1", Module: content.ModulePages, Version: 2, PageKey: "home", PageTemplate: "home.v1", RoutePath: "/", Indexable: true, Translations: []content.Translation{{Locale: "zh-Hant", BodyJSON: validHTTPPagePayload()}}}}
	request := httptest.NewRequest(http.MethodPut, "/api/admin/content/pages/page-1", bytes.NewBufferString(`{"pageKey":"home","pageTemplate":"home.v1","routePath":"/","indexable":true,"translations":[{"locale":"zh-Hant","bodyJson":{"schemaVersion":1,"template":"home.v1","data":{"heroTitle":"Only one field"}}}]}`))
	trusted(request, "cms:news:write cms:pages:write")
	request.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()
	contentTestHandler(repo).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(response.Body.String(), `"code":"invalid_content"`) {
		t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
}

func TestPageUpdateRequiresBooleanIndexable(t *testing.T) {
	base := `{"pageKey":"home","pageTemplate":"home.v1","routePath":"/",%s"translations":[{"locale":"zh-Hant","bodyJson":` + string(validHTTPPagePayload()) + `}]}`
	for _, test := range []struct {
		name, field string
		status      int
		want        bool
	}{
		{"omitted", "", http.StatusBadRequest, false},
		{"null", `"indexable":null,`, http.StatusBadRequest, false},
		{"string", `"indexable":"false",`, http.StatusBadRequest, false},
		{"number", `"indexable":0,`, http.StatusBadRequest, false},
		{"object", `"indexable":{},`, http.StatusBadRequest, false},
		{"false", `"indexable":false,`, http.StatusOK, false},
		{"true", `"indexable":true,`, http.StatusOK, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &contentRepository{item: content.Item{ID: "page-1", Module: content.ModulePages, Version: 2, PageKey: "home", PageTemplate: "home.v1", RoutePath: "/", Indexable: true, Translations: []content.Translation{{Locale: "zh-Hant", BodyJSON: validHTTPPagePayload()}}}}
			request := httptest.NewRequest(http.MethodPut, "/api/admin/content/pages/page-1", bytes.NewBufferString(fmt.Sprintf(base, test.field)))
			trusted(request, "cms:news:write cms:pages:write")
			request.Header.Set("If-Match", `"2"`)
			response := httptest.NewRecorder()
			contentTestHandler(repo).ServeHTTP(response, request)
			if response.Code != test.status || (test.status == http.StatusBadRequest && !strings.Contains(response.Body.String(), `"code":"invalid_request"`)) || (test.status == http.StatusOK && repo.updateInput.Indexable != test.want) {
				t.Fatalf("status=%d input=%#v body=%s", response.Code, repo.updateInput, response.Body.String())
			}
		})
	}
}

func validHTTPPagePayload() json.RawMessage {
	return json.RawMessage(`{"schemaVersion":1,"template":"home.v1","data":{"heroTitle":"Home","heroSubtitle":"Welcome","newsTitle":"News","moreNews":"More","weeklyTitle":"Weekly","downloadWeekly":"Download","videosTitle":"Videos","videosSubtitle":"Music","watchMore":"Watch","aboutTitle":"About","aboutBody":"About us","aboutCta":"Meet us","locationsTitle":"Locations","mapLink":"Map"}}`)
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

func TestHomeVideoSeedUsesTaipeiDateWithoutLocale(t *testing.T) {
	before := time.Date(2026, 8, 30, 15, 59, 0, 0, time.UTC)
	after := time.Date(2026, 8, 30, 16, 1, 0, 0, time.UTC)
	if got := homeVideoSeed(before); got != "2026-08-30" {
		t.Fatalf("before=%s", got)
	}
	if got := homeVideoSeed(after); got != "2026-08-31" {
		t.Fatalf("after=%s", got)
	}
}

func TestPublicHomeReturnsUpToFourNewsAndThreeVideos(t *testing.T) {
	for _, test := range []struct {
		name      string
		available int
		wantNews  int
		wantVideo int
	}{
		{name: "empty", available: 0, wantNews: 0, wantVideo: 0},
		{name: "fewer than limit", available: 2, wantNews: 2, wantVideo: 2},
		{name: "limits each feed", available: 5, wantNews: 4, wantVideo: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			values := []content.PublicItem{{ID: "newest", HomeEligible: true}, {ID: "second", HomeEligible: true}, {ID: "third", HomeEligible: true}, {ID: "fourth", HomeEligible: true}, {ID: "fifth", HomeEligible: true}}
			response := httptest.NewRecorder()
			contentTestHandler(&contentRepository{public: values[:test.available]}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/home?locale=zh-Hant", nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Data struct {
					News   []content.PublicItem `json:"news"`
					Videos []content.PublicItem `json:"videos"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Data.News) != test.wantNews || len(body.Data.Videos) != test.wantVideo {
				t.Fatalf("news=%d videos=%d", len(body.Data.News), len(body.Data.Videos))
			}
			for i, item := range body.Data.News {
				if item.ID != values[i].ID {
					t.Fatalf("news[%d]=%s, want %s", i, item.ID, values[i].ID)
				}
			}
		})
	}
}

func TestEligibleVideosAreDeterministicForTaipeiDate(t *testing.T) {
	values := []content.PublicItem{{ID: "a", HomeEligible: true}, {ID: "b", HomeEligible: true}, {ID: "c", HomeEligible: true}, {ID: "d", HomeEligible: true}}
	seed := homeVideoSeed(time.Date(2026, 8, 30, 3, 0, 0, 0, time.UTC))
	first := eligibleVideos(values, 3, seed)
	second := eligibleVideos(append([]content.PublicItem(nil), values...), 3, seed)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%#v second=%#v", first, second)
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
	trusted(request, "cms:news:publish cms:pages:publish")
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

func TestChildPublicationOperationsFailClosed(t *testing.T) {
	for _, module := range []string{"history", "videos"} {
		for _, path := range []string{
			"/api/admin/content/" + module + "/item-1/publish",
			"/api/admin/content/" + module + "/item-1/unpublish",
			"/api/admin/content/" + module + "/item-1/revisions/1/restore",
		} {
			request := httptest.NewRequest(http.MethodPost, path, nil)
			trusted(request, "cms:news:publish cms:pages:publish cms:news:write cms:pages:write")
			request.Header.Set("If-Match", `"1"`)
			response := httptest.NewRecorder()
			contentTestHandler(&contentRepository{}).ServeHTTP(response, request)
			if response.Code != http.StatusMethodNotAllowed || !strings.Contains(response.Body.String(), `"code":"method_not_allowed"`) {
				t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
			}
		}
	}
}

func TestAboutPagePublishKeepsPageEnvelope(t *testing.T) {
	payload := json.RawMessage(`{"schemaVersion":1,"template":"about.v1","data":{"heroTitle":"About","heroSubtitle":"Subtitle","vision":{"intro":"Vision","imageAlt":"Image","actionsImageAlt":"Actions","sections":[{"eyebrow":"One","title":"Vision","body":"Body"},{"eyebrow":"Two","title":"Goals","body":"Body"},{"eyebrow":"Three","title":"Actions","cards":[{"title":"Share","body":"Body"}]},{"eyebrow":"Four","title":"Commitments","cards":[{"title":"Mission","body":"Body"}]}]},"history":{"scripture":[{"lines":["Scripture"],"cite":"Isaiah"}],"imageAlt":"History","intro":"History intro","title":"History"}}}`)
	translations := make([]content.Translation, 0, 5)
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		translations = append(translations, content.Translation{Locale: locale, Title: "About", Summary: "Subtitle", BodyJSON: payload})
	}
	repo := &contentRepository{item: content.Item{ID: "page-about", Module: content.ModulePages, Status: content.StatusDraft, Version: 2, PageKey: "about", PageTemplate: "about.v1", RoutePath: "/about", Indexable: true, Translations: translations}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/pages/page-about/publish", nil)
	request.Body = io.NopCloser(strings.NewReader(""))
	request.ContentLength = -1 // Dapr may forward an empty stream instead of http.NoBody.
	trusted(request, "cms:news:publish cms:pages:publish")
	request.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()
	contentTestHandler(repo).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"module":"pages"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
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
			trusted(request, "cms:news:publish cms:pages:publish")
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
	repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Version: 1, Slug: "news", DisplayDate: "2026-08-02", Translations: []content.Translation{{Locale: "zh-Hant", Title: "消息", Body: "內容"}}}}
	uploads := &apiUploads{completed: assetclient.Asset{
		ID: "asset-1", Namespace: "cms.news.cover", OwnerService: "hhc-web-api",
		OwnerType: "news", OwnerID: "another-news",
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/assets/asset-1/complete", bytes.NewBufferString(`{"fileName":"cover.jpg","mimeType":"image/jpeg","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:news:write cms:pages:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || uploads.completeCalls != 0 {
		t.Fatalf("status=%d completeCalls=%d", response.Code, uploads.completeCalls)
	}
}

func TestNewsCoverUploadDefaultsToDetailAndAcceptsHomeUsage(t *testing.T) {
	uploads := &apiUploads{}
	handler := contentTestHandlerWithAssets(&contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews}}, uploads)

	for _, test := range []struct {
		body    string
		purpose string
	}{
		{body: `{"fileName":"detail.jpg","mimeType":"image/jpeg","sizeBytes":128}`, purpose: "news_detail_cover"},
		{body: `{"usage":"home","fileName":"home.jpg","mimeType":"image/jpeg","sizeBytes":128}`, purpose: "news_home_cover"},
	} {
		request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/upload-sessions", bytes.NewBufferString(test.body))
		trusted(request, "cms:news:write cms:pages:write")
		request.Header.Set("Idempotency-Key", test.purpose)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusCreated || uploads.createdPurpose != test.purpose {
			t.Fatalf("status=%d purpose=%q body=%s", response.Code, uploads.createdPurpose, response.Body.String())
		}
	}
}

func TestStatementInlineUploadRequiresStatementAndDoesNotReplaceCover(t *testing.T) {
	for _, test := range []struct {
		kind string
		want int
	}{
		{kind: content.KindStatement, want: http.StatusCreated},
		{kind: content.KindGeneral, want: http.StatusBadRequest},
	} {
		uploads := &apiUploads{}
		repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Kind: test.kind, Version: 2}}
		request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/upload-sessions", bytes.NewBufferString(`{"usage":"inline","fileName":"inside.jpg","mimeType":"image/jpeg","sizeBytes":128}`))
		trusted(request, "cms:news:write")
		request.Header.Set("Idempotency-Key", "inline-1")
		response := httptest.NewRecorder()
		contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("kind=%q status=%d body=%s", test.kind, response.Code, response.Body.String())
		}
		if test.want == http.StatusCreated && uploads.createdPurpose != "statement_inline" {
			t.Fatalf("purpose=%q", uploads.createdPurpose)
		}
	}

	repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Kind: content.KindStatement, Version: 2}}
	uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-inline", Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: "news-1", Purpose: "statement_inline"}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/assets/asset-inline/complete", bytes.NewBufferString(`{"fileName":"inside.jpg","mimeType":"image/jpeg","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:news:write")
	request.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusOK || repo.item.Version != 2 || repo.item.CoverAssetID != "" || repo.item.HomeCoverAssetID != "" || uploads.serviceGrantCalls != 1 {
		t.Fatalf("status=%d item=%#v body=%s", response.Code, repo.item, response.Body.String())
	}
}

func TestStatementUpdateRejectsForeignInlineAsset(t *testing.T) {
	repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Kind: content.KindStatement, Version: 2, Translations: []content.Translation{{Locale: "zh-Hant", Title: "聲明"}}}}
	uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-inline", Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: "other-news", Purpose: "statement_inline"}}
	body := `{"kind":"statement","translations":[{"locale":"zh-Hant","title":"聲明","bodyJson":{"schemaVersion":1,"blocks":[{"id":"photo","type":"image","assetId":"asset-inline","size":"full","alignment":"center","alt":{"mode":"text","text":"照片"}}]}}]}`
	request := httptest.NewRequest(http.MethodPut, "/api/admin/content/news/news-1", bytes.NewBufferString(body))
	trusted(request, "cms:news:write")
	request.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || repo.updateInput.Translations != nil {
		t.Fatalf("status=%d update=%#v body=%s", response.Code, repo.updateInput, response.Body.String())
	}
}

func TestStatementInlinePreviewIsPrivateAndOwnerScoped(t *testing.T) {
	repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Kind: content.KindStatement}}
	uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-inline", Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: "news-1", Purpose: "statement_inline", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready"}}
	downloads := &fullBulletinDownloadStub{}
	handler := NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), content.NewService(repo, time.Now), nil, uploads, "api-gateway", "", false).WithAssetDownloads(downloads).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/news/news-1/assets/asset-inline/preview", nil)
	trusted(request, "cms:news:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" || downloads.calls != 1 || !strings.Contains(response.Body.String(), "%PDF-original") {
		t.Fatalf("status=%d cache=%q calls=%d body=%s", response.Code, response.Header().Get("Cache-Control"), downloads.calls, response.Body.String())
	}
}

type missingGrantDownloadStub struct{ calls int }

func (s *missingGrantDownloadStub) AuthorizedDownload(context.Context, string, string, string, string) (*http.Response, error) {
	s.calls++
	if s.calls == 1 {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("image"))}, nil
}

func TestStatementInlinePreviewRepairsMissingServiceGrant(t *testing.T) {
	repo := &contentRepository{item: content.Item{ID: "news-1", Module: content.ModuleNews, Kind: content.KindStatement}}
	uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-inline", Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: "news-1", Purpose: "statement_inline", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready"}}
	downloads := &missingGrantDownloadStub{}
	handler := NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), content.NewService(repo, time.Now), nil, uploads, "api-gateway", "", false).WithAssetDownloads(downloads).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/news/news-1/assets/asset-inline/preview", nil)
	trusted(request, "cms:news:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "image" || uploads.serviceGrantCalls != 1 || downloads.calls != 2 {
		t.Fatalf("status=%d body=%q grant calls=%d download calls=%d", response.Code, response.Body.String(), uploads.serviceGrantCalls, downloads.calls)
	}
}

func TestCompleteNewsCoverUsesAssetPurpose(t *testing.T) {
	repo := &contentRepository{item: content.Item{
		ID: "news-1", Module: content.ModuleNews, Version: 1, Slug: "news",
		DisplayDate:  "2026-08-02",
		Translations: []content.Translation{{Locale: "zh-Hant", Title: "News", Body: "Body"}},
	}}
	uploads := &apiUploads{completed: assetclient.Asset{
		ID: "asset-home", Namespace: "cms.news.cover", OwnerService: "hhc-web-api", OwnerType: "news", OwnerID: "news-1", Purpose: "news_home_cover",
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/assets/asset-home/complete", bytes.NewBufferString(`{"fileName":"home.jpg","mimeType":"image/jpeg","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:news:write cms:pages:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)

	if response.Code != http.StatusOK || repo.item.HomeCoverAssetID != "asset-home" || repo.item.CoverAssetID != "" {
		t.Fatalf("status=%d item=%#v body=%s", response.Code, repo.item, response.Body.String())
	}
}

func TestCompleteNewsCoverReportsAssetServiceUnavailable(t *testing.T) {
	uploads := &apiUploads{getError: assetclient.ErrUnavailable}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/assets/asset-1/complete", bytes.NewBufferString(`{"fileName":"cover.jpg","mimeType":"image/jpeg","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:news:write cms:pages:write")
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
	trusted(request, "cms:news:read cms:pages:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "ownerService") || !strings.Contains(response.Body.String(), `"retryable":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/assets/asset-1/scan/retry", nil)
	trusted(request, "cms:news:write cms:pages:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || uploads.requeueCalls != 1 || !strings.Contains(response.Body.String(), `"scanStatus":"pending"`) {
		t.Fatalf("status=%d requeueCalls=%d body=%s", response.Code, uploads.requeueCalls, response.Body.String())
	}
}

func TestHomeBannerUploadUsesExactContractForFixedHomeV2(t *testing.T) {
	repo := &contentRepository{item: homeV2HTTPItem()}
	uploads := &apiUploads{}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/pages/page-home/upload-sessions", bytes.NewBufferString(`{"usage":"home_banner","fileName":"banner.jpg","mimeType":"image/jpeg","sizeBytes":128}`))
	trusted(request, "cms:news:write cms:pages:write")
	request.Header.Set("Idempotency-Key", "home-banner-1")
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusCreated || uploads.createdHomeID != "page-home" {
		t.Fatalf("status=%d homeID=%q body=%s", response.Code, uploads.createdHomeID, response.Body.String())
	}

	for _, body := range []string{
		`{"usage":"detail","fileName":"banner.jpg","mimeType":"image/jpeg","sizeBytes":128}`,
		`{"usage":"home_banner","fileName":"banner.png","mimeType":"image/png","sizeBytes":128}`,
	} {
		request = httptest.NewRequest(http.MethodPost, "/api/admin/content/pages/page-home/upload-sessions", bytes.NewBufferString(body))
		trusted(request, "cms:news:write cms:pages:write")
		request.Header.Set("Idempotency-Key", "invalid")
		response = httptest.NewRecorder()
		contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestHomeBannerCompleteRechecksIdentityAndAttachesOnlyReadyContract(t *testing.T) {
	repo := &contentRepository{item: homeV2HTTPItem()}
	uploads := &apiUploads{completed: readyHomeBannerAsset()}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/pages/page-home/assets/banner-1/complete", bytes.NewBufferString(`{"mimeType":"image/jpeg","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:news:write cms:pages:write")
	request.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()
	contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusOK || repo.updateInput.BannerAssetID != "banner-1" || uploads.completeCalls != 1 {
		t.Fatalf("status=%d input=%#v completeCalls=%d body=%s", response.Code, repo.updateInput, uploads.completeCalls, response.Body.String())
	}

	for _, mutate := range []func(*assetclient.Asset){
		func(asset *assetclient.Asset) { asset.Namespace = "cms.news.cover" },
		func(asset *assetclient.Asset) { asset.Purpose = "news_home_cover" },
		func(asset *assetclient.Asset) { asset.ExpectedMIMEType = "image/png" },
		func(asset *assetclient.Asset) { asset.DetectedMIMEType = "image/png" },
		func(asset *assetclient.Asset) { asset.ProcessingStatus = "ready" },
	} {
		asset := readyHomeBannerAsset()
		mutate(&asset)
		uploads = &apiUploads{completed: asset}
		repo = &contentRepository{item: homeV2HTTPItem()}
		request = httptest.NewRequest(http.MethodPost, "/api/admin/content/pages/page-home/assets/banner-1/complete", bytes.NewBufferString(`{"mimeType":"image/jpeg","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
		trusted(request, "cms:news:write cms:pages:write")
		request.Header.Set("If-Match", `"2"`)
		response = httptest.NewRecorder()
		contentTestHandlerWithAssets(repo, uploads).ServeHTTP(response, request)
		if response.Code == http.StatusOK || repo.updateInput.BannerAssetID != "" {
			t.Fatalf("asset=%#v status=%d input=%#v", asset, response.Code, repo.updateInput)
		}
	}
}

func TestHomeBannerStatusAndRetryRequireExactOwnedFailedAsset(t *testing.T) {
	asset := readyHomeBannerAsset()
	asset.ScanStatus = "failed"
	uploads := &apiUploads{completed: asset}
	handler := contentTestHandlerWithAssets(&contentRepository{item: homeV2HTTPItem()}, uploads)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/pages/page-home/assets/banner-1", nil)
	trusted(request, "cms:news:read cms:pages:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"retryable":true`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/admin/content/pages/page-home/assets/banner-1/scan/retry", nil)
	trusted(request, "cms:news:write cms:pages:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || uploads.requeueCalls != 1 {
		t.Fatalf("status=%d requeueCalls=%d body=%s", response.Code, uploads.requeueCalls, response.Body.String())
	}
}

func readyHomeBannerAsset() assetclient.Asset {
	return assetclient.Asset{ID: "banner-1", Namespace: "cms.home.banner", OwnerService: "hhc-web-api", OwnerType: "page", OwnerID: "page-home", Purpose: "home_banner", ExpectedMIMEType: "image/jpeg", UploadStatus: "completed", ScanStatus: "pending", ProcessingStatus: "not_required", DetectedMIMEType: "image/jpeg"}
}

func homeV2HTTPItem() content.Item {
	payload := json.RawMessage(`{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Kingdom joy","aboutDescription":"About us"}}`)
	translations := make([]content.Translation, 0, 5)
	locationTranslations := make([]content.HomeLocationTranslation, 0, 5)
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		translations = append(translations, content.Translation{Locale: locale, Title: "Home", Summary: "Welcome", BodyJSON: payload})
		locationTranslations = append(locationTranslations, content.HomeLocationTranslation{Locale: locale, Name: "Location", Address: "Address"})
	}
	return content.Item{
		ID: "page-home", Module: content.ModulePages, Version: 2, PageKey: "home", PageTemplate: "home.v2", RoutePath: "/", Indexable: true,
		Links:     content.HomeLinks{ChurchYouTube: "https://youtube.com/@hhc", ChurchFacebook: "https://facebook.com/hhc", MusicYouTube: "https://youtube.com/@music"},
		Locations: []content.HomeLocation{{Key: "taipei", MapHref: "https://maps.example.com/taipei", SortOrder: 10, Translations: locationTranslations}}, Translations: translations,
	}
}

func TestNewsUnpublishDoesNotDependOnCurrentDraftCover(t *testing.T) {
	repo := &contentRepository{item: content.Item{
		ID: "news-1", Module: content.ModuleNews, Status: content.StatusDraft, Version: 3,
		Slug: "news", DisplayDate: "2026-07-13", IsPublished: true,
		Translations: []content.Translation{{Locale: "zh-Hant", Title: "消息"}},
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/news-1/unpublish", nil)
	trusted(request, "cms:news:publish cms:pages:publish")
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
	trusted(deleteRequest, "cms:news:write cms:pages:write")
	deleteRequest.Header.Set("If-Match", `"2"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, deleteRequest)
	if response.Code != http.StatusNoContent || !repo.deleted {
		t.Fatalf("status=%d item=%#v body=%s", response.Code, repo.item, response.Body.String())
	}
}

func TestContentListRejectsInvalidFilters(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/admin/content/news?status=unknown", nil)
	trusted(request, "cms:news:read cms:pages:read")
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
	item                  content.Item
	public                []content.PublicItem
	publicTotal           int64
	publicNews            content.PublicItem
	publicETag            string
	publicNewsErr         error
	publicLocations       []content.PublicLocation
	publicLocationsLocale string
	publicLocationsErr    error
	publicEditorialPage   content.PublicEditorialPage
	publicEditorialETag   string
	publicEditorialKey    string
	publicEditorialLocale string
	updateInput           content.WriteInput
	deleted               bool
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

func (r *contentRepository) UpdateContent(_ context.Context, _ content.Module, _ string, _ int64, input content.WriteInput, _ string, _ time.Time) (content.Item, error) {
	r.updateInput = input
	r.item.CoverAssetID = input.CoverAssetID
	r.item.HomeCoverAssetID = input.HomeCoverAssetID
	r.item.BannerAssetID = input.BannerAssetID
	return r.item, nil
}
func (r *contentRepository) RestoreContent(ctx context.Context, module content.Module, id string, expected int64, input content.WriteInput, actor string, now time.Time) (content.Item, error) {
	return r.UpdateContent(ctx, module, id, expected, input, actor, now)
}

func (r *contentRepository) RestorePageGroup(context.Context, string, int64, int64, string, time.Time) (content.Item, error) {
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
func (r *contentRepository) ContentRevision(context.Context, content.Module, string, int64) (content.Revision, error) {
	return content.Revision{}, content.ErrNotFound
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
func (r *contentRepository) PublicLocations(_ context.Context, locale string) ([]content.PublicLocation, error) {
	r.publicLocationsLocale = locale
	return r.publicLocations, r.publicLocationsErr
}
func (r *contentRepository) PublicEditorialPage(_ context.Context, key, locale string) (content.PublicEditorialPage, string, error) {
	r.publicEditorialKey, r.publicEditorialLocale = key, locale
	return r.publicEditorialPage, r.publicEditorialETag, nil
}

func (r *contentRepository) ActiveStatement(context.Context, string, time.Time) (content.ActiveStatement, error) {
	return content.ActiveStatement{}, nil
}

func TestStatementWriteWindowPresence(t *testing.T) {
	for _, tc := range []struct {
		raw string
		set bool
	}{{`{}`, false}, {`{"popupStartsAt":null,"popupEndsAt":null}`, true}} {
		var request contentWriteRequest
		if err := json.Unmarshal([]byte(tc.raw), &request); err != nil {
			t.Fatal(err)
		}
		if request.PopupWindowSet != tc.set {
			t.Fatal("HTTP DTO lost explicit window clear")
		}
	}
}

func TestStatementPublishWithoutCover(t *testing.T) {
	repo := &contentRepository{item: content.Item{ID: "statement-1", Module: content.ModuleNews, Kind: content.KindStatement, Version: 1, Slug: "statement-1", DisplayDate: "2026-09-07", Translations: []content.Translation{{Locale: "zh-Hant", Title: "聲明", Body: "原文"}}}}
	r := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/statement-1/publish", nil)
	trusted(r, "cms:news:publish cms:pages:publish")
	r.Header.Set("If-Match", `"1"`)
	w := httptest.NewRecorder()
	contentTestHandler(repo).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func (r *contentRepository) EndStatementPopup(context.Context, string, int64, string, time.Time) (content.Item, error) {
	return content.Item{}, nil
}

func (r *contentRepository) PublishStatement(context.Context, string, int64, string, content.StatementNotificationRequest, time.Time) (content.Item, error) {
	return content.Item{}, nil
}
func (r *contentRepository) RequestStatementNotifications(context.Context, string, int64, int64, string, content.StatementNotificationRequest, time.Time) ([]content.StatementNotificationBatch, error) {
	return nil, nil
}
func (r *contentRepository) StatementNotifications(context.Context, string) ([]content.StatementNotificationBatch, error) {
	return nil, nil
}

func TestStatementNotificationResultRoutesAreRemoved(t *testing.T) {
	handler := contentTestHandler(&contentRepository{})
	for _, path := range []string{"/api/admin/content/news/statement-1/statement-notifications", "/api/admin/content/news/statement-1/statement-notifications/preview"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		trusted(request, "cms:news:read cms:pages:read cms:news:publish cms:pages:publish")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestPublishOptionalJSONBody(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"empty stream", "", http.StatusOK},
		{"empty options", "{}", http.StatusOK},
		{"malformed", "{", http.StatusBadRequest},
		{"unknown field", `{"unexpected":true}`, http.StatusBadRequest},
		{"trailing JSON", "{}{}", http.StatusBadRequest},
		{"trailing garbage", "{}invalid", http.StatusBadRequest},
		{"oversize whitespace", strings.Repeat(" ", (1<<20)+1), http.StatusBadRequest},
		{"oversize trailing whitespace", "{}" + strings.Repeat(" ", 1<<20), http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &contentRepository{item: content.Item{ID: "statement-1", Module: content.ModuleNews, Kind: content.KindStatement, Status: content.StatusDraft, Version: 1, Slug: "statement-1", DisplayDate: "2026-09-07", Translations: []content.Translation{{Locale: "zh-Hant", Title: "聲明", Body: "原文"}}}}
			request := httptest.NewRequest(http.MethodPost, "/api/admin/content/news/statement-1/publish", nil)
			request.Body = io.NopCloser(strings.NewReader(tc.body))
			request.ContentLength = -1
			trusted(request, "cms:news:publish cms:pages:publish")
			request.Header.Set("If-Match", `"1"`)
			response := httptest.NewRecorder()
			contentTestHandler(repo).ServeHTTP(response, request)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status != http.StatusOK && repo.item.Status != content.StatusDraft {
				t.Fatal("invalid body mutated publication")
			}
		})
	}
}

func (r *contentRepository) GetStatementDismissal(context.Context, string, content.StatementRef) (content.StatementDismissal, error) {
	return content.StatementDismissal{}, nil
}

func (r *contentRepository) DismissStatement(context.Context, string, content.StatementRef) (content.StatementDismissal, error) {
	return content.StatementDismissal{}, nil
}
