package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type memberOnlineRepository struct {
	*protectedAPIRepository
	err                   error
	actor, series, locale string
}

func (r *memberOnlineRepository) BulletinEntitlement(_ context.Context, series, locale string) (string, error) {
	r.entitlementReads++
	if _, ok := bulletins.NormalizeBulletinEdition(series, locale); !ok {
		return "", bulletins.ErrNotFound
	}
	return "bulletin." + series + "." + locale + ".access", nil
}
func (r *memberOnlineRepository) DiscoverOnlineBulletins(_ context.Context, series string, locales []string, offset, limit int, issueNumber *int) (onlinebulletins.DiscoveryPage, error) {
	r.dataReads++
	r.series, r.locale = series, strings.Join(locales, ",")
	return onlinebulletins.DiscoveryPage{Items: []onlinebulletins.DiscoveryItem{}, Offset: offset, Limit: limit}, r.err
}
func (r *memberOnlineRepository) OpenOnlineBulletin(_ context.Context, issue, series, locale, actor, privacy string, input onlinebulletins.ReaderAccessInput, now time.Time) (onlinebulletins.ReaderResponse, error) {
	r.dataReads++
	r.actor, r.series, r.locale = actor, series, locale
	return onlinebulletins.ReaderResponse{Document: onlinebulletins.MemberDocument{IssueID: issue, Series: series, ContentLocale: locale}}, r.err
}
func memberOnlineHandler(repo *memberOnlineRepository, decision string, err error) http.Handler {
	h := NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithBulletinEntitlements(entitlementCheckerStub{decision: decision, err: err})
	h.watermarkPrivacyVersion = "privacy-v1"
	return h.Routes()
}
func TestReaderUsesDownloadEntitlement(t *testing.T) {
	for _, edition := range bulletins.BulletinEditionOrder {
		for _, scenario := range []struct {
			name, decision string
			err            error
			status, reads  int
		}{{"allow", "allow", nil, 200, 1}, {"deny", "deny", nil, 404, 0}, {"dependency", "", errors.New("offline"), 503, 0}} {
			t.Run(string(edition.Series)+"/"+string(edition.Locale)+"/"+scenario.name, func(t *testing.T) {
				for _, method := range []string{http.MethodGet, http.MethodPost} {
					repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}}
					path := "/api/member/bulletins/online?series=" + string(edition.Series) + "&locale=" + string(edition.Locale)
					if method == http.MethodPost {
						path = "/api/member/bulletins/b08a80e8-6388-4166-9a2a-23d336f74b7a/versions/" + string(edition.Locale) + "/online/access?series=" + string(edition.Series)
					}
					req := httptest.NewRequest(method, path, strings.NewReader(`{"clientRequestId":"94aff842-b88a-4942-8ecb-2817955fa9de"}`))
					trusted(req, "cms:bulletins:publish")
					req.Header.Set("X-HHC-User-ID", "4a5df71f-6bdc-467c-8879-68ec251fdc26")
					out := httptest.NewRecorder()
					memberOnlineHandler(repo, scenario.decision, scenario.err).ServeHTTP(out, req)
					if out.Code != scenario.status || repo.dataReads != scenario.reads || repo.entitlementReads != 1 {
						t.Fatalf("status %d reads %d entitlement %d", out.Code, repo.dataReads, repo.entitlementReads)
					}
					if out.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal("cacheable member response")
					}
					if (out.Header().Get("X-HHC-Bulletin-Access") == "unavailable") != (scenario.status == 404) {
						t.Fatal("incorrect terminal marker")
					}
					if method == http.MethodPost && out.Code == 200 {
						sum := sha256.Sum256(out.Body.Bytes())
						if out.Header().Get("X-HHC-Content-SHA256") != hex.EncodeToString(sum[:]) {
							t.Fatal("missing byte integrity")
						}
						if repo.actor != "4a5df71f-6bdc-467c-8879-68ec251fdc26" {
							t.Fatal("identity not from gateway")
						}
					}
				}
			})
		}
	}
}
func TestReaderNeverDisclosesContentOnIssuanceFailure(t *testing.T) {
	for _, failure := range []error{errors.New("receipt unavailable"), bulletins.ErrNotFound} {
		repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}, err: failure}
		req := httptest.NewRequest("POST", "/api/member/bulletins/b08a80e8-6388-4166-9a2a-23d336f74b7a/versions/zh-Hant/online/access", strings.NewReader(`{"clientRequestId":"94aff842-b88a-4942-8ecb-2817955fa9de"}`))
		trusted(req, "")
		req.Header.Set("X-HHC-User-ID", "4a5df71f-6bdc-467c-8879-68ec251fdc26")
		out := httptest.NewRecorder()
		memberOnlineHandler(repo, "allow", nil).ServeHTTP(out, req)
		want := 503
		if errors.Is(failure, bulletins.ErrNotFound) {
			want = 404
		}
		if out.Code != want || strings.Contains(out.Body.String(), `"document"`) || out.Header().Get("X-HHC-Content-SHA256") != "" {
			t.Fatal("failed issuance disclosed content")
		}
	}
	repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}}
	req := httptest.NewRequest("GET", "/api/member/bulletins/b08a80e8-6388-4166-9a2a-23d336f74b7a/versions/zh-Hant/online/access", nil)
	trusted(req, "")
	out := httptest.NewRecorder()
	memberOnlineHandler(repo, "allow", nil).ServeHTTP(out, req)
	if out.Code != 405 || repo.dataReads != 0 || out.Header().Get("X-HHC-Bulletin-Access") != "" {
		t.Fatal("full-content GET or unrelated failure marked terminal")
	}
}

