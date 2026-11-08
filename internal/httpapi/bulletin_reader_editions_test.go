package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
)

type singleEditionGrant string

func (grant singleEditionGrant) CheckEntitlements(_ context.Context, _ string, codes []string) ([]operationsclient.EntitlementDecision, error) {
	decisions := make([]operationsclient.EntitlementDecision, len(codes))
	for i, code := range codes {
		decision := "deny"
		if code == string(grant) {
			decision = "allow"
		}
		decisions[i] = operationsclient.EntitlementDecision{EntitlementCode: code, Decision: decision}
	}
	return decisions, nil
}

// A grant for one language/series must not unlock discovery, reading, or private
// annotations in other editions, regardless of UI language. Children/Hans is
// intentionally unsupported, even if an upstream grant accidentally names it.
func TestReaderEditionGrantsAreNotInterchangeable(t *testing.T) {
	editions := []struct{ series, locale string }{
		{"general", "zh-Hant"}, {"general", "zh-Hans"}, {"general", "en"},
		{"children", "zh-Hant"}, {"children", "zh-Hans"}, {"children", "en"},
	}
	for _, granted := range editions {
		for _, requested := range editions {
			base := "/api/member/bulletins/b08a80e8-6388-4166-9a2a-23d336f74b7a/versions/" + requested.locale + "/online"
			for _, endpoint := range []struct{ name, method, path, body string }{
				{"discovery", "GET", "/api/member/bulletins/online?locale=" + requested.locale + "&series=" + requested.series, ""},
				{"access", "POST", base + "/access?series=" + requested.series, `{"clientRequestId":"94aff842-b88a-4942-8ecb-2817955fa9de"}`},
				{"state", "GET", base + "/reader/state?series=" + requested.series, ""},
				{"mutations", "POST", base + "/reader/mutations?series=" + requested.series, privateReaderBody},
			} {
				t.Run(granted.series+"/"+granted.locale+"/"+requested.series+"/"+requested.locale+"/"+endpoint.name, func(t *testing.T) {
					repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}}
					h := NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).
						WithBulletinEntitlements(singleEditionGrant("bulletin." + granted.series + "." + granted.locale + ".access"))
					h.watermarkPrivacyVersion = "privacy-v1"
					req := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(endpoint.body))
					trusted(req, "cms:bulletins:publish")
					req.Header.Set("X-HHC-User-ID", "4a5df71f-6bdc-467c-8879-68ec251fdc26")
					req.Header.Set("Accept-Language", "ja,zh-Hant;q=0.9")
					out := httptest.NewRecorder()
					h.Routes().ServeHTTP(out, req)
					status, reads := 404, 0
					if granted == requested && !(requested.series == "children" && requested.locale == "zh-Hans") {
						status, reads = 200, 1
					}
					if out.Code != status || repo.dataReads != reads || repo.entitlementReads != 1 {
						t.Fatalf("status=%d dataReads=%d entitlementReads=%d", out.Code, repo.dataReads, repo.entitlementReads)
					}
					if out.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal("member response is cacheable")
					}
					if reads == 1 && (repo.series != requested.series || repo.locale != requested.locale) {
						t.Fatalf("lookup selected another edition: %s/%s", repo.series, repo.locale)
					}
				})
			}
		}
	}
}

func TestMixedDiscoveryCannotSmuggleAnUnentitledLocale(t *testing.T) {
	for _, locales := range []string{"zh-Hans,zh-Hant", "zh-Hant,zh-Hans", "zh-Hans,en", "en,zh-Hans"} {
		t.Run(locales, func(t *testing.T) {
			repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}}
			h := NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).
				WithBulletinEntitlements(singleEditionGrant("bulletin.general.zh-Hans.access")).Routes()
			req := httptest.NewRequest("GET", "/api/member/bulletins/online?series=general&locales="+locales, nil)
			trusted(req, "")
			out := httptest.NewRecorder()
			h.ServeHTTP(out, req)
			if out.Code != 404 || repo.dataReads != 0 || strings.Contains(out.Body.String(), `"items"`) {
				t.Fatalf("mixed discovery leaked content: status=%d reads=%d", out.Code, repo.dataReads)
			}
		})
	}
}
