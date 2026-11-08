package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
)

type protectedAPIRepository struct {
	*apiRepository
	entitlementReads int
	dataReads        int
	item             bulletins.ProtectedBulletin
	issue            bulletins.Issue
}

func (r *protectedAPIRepository) GetIssue(context.Context, string) (bulletins.Issue, error) {
	if r.issue.ID == "" {
		return bulletins.Issue{}, bulletins.ErrNotFound
	}
	return r.issue, nil
}

func (r *protectedAPIRepository) BulletinEntitlement(_ context.Context, series, locale string) (string, error) {
	r.entitlementReads++
	codes := map[string]string{
		"zh-Hant": "bulletin.general.zh-Hant.access",
		"zh-Hans": "bulletin.general.zh-Hans.access",
		"en":      "bulletin.general.en.access",
	}
	code, ok := codes[locale]
	if series != "general" || !ok {
		return "", bulletins.ErrNotFound
	}
	return code, nil
}
func (r *protectedAPIRepository) ListProtected(context.Context, string, string, int, int, *int) (bulletins.ProtectedPage, error) {
	r.dataReads++
	return bulletins.ProtectedPage{Items: []bulletins.ProtectedBulletin{}, Page: 1, PageSize: 20}, nil
}
func (r *protectedAPIRepository) GetProtectedLatest(context.Context, string, string) (bulletins.ProtectedBulletin, error) {
	r.dataReads++
	if r.item.IssueID != "" {
		return r.item, nil
	}
	return bulletins.ProtectedBulletin{}, bulletins.ErrNotFound
}
func (r *protectedAPIRepository) GetProtectedVersion(context.Context, string, string, string) (bulletins.ProtectedBulletin, error) {
	r.dataReads++
	if r.item.IssueID != "" {
		return r.item, nil
	}
	return bulletins.ProtectedBulletin{}, bulletins.ErrNotFound
}

func TestProtectedBulletinDownloadAuthorizesBeforeAssetAndRange(t *testing.T) {
	item := bulletins.ProtectedBulletin{IssueID: "issue-1", Series: "general", Locale: "zh-Hant", ProtectedAssetID: "asset-1", DownloadName: "週報.pdf", Version: 7}
	for _, test := range []struct {
		name, decision string
		want           int
		wantCalls      int
	}{
		{"allowed", "allow", http.StatusOK, 1},
		{"denied", "deny", http.StatusNotFound, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &protectedAPIRepository{apiRepository: &apiRepository{}, item: item}
			uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready", Visibility: "private"}}
			downloads := &fullBulletinDownloadStub{}
			handler := NewWithContent(bulletins.NewService(repository, nil), nil, nil, uploads, "api-gateway", "", true).
				WithBulletinEntitlements(entitlementCheckerStub{decision: test.decision}).WithAssetDownloads(downloads).Routes()
			request := httptest.NewRequest(http.MethodGet, "/api/member/bulletins/issue-1/versions/zh-Hant/download?series=general", nil)
			request.Header.Set("Range", "bytes=10-")
			trusted(request, "cms:bulletins:publish")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want || uploads.getCalls != test.wantCalls || downloads.calls != test.wantCalls {
				t.Fatalf("status=%d assetCalls=%d downloadCalls=%d body=%s", response.Code, uploads.getCalls, downloads.calls, response.Body.String())
			}
			if test.wantCalls == 1 && !downloads.rangeReceived {
				t.Fatal("range headers were not forwarded after authorization")
			}
		})
	}
}

type entitlementCheckerStub struct {
	decision string
	err      error
	codes    *[]string
}

func (s entitlementCheckerStub) CheckEntitlements(_ context.Context, _ string, codes []string) ([]operationsclient.EntitlementDecision, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.codes != nil {
		*s.codes = append([]string(nil), codes...)
	}
	return []operationsclient.EntitlementDecision{{EntitlementCode: codes[0], Decision: s.decision}}, nil
}

func TestProtectedBulletinRoutesRequireExactEntitlementBeforeDataLookup(t *testing.T) {
	for _, test := range []struct {
		name, path, decision string
		checkerErr           error
		want                 int
		wantReads            int
	}{
		{"traditional allowed", "/api/member/bulletins?series=general&locale=zh-Hant", "allow", nil, http.StatusOK, 1},
		{"simplified allowed", "/api/member/bulletins?series=general&locale=zh-Hans", "allow", nil, http.StatusOK, 1},
		{"english allowed", "/api/member/bulletins?series=general&locale=en", "allow", nil, http.StatusOK, 1},
		{"denied", "/api/member/bulletins?series=general&locale=zh-Hant", "deny", nil, http.StatusNotFound, 0},
		{"cross locale denied", "/api/member/bulletins?series=general&locale=zh-Hans", "deny", nil, http.StatusNotFound, 0},
		{"unknown locale", "/api/member/bulletins?series=general&locale=ja", "allow", nil, http.StatusNotFound, 0},
		{"unknown series", "/api/member/bulletins?series=children&locale=zh-Hant", "allow", nil, http.StatusNotFound, 0},
		{"authority unavailable", "/api/member/bulletins?series=general&locale=zh-Hant", "", errors.New("down"), http.StatusServiceUnavailable, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &protectedAPIRepository{apiRepository: &apiRepository{}}
			handler := NewWithContent(bulletins.NewService(repository, nil), nil, nil, nil, "api-gateway", "", true).
				WithBulletinEntitlements(entitlementCheckerStub{decision: test.decision, err: test.checkerErr}).Routes()
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			trusted(request, "*")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want || repository.dataReads != test.wantReads || response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("status=%d reads=%d cache=%q body=%s", response.Code, repository.dataReads, response.Header().Get("Cache-Control"), response.Body.String())
			}
		})
	}
}

