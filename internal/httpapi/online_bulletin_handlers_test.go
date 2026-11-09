package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type onlineStateRepository struct {
	apiRepository
	state         onlinebulletins.AdminState
	calls         int
	queueCalls    int
	queueInput    onlinebulletins.QueueExtractionInput
	queueErr      error
	saveCalls     int
	saveInput     onlinebulletins.SaveDraftInput
	confirmCalls  int
	confirmInput  onlinebulletins.ConfirmInput
	restoreCalls  int
	restoreInput  onlinebulletins.RestoreInput
	historyCalls  int
	historyBefore int64
	historyLimit  int
	publishCalls  int
	publishInput  onlinebulletins.PublishInput
	compareInput  onlinebulletins.CompareInput
}

func (r *onlineStateRepository) OnlineBulletinComparison(_ context.Context, _ string, _ string, _ string) (onlinebulletins.Comparison, error) {
	r.calls++
	return onlinebulletins.Comparison{DocumentID: r.state.DocumentID, Version: 3, Units: []onlinebulletins.ComparisonUnit{}}, r.queueErr
}

func (r *onlineStateRepository) ApplyOnlineBulletinComparison(_ context.Context, _ string, _ string, _ string, input onlinebulletins.CompareInput, _ string, _ time.Time) (onlinebulletins.SavedDraft, error) {
	r.calls++
	r.compareInput = input
	return onlinebulletins.SavedDraft{DocumentID: r.state.DocumentID, Version: 4, Revision: 4, LayoutJobID: "job"}, r.queueErr
}

func TestOnlineComparisonRoutesProtectReadsAndCASWrites(t *testing.T) {
	for _, scenario := range []struct {
		name, method, path, scope, body string
		status                          int
	}{
		{"read", "GET", "comparison", "read", "", 200},
		{"read-scope", "GET", "comparison", "write", "", 403},
		{"read-audit", "GET", "comparison", "read", "", 503},
		{"apply", "POST", "compare", "write", `{"canonicalVersion":5,"baseRevision":1,"localRevision":2,"incomingRevision":3,"choices":[]}`, 200},
		{"write-scope", "POST", "compare", "read", `{}`, 403},
		{"missing-cas", "POST", "compare", "write", `{}`, 428},
		{"injected", "POST", "compare", "write", `{"canonicalVersion":5,"baseRevision":1,"localRevision":2,"incomingRevision":3,"layoutManifest":{}}`, 400},
		{"invalid", "POST", "compare", "write", `{"canonicalVersion":5,"baseRevision":0}`, 400},
		{"stale", "POST", "compare", "write", `{"canonicalVersion":5,"baseRevision":1,"localRevision":2,"incomingRevision":3,"choices":[]}`, 409},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repo := &onlineStateRepository{state: onlinebulletins.AdminState{IssueID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f73", DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", Version: 3}}
			audit := &bulletinAuditOutbox{}
			if scenario.name == "read-audit" {
				audit.err = errors.New("unavailable")
			}
			if scenario.name == "stale" {
				repo.queueErr = bulletins.ErrPrecondition
			}
			request := httptest.NewRequest(scenario.method, "/api/admin/bulletins/"+repo.state.IssueID+"/online/general/zh-Hant/"+scenario.path, strings.NewReader(scenario.body))
			trustedBulletinAudit(request, "cms:bulletins:"+scenario.scope)
			if scenario.name != "missing-cas" {
				request.Header.Set("If-Match", `"3"`)
			}
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(audit, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if scenario.name == "apply" && (repo.compareInput.OnlineVersion != 3 || repo.compareInput.IncomingRevision != 3 || response.Header().Get("ETag") != `"4"`) {
				t.Fatal("lost CAS")
			}
			if scenario.status == 200 && response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("cacheable comparison")
			}
		})
	}
}

func (r *onlineStateRepository) PublishOnlineBulletin(_ context.Context, _ string, _ string, _ string, input onlinebulletins.PublishInput, _ string, _ time.Time) (onlinebulletins.ConfirmedRevision, error) {
	r.publishCalls++
	r.publishInput = input
	return onlinebulletins.ConfirmedRevision{DocumentID: r.state.DocumentID, Version: 4, Revision: input.Revision}, r.queueErr
}