func TestReaderRejectsMalformedSelectorsBeforeContentLookup(t *testing.T) {
	for _, test := range []struct{ method, path, body string }{
		{"GET", "/api/member/bulletins/online?offset=-1", ""},
		{"GET", "/api/member/bulletins/online?limit=101", ""},
		{"GET", "/api/member/bulletins/online?offset=01", ""},
		{"GET", "/api/member/bulletins/online?issueNumber=", ""},
		{"POST", "/api/member/bulletins/b08a80e8-6388-4166-9a2a-23d336f74b7a/versions/zh-Hant/online/access", `{"clientRequestId":"invalid"}`},
		{"POST", "/api/member/bulletins/b08a80e8-6388-4166-9a2a-23d336f74b7a/versions/zh-Hant/online/access", `{"clientRequestId":"94aff842-b88a-4942-8ecb-2817955fa9de","accountId":"spoof"}`},
		{"POST", "/api/member/bulletins/b08a80e8-6388-4166-9a2a-23d336f74b7a/versions/zh-Hant/online/access", `{"clientRequestId":"94aff842-b88a-4942-8ecb-2817955fa9de","revision":0}`},
	} {
		repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}}
		req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		trusted(req, "")
		req.Header.Set("X-HHC-User-ID", "4a5df71f-6bdc-467c-8879-68ec251fdc26")
		out := httptest.NewRecorder()
		memberOnlineHandler(repo, "allow", nil).ServeHTTP(out, req)
		if out.Code != 400 || repo.dataReads != 0 || out.Header().Get("Cache-Control") != "private, no-store" || out.Header().Get("X-HHC-Bulletin-Access") != "" {
			t.Fatalf("invalid request reached content: %s %d", test.path, out.Code)
		}
	}
}

func TestReaderDiscoveryChecksEveryRequestedEdition(t *testing.T) {
	for _, test := range []struct {
		query                 string
		status, checks, reads int
	}{
		{"locales=zh-Hant,en", 200, 2, 1},
		{"locales=zh-Hant,en,zh-Hans", 200, 3, 1},
		{"locales=zh-Hant,ja", 404, 2, 0},
		{"locales=zh-Hant,zh-Hant", 400, 0, 0},
		{"locales=en&locale=zh-Hant", 400, 0, 0},
		{"locales=", 400, 0, 0},
	} {
		repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}}
		req := httptest.NewRequest("GET", "/api/member/bulletins/online?"+test.query, nil)
		trusted(req, "")
		out := httptest.NewRecorder()
		memberOnlineHandler(repo, "allow", nil).ServeHTTP(out, req)
		if out.Code != test.status || repo.entitlementReads != test.checks || repo.dataReads != test.reads {
			t.Fatalf("%s: status %d checks %d reads %d", test.query, out.Code, repo.entitlementReads, repo.dataReads)
		}
	}
}
