package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type conversionRepository struct {
	onlineStateRepository
	input       onlinebulletins.QueueSimplifiedInput
	stateErr    error
	layoutRetry bool
}

func (r *conversionRepository) OnlineBulletinState(_ context.Context, _, _, _ string) (onlinebulletins.AdminState, error) {
	return onlinebulletins.AdminState{Version: 1, CanonicalVersion: 5}, r.stateErr
}

func (r *conversionRepository) QueueOnlineBulletinConversion(_ context.Context, _ string, input onlinebulletins.QueueSimplifiedInput, _ string, _ time.Time) (onlinebulletins.QueuedExtraction, error) {
	r.calls++
	r.input = input
	if r.layoutRetry {
		return onlinebulletins.QueuedExtraction{DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", Version: 2, Job: onlinebulletins.AdminJob{ID: "job", Status: "review_ready"}}, nil
	}
	return onlinebulletins.QueuedExtraction{DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", Version: 1, Job: onlinebulletins.AdminJob{ID: "job", Status: "queued"}}, r.queueErr
}

func TestOnlineConversionLayoutRetryContract(t *testing.T) {
	repo := &conversionRepository{layoutRetry: true}
	r := httptest.NewRequest("POST", "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/online/general/zh-Hans/conversions", strings.NewReader(`{"canonicalVersion":5,"sourceOnlineVersion":3,"sourceRevision":2,"retry":true}`))
	trustedBulletinAudit(r, "cms:bulletins:write")
	r.Header.Set("If-Match", `"1"`)
	w := httptest.NewRecorder()
	NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("ETag") != `"2"` || !repo.input.Retry || repo.input.TargetOnlineVersion != 1 || !strings.Contains(w.Body.String(), `"status":"review_ready"`) {
		t.Fatalf("layout retry lost completed conversion or new version: %d %s", w.Code, w.Body.String())
	}
}

func TestOnlineConversionBoundary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{"valid", 202}, {"missing-cas", 428}, {"stale", 409}, {"stale-before-target", 409}, {"scope", 403}, {"service", 401}, {"locale", 404}, {"series", 404}, {"injected", 400}, {"invalid", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &conversionRepository{}
			body := `{"canonicalVersion":5,"sourceOnlineVersion":3,"sourceRevision":2}`
			locale, series := "zh-Hans", "general"
			if tc.name == "stale" {
				repo.queueErr = bulletins.ErrPrecondition
			}
			if tc.name == "stale-before-target" {
				repo.queueErr = bulletins.ErrPrecondition
				repo.stateErr = bulletins.ErrNotFound
			}
			if tc.name == "locale" {
				locale = "zh-Hant"
			}
			if tc.name == "series" {
				series = "children"
			}
			if tc.name == "injected" {
				body = `{"canonicalVersion":5,"sourceOnlineVersion":3,"sourceRevision":2,"sourceAssetId":"forged"}`
			}
			if tc.name == "invalid" {
				body = `{"canonicalVersion":5,"sourceOnlineVersion":3,"sourceRevision":0}`
			}
			r := httptest.NewRequest("POST", "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/online/"+series+"/"+locale+"/conversions", strings.NewReader(body))
			trustedBulletinAudit(r, "cms:bulletins:write")
			r.Header.Set("If-Match", `"0"`)
			if tc.name == "missing-cas" {
				r.Header.Del("If-Match")
			}
			if tc.name == "scope" {
				trustedBulletinAudit(r, "cms:bulletins:read")
			}
			if tc.name == "service" {
				r.Header = serviceRequest("POST", r.URL.Path).Header
				r.Header.Set("X-HHC-Scopes", "cms:bulletins:write")
				r.Header.Set("If-Match", `"0"`)
			}
			w := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if tc.name == "valid" && (repo.input.TargetOnlineVersion != 0 || repo.input.SourceRevision != 2 || repo.input.SourceOnlineVersion != 3 || repo.input.CanonicalVersion != 5 || w.Header().Get("ETag") != `"1"` || w.Header().Get("Cache-Control") != "private, no-store") {
				t.Fatal("lost source/target CAS or response headers")
			}
			if tc.status != 202 && tc.status != 409 && repo.calls != 0 {
				t.Fatal("rejected request reached repository")
			}
		})
	}
}