func TestProtectedBulletinRoutesTrustOnlyGatewayOrLineBot(t *testing.T) {
	for _, test := range []struct {
		caller string
		want   int
	}{
		{caller: "api-gateway", want: http.StatusOK},
		{caller: "hhc-line-function-bot", want: http.StatusOK},
		{caller: "attacker", want: http.StatusUnauthorized},
		{want: http.StatusUnauthorized},
	} {
		handler := NewWithContent(bulletins.NewService(&protectedAPIRepository{apiRepository: &apiRepository{}}, nil), nil, nil, nil, "api-gateway", "test-token", false).
			WithBulletinEntitlements(entitlementCheckerStub{decision: "allow"}).Routes()
		request := httptest.NewRequest(http.MethodGet, "/api/member/bulletins?series=general&locale=zh-Hant", nil)
		request.Header.Set("Dapr-Caller-App-Id", test.caller)
		request.Header.Set("dapr-api-token", "test-token")
		request.Header.Set("X-HHC-User-ID", "member-1")
		request.Header.Set("X-HHC-Auth-Provider", "account-api")
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		if response.Code != test.want {
			t.Fatalf("caller=%q status=%d want=%d body=%s", test.caller, response.Code, test.want, response.Body.String())
		}
	}
}

func TestProtectedBulletinUsesExactLocaleEntitlement(t *testing.T) {
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en"} {
		t.Run(locale, func(t *testing.T) {
			var codes []string
			repository := &protectedAPIRepository{apiRepository: &apiRepository{}}
			handler := NewWithContent(bulletins.NewService(repository, nil), nil, nil, nil, "api-gateway", "", true).
				WithBulletinEntitlements(entitlementCheckerStub{decision: "allow", codes: &codes}).Routes()
			request := httptest.NewRequest(http.MethodGet, "/api/member/bulletins?series=general&locale="+locale, nil)
			trusted(request, "*")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || len(codes) != 1 || codes[0] != "bulletin.general."+locale+".access" {
				t.Fatalf("status=%d codes=%v body=%s", response.Code, codes, response.Body.String())
			}
		})
	}
}

func TestPublicAndLegacyBulletinRoutesAreAbsent(t *testing.T) {
	handler := New(nil, nil, nil).Routes()
	for _, path := range []string{
		"/api/bulletins", "/api/bulletins/latest", "/api/bulletins/by-number/1737", "/api/bulletin-access",
		"/api/member/bulletin-access", "/api/member/bulletins/by-number/1737",
	} {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		if strings.HasPrefix(path, "/api/member/") {
			trusted(request, "*")
		}
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func TestBulletinNotificationPolicyReturnsPublishedLocaleEntitlements(t *testing.T) {
	repository := &protectedAPIRepository{apiRepository: &apiRepository{}, issue: bulletins.Issue{
		ID: "issue-1", Status: "published", Versions: []bulletins.Version{
			{Locale: "en", Status: "published"},
			{Locale: "zh-Hant", Status: "published"},
			{Locale: "zh-Hans", Status: "unpublished"},
		},
	}}
	handler := NewWithContent(bulletins.NewService(repository, nil), nil, nil, nil, "api-gateway", "", true).Routes()
	request := httptest.NewRequest(http.MethodGet, "/priv/bulletins/issue-1/notification-policy", nil)
	request.Header.Set("X-Internal-Caller-App-Id", "engagement-api")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	var body struct {
		Data struct {
			Published    bool `json:"published"`
			Entitlements []struct {
				Locale          string `json:"locale"`
				EntitlementCode string `json:"entitlementCode"`
			} `json:"entitlements"`
		} `json:"data"`
	}
	raw := response.Body.Bytes()
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !body.Data.Published || len(body.Data.Entitlements) != 2 ||
		body.Data.Entitlements[0].Locale != "zh-Hant" || body.Data.Entitlements[0].EntitlementCode != "bulletin.general.zh-Hant.access" ||
		body.Data.Entitlements[1].Locale != "en" || body.Data.Entitlements[1].EntitlementCode != "bulletin.general.en.access" {
		t.Fatalf("status=%d body=%+v", response.Code, body)
	}
	if strings.Contains(string(raw), "membersOnly") || strings.Contains(string(raw), "enabled") {
		t.Fatal("legacy bulletin policy fields returned")
	}
}
