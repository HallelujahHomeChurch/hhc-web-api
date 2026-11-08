package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
)

const lineAccessSubject = "01995fd2-6f6c-7b40-8707-6503edc362c8"

type lineEntitlementsFunc func(context.Context, string, []string) ([]operationsclient.EntitlementDecision, error)

func (f lineEntitlementsFunc) CheckEntitlements(ctx context.Context, subject string, codes []string) ([]operationsclient.EntitlementDecision, error) {
	return f(ctx, subject, codes)
}

func newLineAccessHandler() (*Handler, *memberOnlineRepository) {
	repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}}
	return NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "test-sidecar-token", false).WithLegal(nil, qualificationStub{allowed: true}).WithBulletinEntitlements(singleEditionGrant("bulletin.children.en.access")), repo
}
func lineAccessRequest(path string) *http.Request {
	request := httptest.NewRequest("GET", path, nil)
	request.Header.Set("Dapr-Caller-App-Id", "hhc-line-function-bot")
	request.Header.Set("dapr-api-token", "test-sidecar-token")
	request.Header.Set("X-HHC-User-ID", lineAccessSubject)
	request.Header.Set("X-HHC-Auth-Provider", "account-api")
	return request
}
func TestLineAccessRejectsSpoofedIdentityAndNonBotCallers(t *testing.T) {
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) {
			r.Header.Del("Dapr-Caller-App-Id")
			r.Header.Set("X-Internal-Caller-App-Id", "hhc-line-function-bot")
		},
		func(r *http.Request) { r.Header.Set("Dapr-Caller-App-Id", "api-gateway") },
		func(r *http.Request) { r.Header.Del("dapr-api-token") },
		func(r *http.Request) { r.Header.Set("X-HHC-Auth-Provider", "browser") },
		func(r *http.Request) { r.Header.Set("X-HHC-User-ID", "invalid") },
	} {
		h, repo := newLineAccessHandler()
		request := lineAccessRequest("/priv/line/member-access")
		mutate(request)
		response := httptest.NewRecorder()
		h.Routes().ServeHTTP(response, request)
		if response.Code != 401 || repo.entitlementReads != 0 {
			t.Fatalf("status=%d reads=%d body=%s", response.Code, repo.entitlementReads, response.Body.String())
		}
	}
}

func TestLineAccessRejectsCallerSelectedIdentityAndInvalidLocale(t *testing.T) {
	for _, query := range []string{"subjectId=" + lineAccessSubject, "locale=zh-Hant&locale=en", "locale=fr", "locale="} {
		h, repo := newLineAccessHandler()
		response := httptest.NewRecorder()
		h.Routes().ServeHTTP(response, lineAccessRequest("/priv/line/member-access?"+query))
		if response.Code != 400 || repo.entitlementReads != 0 {
			t.Fatalf("query=%s status=%d reads=%d", query, response.Code, repo.entitlementReads)
		}
	}
}
func TestLineAccessUsesLiveEditionGrantsIndependentOfUILocale(t *testing.T) {
	h, repo := newLineAccessHandler()
	response := httptest.NewRecorder()
	h.Routes().ServeHTTP(response, lineAccessRequest("/priv/line/member-access?locale=ja"))
	var body struct {
		Data lineMemberAccess `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || !body.Data.Qualified || body.Data.Legal != "allowed" || len(body.Data.Editions) != 1 || body.Data.Editions[0] != (bulletins.BulletinEdition{Series: "children", Locale: "en"}) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.dataReads != 0 || repo.entitlementReads != len(bulletins.BulletinEditionOrder) {
		t.Fatalf("content=%d policies=%d", repo.dataReads, repo.entitlementReads)
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || strings.Contains(response.Body.String(), lineAccessSubject) || strings.Contains(response.Body.String(), "entitlementCode") {
		t.Fatalf("private projection=%s", response.Body.String())
	}
	h.WithBulletinEntitlements(singleEditionGrant("revoked"))
	response = httptest.NewRecorder()
	h.Routes().ServeHTTP(response, lineAccessRequest("/priv/line/member-access"))
	if !strings.Contains(response.Body.String(), `"editions":[]`) {
		t.Fatalf("revoked grant retained: %s", response.Body.String())
	}
}
func TestLineAccessUnqualifiedAndUnavailableAreDifferent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		qualified bool
		err       error
		status    int
	}{
		{"unqualified", false, nil, 200}, {"authority unavailable", false, errors.New("down"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, repo := newLineAccessHandler()
			h.WithLegal(nil, qualificationStub{allowed: tc.qualified, err: tc.err})
			response := httptest.NewRecorder()
			h.Routes().ServeHTTP(response, lineAccessRequest("/priv/line/member-access"))
			if response.Code != tc.status || repo.entitlementReads != 0 {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if tc.status == 200 && !strings.Contains(response.Body.String(), `"qualified":false,"editions":[],"legal":"not_applicable"`) {
				t.Fatal(response.Body.String())
			}
		})
	}
}
func TestLineAccessRejectsPartialEntitlementAuthorityResults(t *testing.T) {
	h, _ := newLineAccessHandler()
	h.WithBulletinEntitlements(lineEntitlementsFunc(func(context.Context, string, []string) ([]operationsclient.EntitlementDecision, error) {
		return []operationsclient.EntitlementDecision{{EntitlementCode: "wrong", Decision: "allow"}}, nil
	}))
	response := httptest.NewRecorder()
	h.Routes().ServeHTTP(response, lineAccessRequest("/priv/line/member-access"))
	if response.Code != 503 {
		t.Fatal(response.Body.String())
	}
}
func TestLineAccessPreservesLegalRequiredAndUnavailable(t *testing.T) {
	body := json.RawMessage(`{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":"Policy","updatedAtLabel":"Updated","updatedAt":"2026年9月30日","intro":"Notice","sections":[{"title":"Purpose","body":["Policy"]}]}}`)
	translations := map[string]legal.Documents{}
	for _, language := range legal.Locales {
		translations[language] = legal.Documents{Terms: body, Privacy: body}
	}
	draft := legal.Draft{Scope: legal.Common, TermsVersion: "terms-v1", PrivacyNoticeVersion: "privacy-v1", Translations: translations}
	common, err := legal.BuildSnapshots(draft)
	if err != nil {
		t.Fatal(err)
	}
	draft.Scope, draft.TermsVersion, draft.PrivacyNoticeVersion = legal.VerifiedMember, "member-terms-v1", "member-privacy-v1"
	draft.CommonTermsVersion, draft.CommonPrivacyNoticeVersion = "terms-v1", "privacy-v1"
	member, err := legal.BuildSnapshots(draft)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		accepted bool
		err      error
		status   int
		legal    string
	}{
		{false, nil, 200, "required"}, {true, nil, 200, "allowed"}, {false, errors.New("down"), 503, ""},
	} {
		h, _ := newLineAccessHandler()
		h.WithLegal(legal.NewService(legalGateRepo{common: common[0], member: member[0]}), qualificationStub{allowed: true}).WithMemberLegalEnforcement(true, legalCheckStub{accepted: tc.accepted, err: tc.err})
		response := httptest.NewRecorder()
		h.Routes().ServeHTTP(response, lineAccessRequest("/priv/line/member-access"))
		if response.Code != tc.status || (tc.legal != "" && !strings.Contains(response.Body.String(), `"legal":"`+tc.legal+`"`)) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}
