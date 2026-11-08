package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type learningRulesRepository struct {
	apiRepository
	calls int
}

func (r *learningRulesRepository) OnlineBulletinLearningRules(context.Context, string) (onlinebulletins.LearningRulePage, error) {
	r.calls++
	return onlinebulletins.LearningRulePage{Items: []onlinebulletins.LearningRule{}}, nil
}
func (r *learningRulesRepository) OnlineBulletinLearningRule(context.Context, string) (onlinebulletins.LearningRuleDetail, error) {
	r.calls++
	return onlinebulletins.LearningRuleDetail{Rule: onlinebulletins.LearningRule{Version: 1, Title: "private-rule"}, Revisions: []onlinebulletins.LearningRuleRevision{}, Examples: []onlinebulletins.LearningExample{}}, nil
}
func (r *learningRulesRepository) SetOnlineBulletinLearningRuleEnabled(_ context.Context, signature string, enabled bool, expected int64, _ string, _ time.Time) (onlinebulletins.LearningRule, error) {
	r.calls++
	if expected != 1 {
		return onlinebulletins.LearningRule{}, bulletins.ErrPrecondition
	}
	return onlinebulletins.LearningRule{Signature: signature, Version: 2, Status: "disabled"}, nil
}

func TestLearningRulesPrivateHTTPContract(t *testing.T) {
	signature := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, method, suffix, scope, body, etag string
		want                                    int
	}{
		{"list", "GET", "", "read", "", "", 200},
		{"cursor", "GET", "?cursor=" + signature, "read", "", "", 200},
		{"detail", "GET", "/" + signature, "read", "", "", 200},
		{"disable", "PUT", "/" + signature, "write", `{"enabled":false}`, `"1"`, 200},
		{"restore", "PUT", "/" + signature, "write", `{"enabled":true}`, `"1"`, 200},
		{"cas", "PUT", "/" + signature, "write", `{"enabled":false}`, "", 428},
		{"stale", "PUT", "/" + signature, "write", `{"enabled":false}`, `"2"`, 412},
		{"missing", "PUT", "/" + signature, "write", `{}`, `"1"`, 400},
		{"null", "PUT", "/" + signature, "write", `{"enabled":null}`, `"1"`, 400},
		{"unknown", "PUT", "/" + signature, "write", `{"enabled":false,"kind":"sermon"}`, `"1"`, 400},
		{"multiple", "PUT", "/" + signature, "write", `{"enabled":false}{}`, `"1"`, 400},
		{"invalid-signature", "GET", "/invalid", "read", "", "", 400},
		{"unknown-query", "GET", "?all=true", "read", "", "", 400},
		{"duplicate-query", "GET", "?cursor=" + signature + "&cursor=" + signature, "read", "", "", 400},
		{"invalid-cursor", "GET", "?cursor=invalid", "read", "", "", 400},
		{"read-scope", "GET", "", "write", "", "", 403},
		{"write-scope", "PUT", "/" + signature, "read", `{"enabled":false}`, `"1"`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &learningRulesRepository{}
			r := httptest.NewRequest(tc.method, "/api/admin/bulletin-learning-rules"+tc.suffix, strings.NewReader(tc.body))
			trustedBulletinAudit(r, "cms:bulletins:"+tc.scope)
			if tc.etag != "" {
				r.Header.Set("If-Match", tc.etag)
			}
			w := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(w, r)
			if w.Code != tc.want || (tc.want == 200 && w.Header().Get("Cache-Control") != "private, no-store") {
				t.Fatal(w.Code, w.Header(), w.Body.String())
			}
		})
	}
	for _, suffix := range []string{"", "/" + signature} {
		r := httptest.NewRequest("GET", "/api/admin/bulletin-learning-rules"+suffix, nil)
		trustedBulletinAudit(r, "cms:bulletins:read")
		w := httptest.NewRecorder()
		NewWithContent(bulletins.NewService(&learningRulesRepository{}, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{err: errors.New("unavailable")}, time.Now).Routes().ServeHTTP(w, r)
		if w.Code != 503 || strings.Contains(w.Body.String(), "private-rule") {
			t.Fatal("learning evidence returned without audit", w.Code, w.Body.String())
		}
	}
}