func (r *onlineStateRepository) UnpublishOnlineBulletin(_ context.Context, _ string, _ string, _ string, canonical, online int64, _ string, _ time.Time) (onlinebulletins.UnpublishedRevision, error) {
	r.publishCalls++
	r.publishInput = onlinebulletins.PublishInput{CanonicalVersion: canonical, OnlineVersion: online}
	return onlinebulletins.UnpublishedRevision{DocumentID: r.state.DocumentID, Version: 4}, r.queueErr
}

func (r *onlineStateRepository) UnpublishWithOnline(_ context.Context, _ string, _, _ string, canonical, online int64, _ string, _ time.Time) (bulletins.Issue, error) {
	r.publishCalls++
	r.publishInput = onlinebulletins.PublishInput{CanonicalVersion: canonical, OnlineVersion: online}
	return bulletins.Issue{Version: canonical + 1}, r.queueErr
}

func TestPDFUnpublishOnlineRequiresExplicitChoiceAndOnlineCAS(t *testing.T) {
	for _, scenario := range []struct {
		name, body    string
		status, calls int
	}{
		{"default", `{"locale":"zh-Hant"}`, 200, 0},
		{"both", `{"locale":"zh-Hant","unpublishOnline":true,"onlineVersion":3}`, 200, 1},
		{"missing-cas", `{"locale":"zh-Hant","unpublishOnline":true}`, 400, 0},
		{"zero-cas", `{"locale":"zh-Hant","unpublishOnline":true,"onlineVersion":0}`, 400, 0},
		{"implicit-choice", `{"locale":"zh-Hant","onlineVersion":3}`, 400, 0},
		{"unsupported", `{"locale":"en","unpublishOnline":true,"onlineVersion":3}`, 400, 0},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repo := &onlineStateRepository{}
			request := httptest.NewRequest("POST", "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/unpublish", strings.NewReader(scenario.body))
			trustedBulletinAudit(request, "cms:bulletins:publish")
			request.Header.Set("If-Match", `"5"`)
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != scenario.status || repo.publishCalls != scenario.calls {
				t.Fatalf("status=%d calls=%d body=%s", response.Code, repo.publishCalls, response.Body.String())
			}
			if scenario.calls == 1 && (repo.publishInput.CanonicalVersion != 5 || repo.publishInput.OnlineVersion != 3) {
				t.Fatal("lost independent CAS")
			}
		})
	}
}

