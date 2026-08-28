package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

func TestPublicSiteLayoutReadsOnlyExactPublishedLocale(t *testing.T) {
	repo := &siteSettingsRepository{public: sitesettings.PublicLayout{Locale: "ja", SiteName: "ハレルヤ家庭教会", Version: 4}}
	response := httptest.NewRecorder()
	siteSettingsTestHandler(repo).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/site-layout?locale=ja", nil))

	if response.Code != http.StatusOK || repo.publicLocale != "ja" || response.Header().Get("Cache-Control") == "" || response.Header().Get("ETag") != `"site-layout-4"` {
		t.Fatalf("status=%d locale=%q cache=%q etag=%q body=%s", response.Code, repo.publicLocale, response.Header().Get("Cache-Control"), response.Header().Get("ETag"), response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"siteName":"ハレルヤ家庭教会"`) || strings.Contains(response.Body.String(), `"status"`) {
		t.Fatalf("body=%s", response.Body.String())
	}

	repo.publicErr = sitesettings.ErrNotFound
	response = httptest.NewRecorder()
	siteSettingsTestHandler(repo).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/site-layout?locale=ko", nil))
	if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"not_found"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPublicSiteLayoutHonorsIfNoneMatch(t *testing.T) {
	repo := &siteSettingsRepository{public: sitesettings.PublicLayout{Locale: "ja", Version: 4}}
	request := httptest.NewRequest(http.MethodGet, "/api/site-layout?locale=ja", nil)
	request.Header.Set("If-None-Match", `"other", W/"site-layout-4"`)
	response := httptest.NewRecorder()
	siteSettingsTestHandler(repo).ServeHTTP(response, request)

	if response.Code != http.StatusNotModified || response.Header().Get("ETag") != `"site-layout-4"` || response.Body.Len() != 0 {
		t.Fatalf("status=%d etag=%q body=%q", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
}

func TestSiteSettingsAdminRoutesSeparateScopesAndNeverCache(t *testing.T) {
	tests := []struct {
		method string
		path   string
		body   string
		scope  string
	}{
		{http.MethodGet, "/api/admin/site-settings", "", "cms:read"},
		{http.MethodPut, "/api/admin/site-settings", siteSettingsWriteJSON(), "cms:write"},
		{http.MethodPost, "/api/admin/site-settings/publish", "", "cms:publish"},
		{http.MethodPost, "/api/admin/site-settings/unpublish", "", "cms:publish"},
		{http.MethodGet, "/api/admin/site-settings/revisions", "", "cms:read"},
		{http.MethodPost, "/api/admin/site-settings/revisions/1/restore", "", "cms:write"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			repo := &siteSettingsRepository{settings: validSiteSettings()}
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			trusted(request, test.scope)
			if test.method == http.MethodPut || test.method == http.MethodPost {
				request.Header.Set("If-Match", `"3"`)
			}
			response := httptest.NewRecorder()
			siteSettingsTestHandler(repo).ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
			}

			request = httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			trusted(request, "cms:unrelated")
			response = httptest.NewRecorder()
			siteSettingsTestHandler(repo).ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("forbidden status=%d cache=%q", response.Code, response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestSiteSettingsAdminUnauthorizedResponseIsNotCacheable(t *testing.T) {
	response := httptest.NewRecorder()
	siteSettingsTestHandler(&siteSettingsRepository{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/admin/site-settings", nil))
	if response.Code != http.StatusUnauthorized || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d cache=%q", response.Code, response.Header().Get("Cache-Control"))
	}
}

func TestSiteSettingsWritesEnforceConcurrencyAndValidation(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		body       string
		ifMatch    string
		repository *siteSettingsRepository
		want       int
		code       string
	}{
		{"missing If-Match", "/api/admin/site-settings", siteSettingsWriteJSON(), "", &siteSettingsRepository{settings: validSiteSettings()}, http.StatusPreconditionRequired, "precondition_required"},
		{"stale version", "/api/admin/site-settings", siteSettingsWriteJSON(), `"3"`, &siteSettingsRepository{settings: validSiteSettings(), saveErr: sitesettings.ErrPrecondition}, http.StatusConflict, "version_conflict"},
		{"invalid links", "/api/admin/site-settings", strings.Replace(siteSettingsWriteJSON(), "https://youtube.com/@hhc33", "http://127.0.0.1", 1), `"3"`, &siteSettingsRepository{settings: validSiteSettings()}, http.StatusUnprocessableEntity, "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPut, test.path, bytes.NewBufferString(test.body))
			trusted(request, "cms:write")
			if test.ifMatch != "" {
				request.Header.Set("If-Match", test.ifMatch)
			}
			response := httptest.NewRecorder()
			siteSettingsTestHandler(test.repository).ServeHTTP(response, request)
			if response.Code != test.want || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestSiteSettingsRestoreDoesNotChangePublicProjection(t *testing.T) {
	repo := &siteSettingsRepository{settings: validSiteSettings(), public: sitesettings.PublicLayout{Locale: "zh-Hant", SiteName: "published", Version: 2}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/site-settings/revisions/1/restore", nil)
	request.Header.Set("If-Match", `"3"`)
	trusted(request, "cms:write")
	response := httptest.NewRecorder()
	siteSettingsTestHandler(repo).ServeHTTP(response, request)
	if response.Code != http.StatusOK || repo.public.SiteName != "published" || repo.restoreRevision != 1 {
		t.Fatalf("status=%d public=%q revision=%d body=%s", response.Code, repo.public.SiteName, repo.restoreRevision, response.Body.String())
	}
}

func siteSettingsTestHandler(repo *siteSettingsRepository) http.Handler {
	bulletinService := bulletins.NewService(&apiRepository{}, time.Now)
	return NewWithContent(bulletinService, nil, nil, nil, "api-gateway", "", false).
		WithSiteSettings(sitesettings.NewService(repo, func() time.Time { return time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC) })).Routes()
}

func validSiteSettings() sitesettings.Settings {
	input := siteSettingsWriteInput()
	return sitesettings.Settings{ID: sitesettings.SingletonID, Status: sitesettings.StatusDraft, Version: 3, Locales: input.Locales, Links: input.Links}
}

func siteSettingsWriteJSON() string {
	value, _ := json.Marshal(siteSettingsWriteInput())
	return string(value)
}

func siteSettingsWriteInput() sitesettings.WriteInput {
	locales := make([]sitesettings.LocaleSettings, 0, len(sitesettings.SupportedLocales))
	for _, locale := range sitesettings.SupportedLocales {
		locales = append(locales, sitesettings.LocaleSettings{
			Locale: locale, SiteName: locale + " site", EnglishName: "Hallelujah Home Church", CopyrightHolder: locale + " holder",
			AllRightsReserved: locale + " rights", SEOTitleSuffix: locale + " title", SEODescriptionFallback: locale + " description",
			Header: []sitesettings.NavItem{{Key: "about", Label: "About", Href: "/{locale}/about", Visible: true}, {Key: "news", Label: "News", Href: "/{locale}/news", Visible: true}, {Key: "literature-ministry", Label: "Literature", Href: "/{locale}/literature-ministry", Visible: true}},
			Legal:  []sitesettings.NavItem{{Key: "privacy-policy", Label: "Privacy", Href: "/{locale}/privacy-policy", Visible: true}, {Key: "terms-of-use", Label: "Terms", Href: "/{locale}/terms-of-use", Visible: true}},
		})
	}
	return sitesettings.WriteInput{Locales: locales, Links: sitesettings.ExternalLinks{ChurchYouTube: "https://youtube.com/@hhc33", ChurchFacebook: "https://facebook.com/hhc", MusicYouTube: "https://youtube.com/@gkpmusic777"}}
}

type siteSettingsRepository struct {
	settings        sitesettings.Settings
	public          sitesettings.PublicLayout
	publicLocale    string
	publicErr       error
	saveErr         error
	restoreRevision int64
}

func (r *siteSettingsRepository) Get(context.Context) (sitesettings.Settings, error) {
	return r.settings, nil
}
func (r *siteSettingsRepository) Public(_ context.Context, locale string) (sitesettings.PublicLayout, error) {
	r.publicLocale = locale
	return r.public, r.publicErr
}
func (r *siteSettingsRepository) Save(_ context.Context, input sitesettings.WriteInput, expected int64, _ string, _ time.Time) (sitesettings.Settings, error) {
	if r.saveErr != nil {
		return sitesettings.Settings{}, r.saveErr
	}
	if r.settings.Version != expected {
		return sitesettings.Settings{}, sitesettings.ErrPrecondition
	}
	r.settings.Locales, r.settings.Links, r.settings.Version = input.Locales, input.Links, expected+1
	return r.settings, nil
}
func (r *siteSettingsRepository) Publish(_ context.Context, expected int64, _ string, _ time.Time) (sitesettings.Settings, error) {
	if r.settings.Version != expected {
		return sitesettings.Settings{}, sitesettings.ErrPrecondition
	}
	r.settings.Version, r.settings.Status = expected+1, sitesettings.StatusPublished
	return r.settings, nil
}
func (r *siteSettingsRepository) Unpublish(_ context.Context, expected int64, _ string, _ time.Time) (sitesettings.Settings, error) {
	if r.settings.Version != expected {
		return sitesettings.Settings{}, sitesettings.ErrPrecondition
	}
	r.settings.Version, r.settings.Status = expected+1, sitesettings.StatusUnpublished
	return r.settings, nil
}
func (r *siteSettingsRepository) Revisions(context.Context) ([]sitesettings.Revision, error) {
	return []sitesettings.Revision{{Revision: 1, Snapshot: r.settings}}, nil
}
func (r *siteSettingsRepository) Restore(_ context.Context, revision, expected int64, _ string, _ time.Time) (sitesettings.Settings, error) {
	if revision < 1 {
		return sitesettings.Settings{}, sitesettings.ErrInvalid
	}
	if r.settings.Version != expected {
		return sitesettings.Settings{}, sitesettings.ErrPrecondition
	}
	r.restoreRevision, r.settings.Version, r.settings.Status = revision, expected+1, sitesettings.StatusDraft
	return r.settings, nil
}

var _ sitesettings.Repository = (*siteSettingsRepository)(nil)
