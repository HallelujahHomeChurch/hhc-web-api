package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"net/http/httptest"
	"testing"
)

type legalGateRepo struct {
	legalRepoStub
	common, member legal.Snapshot
}

func (r legalGateRepo) Current(_ context.Context, scope legal.Scope, _ string) (legal.Snapshot, error) {
	if scope == legal.Common {
		return r.common, nil
	}
	return r.member, nil
}

type legalCheckStub struct {
	accepted bool
	err      error
}

func (c legalCheckStub) CheckLegalAcceptance(context.Context, string, string, string, string) (bool, error) {
	return c.accepted, c.err
}
func TestMemberLegalGateFailsClosedBeforeSensitiveHandler(t *testing.T) {
	body := json.RawMessage(`{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":"Policy","updatedAtLabel":"Updated","updatedAt":"2026年9月30日","intro":"Notice","sections":[{"title":"Purpose","body":["Policy"]}]}}`)
	translations := map[string]legal.Documents{}
	for _, locale := range legal.Locales {
		translations[locale] = legal.Documents{Terms: body, Privacy: body}
	}
	draft := legal.Draft{Scope: legal.Common, TermsVersion: "terms-v1", PrivacyNoticeVersion: "privacy-v1", Translations: translations}
	common, err := legal.BuildSnapshots(draft)
	if err != nil {
		t.Fatal(err)
	}
	draft.Scope = legal.VerifiedMember
	draft.TermsVersion = "member-terms-v1"
	draft.PrivacyNoticeVersion = "member-privacy-v1"
	draft.CommonTermsVersion = "terms-v1"
	draft.CommonPrivacyNoticeVersion = "privacy-v1"
	member, err := legal.BuildSnapshots(draft)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                         string
		enabled, qualified, accepted bool
		err                          error
		want                         int
	}{{"legacy", false, false, false, nil, 200}, {"missing acceptance", true, true, false, nil, 428}, {"valid", true, true, true, nil, 200}, {"revoked", true, false, true, nil, 403}, {"unavailable", true, true, false, errors.New("down"), 503}} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true, nil).WithLegal(legal.NewService(legalGateRepo{common: common[0], member: member[0]}), qualificationStub{allowed: tc.qualified}).WithMemberLegalEnforcement(tc.enabled, legalCheckStub{tc.accepted, tc.err})
			req := httptest.NewRequest("GET", "/api/member/bulletins?locale=zh-Hant", nil)
			req = req.WithContext(context.WithValue(req.Context(), principalKey{}, principal{UserID: "01995fd2-6f6c-7b40-8707-6503edc362c8"}))
			rec := httptest.NewRecorder()
			allowed := h.requireCurrentMemberLegal(rec, req)
			if allowed {
				rec.WriteHeader(200)
			}
			if rec.Code != tc.want || allowed != (tc.want == 200) {
				t.Fatalf("allowed=%v code=%d body=%s", allowed, rec.Code, rec.Body.String())
			}
		})
	}
}