func TestOnlinePublicationRequiresPublishScopeAndCAS(t *testing.T) {
	for _, action := range []string{"publish", "unpublish"} {
		for _, scenario := range []struct {
			name   string
			status int
		}{{"valid", 200}, {"write-only", 403}, {"missing-cas", 428}, {"stale", 409}, {"gate", 409}, {"audit", 503}, {"invalid", 400}, {"injected", 400}, {"locale", 404}} {
			t.Run(action+"/"+scenario.name, func(t *testing.T) {
				repo := &onlineStateRepository{state: onlinebulletins.AdminState{IssueID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f73", DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", Version: 3, CanonicalVersion: 5, Local: &onlinebulletins.AdminRevision{Revision: 2}}}
				body, locale := `{"canonicalVersion":5}`, "zh-Hant"
				if action == "publish" {
					body = `{"canonicalVersion":5,"revision":2}`
				}
				switch scenario.name {
				case "stale":
					repo.queueErr = bulletins.ErrPrecondition
				case "gate":
					repo.queueErr = bulletins.ErrConflict
				case "audit":
					repo.queueErr = bulletins.ErrAuditUnavailable
				case "invalid":
					body = `{"canonicalVersion":0}`
				case "injected":
					body = `{"canonicalVersion":5,"revision":2,"document":{}}`
				case "locale":
					locale = "en"
				}
				request := httptest.NewRequest("POST", "/api/admin/bulletins/"+repo.state.IssueID+"/online/general/"+locale+"/"+action, strings.NewReader(body))
				trustedBulletinAudit(request, "cms:bulletins:publish")
				request.Header.Set("If-Match", `"3"`)
				if scenario.name == "write-only" {
					trustedBulletinAudit(request, "cms:bulletins:write")
				}
				if scenario.name == "missing-cas" {
					request.Header.Del("If-Match")
				}
				response := httptest.NewRecorder()
				NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
				if response.Code != scenario.status {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				if scenario.name == "valid" && (repo.publishInput.CanonicalVersion != 5 || repo.publishInput.OnlineVersion != 3 || response.Header().Get("ETag") != `"4"` || response.Header().Get("Cache-Control") != "private, no-store") {
					t.Fatal("lost CAS/no-store")
				}
				if (scenario.status == 400 || scenario.status == 403 || scenario.status == 404 || scenario.status == 428) && repo.publishCalls != 0 {
					t.Fatal("rejected request reached repository")
				}
			})
		}
	}
}

func (r *onlineStateRepository) OnlineBulletinHistory(_ context.Context, _ string, _ string, _ string, before int64, limit int) (onlinebulletins.RevisionPage, error) {
	r.historyCalls++
	r.historyBefore = before
	r.historyLimit = limit
	return onlinebulletins.RevisionPage{DocumentID: r.state.DocumentID, Items: []onlinebulletins.RevisionSummary{{Revision: 2}}}, r.queueErr
}
func (r *onlineStateRepository) OnlineBulletinRevision(_ context.Context, _ string, _ string, _ string, revision int64) (onlinebulletins.HistoricalRevision, error) {
	r.historyCalls++
	return onlinebulletins.HistoricalRevision{DocumentID: r.state.DocumentID, AdminRevision: onlinebulletins.AdminRevision{Revision: revision}, CanonicalMetadata: onlinebulletins.CanonicalMetadata{Title: "private historical title"}}, r.queueErr
}

func TestOnlineHistoryReadsRequireScopeAndDurableAudit(t *testing.T) {
	for _, path := range []string{"/revisions?before=10&limit=2", "/revisions/2"} {
		for _, scenario := range []struct {
			name   string
			status int
		}{{"valid", 200}, {"scope", 403}, {"audit-failed", 503}, {"missing", 404}} {
			t.Run(path+scenario.name, func(t *testing.T) {
				repo := &onlineStateRepository{state: onlinebulletins.AdminState{IssueID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f73", DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74"}}
				audit := &bulletinAuditOutbox{}
				if scenario.name == "audit-failed" {
					audit.err = errors.New("unavailable")
				}
				if scenario.name == "missing" {
					repo.queueErr = bulletins.ErrNotFound
				}
				request := httptest.NewRequest("GET", "/api/admin/bulletins/"+repo.state.IssueID+"/online/general/zh-Hant"+path, nil)
				trustedBulletinAudit(request, "cms:bulletins:read")
				if scenario.name == "scope" {
					trustedBulletinAudit(request, "cms:bulletins:write")
				}
				response := httptest.NewRecorder()
				NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(audit, time.Now).Routes().ServeHTTP(response, request)
				if response.Code != scenario.status {
					t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
				}
				if scenario.name == "valid" && (len(audit.events) != 1 || audit.events[0].Action != "cms.bulletin.online.read" || audit.events[0].ResourceID != repo.state.DocumentID || response.Header().Get("Cache-Control") != "private, no-store") {
					t.Fatal("unbound or cacheable history audit")
				}
				if scenario.name == "valid" && strings.Contains(path, "?") && (repo.historyBefore != 10 || repo.historyLimit != 2) {
					t.Fatal("lost keyset bounds")
				}
				if scenario.name == "audit-failed" && strings.Contains(response.Body.String(), "private historical title") {
					t.Fatal("history returned before durable audit")
				}
			})
		}
	}
	for _, path := range []string{"/revisions?limit=51", "/revisions?before=-1", "/revisions?before=1&before=2", "/revisions/0"} {
		repo := &onlineStateRepository{}
		request := httptest.NewRequest("GET", "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/online/general/zh-Hant"+path, nil)
		trustedBulletinAudit(request, "cms:bulletins:read")
		response := httptest.NewRecorder()
		NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
		if response.Code != 400 || repo.historyCalls != 0 {
			t.Fatalf("unbounded history %s status=%d", path, response.Code)
		}
	}
}

func (r *onlineStateRepository) RestoreOnlineBulletin(_ context.Context, _ string, _ string, _ string, input onlinebulletins.RestoreInput, _ string, _ time.Time) (onlinebulletins.SavedDraft, error) {
	r.restoreCalls++
	r.restoreInput = input
	return onlinebulletins.SavedDraft{DocumentID: r.state.DocumentID, Version: 4, Revision: 3, LayoutJobID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f75"}, r.queueErr
}

func TestOnlineRestoreRequiresPublishScopeAndServerOwnedHistory(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
	}{
		{"valid", 200}, {"scope", 403}, {"write-only", 403}, {"missing-cas", 428}, {"stale", 409}, {"missing-revision", 404}, {"injected-content", 400}, {"bad-revision", 400},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repo := &onlineStateRepository{state: onlinebulletins.AdminState{IssueID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f73", DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", Version: 3, CanonicalVersion: 5}}
			body, revision := `{"canonicalVersion":5}`, "1"
			if scenario.name == "stale" {
				repo.queueErr = bulletins.ErrPrecondition
			}
			if scenario.name == "missing-revision" {
				repo.queueErr = bulletins.ErrNotFound
			}
			if scenario.name == "injected-content" {
				body = `{"canonicalVersion":5,"document":{}}`
			}
			if scenario.name == "bad-revision" {
				revision = "-1"
			}
			request := httptest.NewRequest("POST", "/api/admin/bulletins/"+repo.state.IssueID+"/online/general/zh-Hant/revisions/"+revision+"/restore", strings.NewReader(body))
			trustedBulletinAudit(request, "cms:bulletins:publish")
			request.Header.Set("If-Match", `"3"`)
			if scenario.name == "scope" {
				trustedBulletinAudit(request, "cms:bulletins:read")
			}
			if scenario.name == "write-only" {
				trustedBulletinAudit(request, "cms:bulletins:write")
			}
			if scenario.name == "missing-cas" {
				request.Header.Del("If-Match")
			}
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if scenario.status == 200 && (repo.restoreInput.Revision != 1 || repo.restoreInput.OnlineVersion != 3 || repo.restoreInput.CanonicalVersion != 5 || response.Header().Get("ETag") != `"4"`) {
				t.Fatal("restore lost revision or CAS")
			}
			if scenario.status != 200 && scenario.name != "stale" && scenario.name != "missing-revision" && repo.restoreCalls != 0 {
				t.Fatal("rejected restore reached repository")
			}
		})
	}
}

func (r *onlineStateRepository) ConfirmOnlineBulletin(_ context.Context, _ string, _ string, _ string, input onlinebulletins.ConfirmInput, _ string, _ time.Time) (onlinebulletins.ConfirmedRevision, error) {
	r.confirmCalls++
	r.confirmInput = input
	return onlinebulletins.ConfirmedRevision{DocumentID: r.state.DocumentID, Version: 4, Revision: 2}, r.queueErr
}

func TestOnlineConfirmationRequiresWriteScopeAndExactVersion(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
	}{
		{"valid", 200}, {"scope", 403}, {"missing-cas", 428}, {"stale", 409}, {"unmeasured", 409}, {"injected-proof", 400},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			repo := &onlineStateRepository{state: onlinebulletins.AdminState{IssueID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f73", DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", Version: 3, CanonicalVersion: 5}}
			if scenario.name == "stale" {
				repo.queueErr = bulletins.ErrPrecondition
			}
			if scenario.name == "unmeasured" {
				repo.queueErr = bulletins.ErrConflict
			}
			body := `{"canonicalVersion":5,"revision":2,"layoutValidationHash":"` + strings.Repeat("a", 64) + `","pageCount":14,"acceptedIssues":[]}`
			if scenario.name == "injected-proof" {
				body = strings.TrimSuffix(body, "}") + `,"validated":true}`
			}
			request := httptest.NewRequest("POST", "/api/admin/bulletins/"+repo.state.IssueID+"/online/general/zh-Hant/confirm", strings.NewReader(body))
			trustedBulletinAudit(request, "cms:bulletins:write")
			request.Header.Set("If-Match", `"3"`)
			if scenario.name == "scope" {
				trustedBulletinAudit(request, "cms:bulletins:read")
			}
			if scenario.name == "missing-cas" {
				request.Header.Del("If-Match")
			}
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "private, no-store" && scenario.name != "scope" {
				t.Fatal("confirmation response cacheable")
			}
			if scenario.status == 200 && (response.Header().Get("ETag") != `"4"` || repo.confirmInput.OnlineVersion != 3 || repo.confirmInput.CanonicalVersion != 5 || repo.confirmInput.PageCount != 14) {
				t.Fatal("confirmation lost CAS or review input")
			}
			if scenario.status != 200 && scenario.status != 409 && repo.confirmCalls != 0 {
				t.Fatal("rejected request confirmed")
			}
		})
	}
}

func (r *onlineStateRepository) SaveOnlineBulletinDraft(_ context.Context, _ string, _ string, _ string, input onlinebulletins.SaveDraftInput, _ string, _ time.Time) (onlinebulletins.SavedDraft, error) {
	r.saveCalls++
	r.saveInput = input
	return onlinebulletins.SavedDraft{DocumentID: r.state.DocumentID, Version: 4, Revision: 2, LayoutJobID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f75"}, r.queueErr
}

func TestOnlineDraftSaveUsesTypedComponentsAndIndependentCAS(t *testing.T) {
	block := onlinebulletins.Block{ID: "paragraph", Style: onlinebulletins.ParagraphStyle{FontSize: 16, LineHeight: 24}, Sentences: []onlinebulletins.Sentence{{ID: "sentence", Spans: []onlinebulletins.Span{{Text: "修正文字。", FontRole: "body"}}}}}
	// The repository owns graph validation against the current immutable draft;
	// this boundary test observes the typed edit and CAS passed to that owner.
	components := []onlinebulletins.Component{{ID: "cover", Type: "cover", Cover: &onlinebulletins.Cover{}}, {ID: "body", Type: "bodySection", BodySection: &onlinebulletins.BodySection{Kind: "sermon", Blocks: []onlinebulletins.Block{block}}}, {ID: "hymns", Type: "hymnLyrics", HymnLyrics: &onlinebulletins.HymnLyrics{}}, {ID: "summary", Type: "backSummary", Items: []onlinebulletins.Item{}}, {ID: "announcements", Type: "announcements", Items: []onlinebulletins.Item{}}, {ID: "prayers", Type: "victoriesAndPrayers", Items: []onlinebulletins.Item{}}}
	for _, scenario := range []struct {
		name   string
		status int
	}{{"valid", 200}, {"scope", 403}, {"stale", 409}, {"missing-cas", 428}, {"layout-injection", 400}} {
		t.Run(scenario.name, func(t *testing.T) {
			repo := &onlineStateRepository{state: onlinebulletins.AdminState{IssueID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f73", DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", Version: 3, CanonicalVersion: 5}}
			if scenario.name == "stale" {
				repo.queueErr = bulletins.ErrPrecondition
			}
			value := map[string]any{"canonicalVersion": 5, "components": components}
			if scenario.name == "layout-injection" {
				value["layoutManifest"] = map[string]any{"validated": true}
			}
			raw, _ := json.Marshal(value)
			request := httptest.NewRequest("PUT", "/api/admin/bulletins/"+repo.state.IssueID+"/online/general/zh-Hant/draft", strings.NewReader(string(raw)))
			trustedBulletinAudit(request, "cms:bulletins:write")
			request.Header.Set("If-Match", `"3"`)
			if scenario.name == "scope" {
				trustedBulletinAudit(request, "cms:bulletins:read")
			}
			if scenario.name == "missing-cas" {
				request.Header.Del("If-Match")
			}
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if scenario.status == 200 && (response.Header().Get("ETag") != `"4"` || repo.saveInput.OnlineVersion != 3 || repo.saveInput.CanonicalVersion != 5 || repo.saveInput.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "修正文字。") {
				t.Fatal("edit or CAS changed")
			}
			if scenario.status != 200 && scenario.status != 409 && repo.saveCalls != 0 {
				t.Fatal("rejected request saved")
			}
		})
	}
}

func (r *onlineStateRepository) QueueOnlineBulletinExtraction(_ context.Context, _ string, input onlinebulletins.QueueExtractionInput, _ string, _ time.Time) (onlinebulletins.QueuedExtraction, error) {
	r.queueCalls++
	r.queueInput = input
	return onlinebulletins.QueuedExtraction{DocumentID: r.state.DocumentID, Version: 4, Job: onlinebulletins.AdminJob{ID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f75", Status: "queued"}}, r.queueErr
}

func TestOnlineExtractionStartTrustsOnlyVerifiedCurrentSource(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
	}{{"valid", 202}, {"retry-prior-parser", 202}, {"auto-publisher", 202}, {"auto-writer", 403}, {"draft-notification", 400}, {"read-only", 403}, {"missing-cas", 428}, {"stale", 409}, {"unscanned", 404}, {"asset-unavailable", 503}, {"injected-source", 400}, {"trailing-json", 400}} {
		t.Run(scenario.name, func(t *testing.T) {
			issue := bulletinIssue()
			issue.ID, issue.Version = "018f47d2-e5d1-4f3f-8f18-6c8e32621f73", 5
			issue.Versions = []bulletins.Version{{Series: "general", Locale: "zh-Hant", PDFAssetID: "current-source"}}
			repo := &onlineStateRepository{apiRepository: apiRepository{issue: issue}, state: onlinebulletins.AdminState{IssueID: issue.ID, DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", Version: 3, CanonicalVersion: 5}}
			uploads := &apiUploads{completed: assetclient.Asset{ID: "current-source", OwnerID: issue.ID, OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", Namespace: "cms.weekly.pdf", Purpose: "weekly_bulletin", Locale: "zh-Hant", Visibility: "private", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready", DetectedMIMEType: "application/pdf", ChecksumSHA256: strings.Repeat("a", 64)}}
			body := `{"canonicalVersion":5,"retry":false}`
			switch scenario.name {
			case "auto-publisher", "auto-writer":
				body = `{"canonicalVersion":5,"autoPublish":true,"notifySubscribers":true}`
			case "draft-notification":
				body = `{"canonicalVersion":5,"notifySubscribers":true}`
			case "retry-prior-parser":
				body = `{"canonicalVersion":5,"retry":true}`
			case "stale":
				repo.queueErr = bulletins.ErrPrecondition
			case "unscanned":
				uploads.completed.ScanStatus = "pending"
			case "asset-unavailable":
				uploads.getError = assetclient.ErrUnavailable
			case "injected-source":
				body = `{"canonicalVersion":5,"sourceAssetId":"attacker"}`
			case "trailing-json":
				body += `{}`
			}
			handler := NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, uploads, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
			request := httptest.NewRequest("POST", "/api/admin/bulletins/"+issue.ID+"/online/general/zh-Hant/extractions", strings.NewReader(body))
			request.Header.Set("If-Match", `"3"`)
			trustedBulletinAudit(request, "cms:bulletins:write")
			if scenario.name == "auto-publisher" {
				trustedBulletinAudit(request, "cms:bulletins:write cms:bulletins:publish")
			}
			if scenario.name == "read-only" {
				trustedBulletinAudit(request, "cms:bulletins:read")
			}
			if scenario.name == "missing-cas" {
				request.Header.Del("If-Match")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if scenario.name != "read-only" && response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("cacheable mutation")
			}
			if scenario.status == 202 && (repo.queueInput.SourceAssetID != "current-source" || repo.queueInput.SourceChecksum != strings.Repeat("a", 64) || repo.queueInput.OnlineVersion != 3 || repo.queueInput.CanonicalVersion != 5 || repo.queueInput.Retry != (scenario.name == "retry-prior-parser") || response.Header().Get("ETag") != `"4"`) {
				t.Fatal("unverified source or wrong CAS")
			}
			if scenario.name == "auto-publisher" && (!repo.queueInput.AutoPublish || !repo.queueInput.NotifySubscribers) {
				t.Fatal("lost publisher intent")
			}
			if scenario.status == 409 && (!strings.Contains(response.Body.String(), `"currentVersion":3`) || !strings.Contains(response.Body.String(), `"canonicalVersion":5`)) {
				t.Fatal("missing actionable conflict metadata")
			}
			if scenario.status != 202 && scenario.status != 409 && repo.queueCalls != 0 {
				t.Fatal("rejected request queued a job")
			}
			if uploads.serviceGrantCalls != 0 {
				t.Fatal("API performed extraction grant instead of isolated worker")
			}
		})
	}
}

func (r *onlineStateRepository) OnlineBulletinState(_ context.Context, issue, series, locale string) (onlinebulletins.AdminState, error) {
	r.calls++
	if issue != r.state.IssueID || series != "general" || locale != "zh-Hant" {
		return onlinebulletins.AdminState{}, bulletins.ErrNotFound
	}
	return r.state, nil
}

func TestOnlineAdminStateIsProtectedAuditedAndNotCached(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
	}{{"ready", 200}, {"not-started", 200}, {"scope", 403}, {"audit", 503}, {"locale", 404}, {"invalid-id", 400}} {
		t.Run(scenario.name, func(t *testing.T) {
			repo := &onlineStateRepository{state: onlinebulletins.AdminState{IssueID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f73", DocumentID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", Version: 3, DraftStatus: "reviewRequired"}}
			if scenario.name == "not-started" {
				repo.state.DocumentID, repo.state.Version = "", 0
			}
			outbox := &bulletinAuditOutbox{}
			if scenario.name == "audit" {
				outbox.err = errors.New("private audit error")
			}
			handler := NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(outbox, time.Now).Routes()
			path := "/api/admin/bulletins/" + repo.state.IssueID + "/online/general/zh-Hant"
			if scenario.name == "locale" {
				path = strings.ReplaceAll(path, "zh-Hant", "en")
			}
			if scenario.name == "invalid-id" {
				path = strings.ReplaceAll(path, repo.state.IssueID, "invalid")
			}
			request := httptest.NewRequest("GET", path, nil)
			trustedBulletinAudit(request, "cms:bulletins:read")
			if scenario.name == "scope" {
				trustedBulletinAudit(request, "cms:bulletins:write")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if scenario.name != "scope" && response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("cacheable admin state")
			}
			if scenario.status == 200 {
				wantAction, wantResource, wantETag := "cms.bulletin.online.read", repo.state.DocumentID, `"3"`
				if scenario.name == "not-started" {
					wantAction, wantResource, wantETag = "cms.bulletin.read", repo.state.IssueID, `"0"`
				}
				if len(outbox.events) != 1 || outbox.events[0].Action != wantAction || outbox.events[0].ResourceID != wantResource || response.Header().Get("ETag") != wantETag {
					t.Fatal("wrong audit or CAS identity")
				}
			} else if strings.Contains(response.Body.String(), "reviewRequired") || strings.Contains(response.Body.String(), "private audit error") {
				t.Fatal("state leaked on error")
			}
			if (scenario.name == "scope" || scenario.name == "locale" || scenario.name == "invalid-id") && repo.calls != 0 {
				t.Fatal("invalid request reached repository")
			}
		})
	}
}

func TestOnlineHistoryDeletionConflictIsActionable(t *testing.T) {
	response := httptest.NewRecorder()
	handleError(response, bulletins.ErrOnlineDocumentExists)
	if response.Code != 409 || !strings.Contains(response.Body.String(), `"code":"online_document_exists"`) {
		t.Fatal("missing explicit Online history conflict", response.Code)
	}
}

func TestOnlineSourcePreviewUsesCurrentEditionAndRevokesGrant(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		grant  bool
	}{
		{"valid", 200, true}, {"scope", 403, false}, {"missing", 404, false},
		{"owner", 404, false}, {"scan", 404, false}, {"locale", 404, false},
		{"content-type", 503, true}, {"oversize", 503, true},
		{"checksum", 503, true}, {"upstream", 503, true}, {"revoke", 503, true},
		{"audit", 503, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pdf := "%PDF-1.7\ncontrolled preview"
			sum := sha256.Sum256([]byte(pdf))
			checksum := hex.EncodeToString(sum[:])
			issue := bulletinIssue()
			issue.ID = "018f47d2-e5d1-4f3f-8f18-6c8e32621f73"
			issue.Versions = []bulletins.Version{{Series: "general", Locale: "zh-Hant", PDFAssetID: "current-source", Status: "draft"}}
			if scenario.name == "missing" {
				issue.Versions = nil
			}
			grants, revokes, calls := 0, 0, 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if !strings.Contains(r.URL.Path, "/current-source") {
					t.Error("not the current edition source")
				}
				switch {
				case r.Method == http.MethodDelete:
					revokes++
					if scenario.name == "revoke" {
						w.WriteHeader(503)
						return
					}
					w.WriteHeader(204)
				case r.Method == http.MethodPost:
					grants++
					_ = json.NewEncoder(w).Encode(assetclient.Grant{ID: "preview-grant", AssetID: "current-source"})
				case strings.HasSuffix(r.URL.Path, "/download"):
					if r.Header.Get("Range") != "" {
						t.Error("preview forwarded a partial range")
					}
					w.Header().Set("Content-Type", "application/pdf")
					switch scenario.name {
					case "content-type":
						w.Header().Set("Content-Type", "text/html")
					case "oversize":
						w.Header().Set("Content-Length", "20971521")
					case "checksum":
						pdf += "changed"
					case "upstream":
						w.WriteHeader(500)
						_, _ = w.Write([]byte("private-upstream-error"))
						return
					}
					_, _ = w.Write([]byte(pdf))
				default:
					asset := assetclient.Asset{ID: "current-source", OwnerID: issue.ID, OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", Namespace: "cms.weekly.pdf", Purpose: "weekly_bulletin", Locale: "zh-Hant", Visibility: "private", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready", DetectedMIMEType: "application/pdf", ChecksumSHA256: checksum}
					switch scenario.name {
					case "owner":
						asset.OwnerID = "another-issue"
					case "scan":
						asset.ScanStatus = "pending"
					case "locale":
						asset.Locale = "en"
					}
					_ = json.NewEncoder(w).Encode(asset)
				}
			}))
			defer upstream.Close()
			client := assetclient.New(upstream.URL, "hhc-web-api", "")
			outbox := &bulletinAuditOutbox{}
			if scenario.name == "audit" {
				outbox.err = errors.New("audit unavailable")
			}
			handler := NewWithContent(bulletins.NewService(&apiRepository{issue: issue}, time.Now), nil, nil, client, "api-gateway", "", true).WithAuditOutbox(outbox, time.Now).Routes()
			request := httptest.NewRequest("GET", "/api/admin/bulletins/"+issue.ID+"/online/general/zh-Hant/source-pdf", nil)
			request.Header.Set("Range", "bytes=0-4")
			trustedBulletinAudit(request, "cms:bulletins:read")
			if scenario.name == "scope" {
				trustedBulletinAudit(request, "cms:bulletins:write")
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d expected=%d body=%s", response.Code, scenario.status, response.Body.String())
			}
			if scenario.name != "scope" && response.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("preview may be cached")
			}
			if scenario.status == 200 && (response.Body.String() != pdf || response.Header().Get("Content-Type") != "application/pdf" || response.Header().Get("X-HHC-Source-SHA256") != checksum) {
				t.Fatal("incomplete or unbound source preview")
			}
			if strings.Contains(response.Body.String(), "private-upstream-error") {
				t.Fatal("upstream content leaked")
			}
			if scenario.status != 200 && strings.Contains(response.Body.String(), "%PDF-") {
				t.Fatal("PDF leaked on failure")
			}
			if scenario.status == 200 && (len(outbox.events) != 1 || outbox.events[0].Action != "cms.bulletin.read") {
				t.Fatal("source read not audited")
			}
			if scenario.grant && (grants != 1 || revokes != 1) {
				t.Fatalf("grant lifecycle=%d/%d", grants, revokes)
			}
			if !scenario.grant && grants != 0 {
				t.Fatal("grant created for rejected preview")
			}
			if scenario.name == "scope" && calls != 0 {
				t.Fatal("source lookup before authorization")
			}
		})
	}
}
