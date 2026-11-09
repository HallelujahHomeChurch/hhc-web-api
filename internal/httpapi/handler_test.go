package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/logging"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/google/uuid"
)

type engagementProxyFake struct {
	path      string
	actor     string
	requestID string
	key       string
	body      string
}

func (f *engagementProxyFake) ForwardSubmission(_ context.Context, body io.Reader, actor, requestID, key string) (*http.Response, error) {
	value, _ := io.ReadAll(body)
	f.path, f.actor, f.requestID, f.key, f.body = "/priv/campaign-submissions", actor, requestID, key, string(value)
	return &http.Response{StatusCode: http.StatusAccepted, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
}

func TestNotificationSubmissionRoutesEnforceScopesAndCanonicalWeeklyCopy(t *testing.T) {
	issue := bulletinIssue()
	number := 1300
	issue.IssueNumber = &number
	issue.Versions = []bulletins.Version{{Series: "children", Locale: "zh-Hant", Title: "兒童週報", Status: "published"}}
	repository := &notificationAPIRepository{apiRepository: &apiRepository{issue: issue}}
	proxy := &engagementProxyFake{}
	handler := NewWithContent(bulletins.NewService(repository, time.Now), nil, nil, nil, "api-gateway", "", true, proxy).Routes()

	preview := httptest.NewRequest(http.MethodGet, "/api/admin/bulletins/issue-1/notification-preview?series=children", nil)
	trusted(preview, "cms:bulletins:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, preview)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"name":"Children's weekly bulletin 1300"`) || !strings.Contains(response.Body.String(), `"channel":"email"`) || !strings.Contains(response.Body.String(), `"channel":"web_push"`) {
		t.Fatalf("preview status=%d body=%s", response.Code, response.Body.String())
	}

	send := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/notifications?series=children", strings.NewReader(`{}`))
	send.Header.Set("Idempotency-Key", uuid.NewString())
	trusted(send, "cms:bulletins:read campaigns:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, send)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing send scope status=%d", response.Code)
	}

	send = httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/notifications?series=children", strings.NewReader(`{}`))
	send.Header.Set("Idempotency-Key", uuid.NewString())
	trusted(send, "cms:bulletins:read campaigns:write campaigns:send")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, send)
	if response.Code != http.StatusAccepted || !repository.submitted || !strings.Contains(response.Body.String(), `"notificationGeneration":1`) {
		t.Fatalf("send status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCustomNotificationSubmissionForwardsOneAtomicCommand(t *testing.T) {
	proxy := &engagementProxyFake{}
	handler := NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil, "api-gateway", "", true, proxy).Routes()
	key := uuid.NewString()
	body := `{"name":"通知","notifications":[{"channel":"email","audienceType":"newsletter","translations":{"zh-Hant":{"subject":"主旨","body":"內容","clickBehavior":"none","actionUrl":""}}},{"channel":"web_push","audienceType":"all","translations":{"zh-Hant":{"subject":"主旨","body":"內容","clickBehavior":"none","actionUrl":""}}}]}`
	request := httptest.NewRequest(http.MethodPost, "/api/admin/campaign-submissions", strings.NewReader(body))
	request.Header.Set("Idempotency-Key", key)
	trusted(request, "campaigns:write campaigns:send")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || proxy.path != "/priv/campaign-submissions" || proxy.key != key || proxy.body != body {
		t.Fatalf("status=%d path=%q key=%q body=%s", response.Code, proxy.path, proxy.key, response.Body.String())
	}
}

func (f *engagementProxyFake) Forward(_ context.Context, _ string, path string, _ io.Reader, actor, requestID string) (*http.Response, error) {
	f.path, f.actor, f.requestID = path, actor, requestID
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
	}, nil
}

func TestCampaignProxyRequiresCampaignReadScopeAndForwardsActor(t *testing.T) {
	proxy := &engagementProxyFake{}
	handler := NewWithContent(
		bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil,
		"api-gateway", "", true, proxy,
	).Routes()

	forbidden := httptest.NewRequest(http.MethodGet, "/api/admin/campaigns", nil)
	trusted(forbidden, "assets:write")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, forbidden)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forbidden status=%d", response.Code)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/admin/campaigns?q=August&page=2", nil)
	trusted(request, "campaigns:read")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || proxy.path != "/priv/campaigns?q=August&page=2" || proxy.actor != "user-1" || proxy.requestID != "request-1" {
		t.Fatalf("status=%d path=%q actor=%q request=%q body=%s", response.Code, proxy.path, proxy.actor, proxy.requestID, response.Body.String())
	}
}

func TestNotificationSummaryProxyForwardsQueryWithCampaignReadScope(t *testing.T) {
	proxy := &engagementProxyFake{}
	handler := NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil,
		"api-gateway", "", true, proxy).Routes()

	request := httptest.NewRequest(http.MethodGet, "/api/admin/notification-summaries?page=2&q=weekly&sort=updatedAt&direction=desc", nil)
	trusted(request, "campaigns:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || proxy.path != "/priv/notification-summaries?page=2&q=weekly&sort=updatedAt&direction=desc" {
		t.Fatalf("status=%d path=%q", response.Code, proxy.path)
	}
}

func TestCampaignDeliveryProxyRoutes(t *testing.T) {
	proxy := &engagementProxyFake{}
	handler := NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil,
		"api-gateway", "", true, proxy).Routes()

	request := httptest.NewRequest(http.MethodGet, "/api/admin/campaigns/campaign-1/deliveries?page=1", nil)
	trusted(request, "campaigns:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || proxy.path != "/priv/campaigns/campaign-1/deliveries?page=1" {
		t.Fatalf("status=%d path=%q", response.Code, proxy.path)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/campaigns/campaign-1/retry-failed", nil)
	trusted(request, "campaigns:send")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || proxy.path != "/priv/campaigns/campaign-1/retry-failed" {
		t.Fatalf("status=%d path=%q", response.Code, proxy.path)
	}
}

func TestCampaignWritesAndSendsUseSeparateScopes(t *testing.T) {
	proxy := &engagementProxyFake{}
	handler := NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil,
		"api-gateway", "", true, proxy).Routes()

	write := httptest.NewRequest(http.MethodPost, "/api/admin/campaigns", strings.NewReader(`{}`))
	trusted(write, "campaigns:write")
	written := httptest.NewRecorder()
	handler.ServeHTTP(written, write)
	if written.Code != http.StatusOK {
		t.Fatalf("write status=%d body=%s", written.Code, written.Body.String())
	}

	send := httptest.NewRequest(http.MethodPost, "/api/admin/campaigns/campaign-1/send", nil)
	trusted(send, "campaigns:write")
	denied := httptest.NewRecorder()
	handler.ServeHTTP(denied, send)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("send with write status=%d", denied.Code)
	}

	send = httptest.NewRequest(http.MethodPost, "/api/admin/campaigns/campaign-1/send", nil)
	trusted(send, "campaigns:send")
	allowed := httptest.NewRecorder()
	handler.ServeHTTP(allowed, send)
	if allowed.Code != http.StatusOK {
		t.Fatalf("send status=%d body=%s", allowed.Code, allowed.Body.String())
	}
}

func TestEnabledCampaignScheduleRequiresSendScope(t *testing.T) {
	proxy := &engagementProxyFake{}
	handler := NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil,
		"api-gateway", "", true, proxy).Routes()

	for _, test := range []struct {
		name       string
		method     string
		body       string
		scopes     string
		wantStatus int
	}{
		{name: "create without send", method: http.MethodPost, body: `{}`, scopes: "campaigns:write", wantStatus: http.StatusForbidden},
		{name: "create with send", method: http.MethodPost, body: `{}`, scopes: "campaigns:write campaigns:send", wantStatus: http.StatusOK},
		{name: "disabled update", method: http.MethodPut, body: `{"enabled":false}`, scopes: "campaigns:write", wantStatus: http.StatusOK},
		{name: "enabled update without send", method: http.MethodPut, body: `{"enabled":true}`, scopes: "campaigns:write", wantStatus: http.StatusForbidden},
		{name: "enabled update with send", method: http.MethodPut, body: `{"enabled":true}`, scopes: "campaigns:write campaigns:send", wantStatus: http.StatusOK},
		{name: "removed cms writer", method: http.MethodPut, body: `{"enabled":true}`, scopes: "cms:bulletins:write", wantStatus: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := "/api/admin/campaign-schedules"
			if test.method == http.MethodPut {
				path += "/schedule-1"
			}
			request := httptest.NewRequest(test.method, path, strings.NewReader(test.body))
			trusted(request, test.scopes)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestAdminRoutesRequireTrustedIdentityAndScope(t *testing.T) {
	handler := testHandler(&apiRepository{})
	request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletins", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/api/admin/bulletins", nil)
	trusted(request, "cms:bulletins:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/bulletins", nil)
	trusted(request, "cms:bulletins:read")
	request.Header.Set("Dapr-Caller-App-Id", "untrusted-service")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("spoofed gateway status=%d", response.Code)
	}
}

func TestLivenessRouteDoesNotRequireDependencies(t *testing.T) {
	response := httptest.NewRecorder()
	testHandler(&apiRepository{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAdminRoutesRequireDaprAPITokenWhenConfigured(t *testing.T) {
	handler := NewWithContent(
		bulletins.NewService(&apiRepository{}, time.Now),
		nil,
		nil,
		nil,
		"api-gateway",
		"sidecar-token",
		false,
	).Routes()
	request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletins", nil)
	trusted(request, "cms:bulletins:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/bulletins", nil)
	trusted(request, "cms:bulletins:read")
	request.Header.Set("dapr-api-token", "sidecar-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid token status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRequestsAreLoggedWithStatusAndRequestID(t *testing.T) {
	var output bytes.Buffer
	previous := logging.Access
	logging.Access = slog.New(slog.NewJSONHandler(&output, nil))
	t.Cleanup(func() { logging.Access = previous })

	handler := testHandler(&apiRepository{})
	request := httptest.NewRequest(http.MethodGet, "/missing", nil)
	request.Header.Set("X-HHC-Request-ID", "request-1")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	logged := output.String()
	if !strings.Contains(logged, `"status":404`) || !strings.Contains(logged, `"request_id":"request-1"`) {
		t.Fatalf("unexpected log: %s", logged)
	}
}

func TestCreateIssueRequiresIdempotencyAndReturnsETag(t *testing.T) {
	repo := &apiRepository{}
	handler := testHandler(repo)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins", bytes.NewBufferString(`{"issueNumber":1732,"issueDate":"2026-07-12"}`))
	trusted(request, "cms:bulletins:write")
	request.Header.Set("Idempotency-Key", "create-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("ETag") != `"1"` {
		t.Fatalf("etag=%s", response.Header().Get("ETag"))
	}
	if repo.actor != "user-1" || repo.idempotency != "create-1" {
		t.Fatalf("actor=%s idempotency=%s", repo.actor, repo.idempotency)
	}
}

func TestMutationRequiresIfMatch(t *testing.T) {
	handler := testHandler(&apiRepository{})
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/publish", bytes.NewBufferString(`{"locale":"zh-Hant"}`))
	trusted(request, "cms:bulletins:publish")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestPublishForwardsNotificationIntent(t *testing.T) {
	repo := &apiRepository{}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/publish", bytes.NewBufferString(`{"locale":"zh-Hant","notifySubscribers":true}`))
	trusted(request, "cms:bulletins:publish")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	testHandler(repo).ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !repo.notifySubscribers {
		t.Fatalf("status=%d notify=%v body=%s", response.Code, repo.notifySubscribers, response.Body.String())
	}
}

func TestPublishDefaultsNotificationIntentOff(t *testing.T) {
	repo := &apiRepository{}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/publish", bytes.NewBufferString(`{"locale":"zh-Hant"}`))
	trusted(request, "cms:bulletins:publish")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	testHandler(repo).ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || repo.notifySubscribers {
		t.Fatalf("status=%d notify=%v body=%s", response.Code, repo.notifySubscribers, response.Body.String())
	}
}

func TestAutomaticUploadRequiresPublisherBeforeAssetMutation(t *testing.T) {
	for _, publisher := range []bool{false, true} {
		t.Run(map[bool]string{false: "writer", true: "publisher"}[publisher], func(t *testing.T) {
			repo := &apiRepository{issue: bulletinIssue()}
			uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant", OriginalFileName: "weekly.pdf"}}
			request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/assets/asset-1/complete", strings.NewReader(`{"locale":"zh-Hant","title":"週報","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","autoPublish":true,"notifySubscribers":true}`))
			scope := "cms:bulletins:write"
			if publisher {
				scope += " cms:bulletins:publish"
			}
			trusted(request, scope)
			request.Header.Set("If-Match", `"1"`)
			response := httptest.NewRecorder()
			testHandlerWithUploads(repo, uploads).ServeHTTP(response, request)
			if !publisher {
				if response.Code != 403 || uploads.completeCalls != 0 || uploads.getCalls != 0 || repo.put.AutoPublish {
					t.Fatal("writer acquired publication authority", response.Code)
				}
				return
			}
			if response.Code != 200 || !repo.put.AutoPublish || !repo.put.NotifySubscribers {
				t.Fatal("publisher intent lost", response.Code, response.Body.String())
			}
		})
	}
}

func TestDeleteIssueRequiresWriteScopeAndVersion(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue()}
	handler := testHandler(repo)

	request := httptest.NewRequest(http.MethodDelete, "/api/admin/bulletins/issue-1", nil)
	trusted(request, "cms:bulletins:read")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("read-only status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/admin/bulletins/issue-1", nil)
	trusted(request, "cms:bulletins:write")
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || !repo.deleted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBulletinVersionUpdateAndDeleteUseIssueVersion(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue()}
	handler := testHandler(repo)
	request := httptest.NewRequest(http.MethodPut, "/api/admin/bulletins/issue-1/versions/en", bytes.NewBufferString(`{"title":"Weekly"}`))
	trusted(request, "cms:bulletins:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("update status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/admin/bulletins/issue-1/versions/en", nil)
	trusted(request, "cms:bulletins:write")
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || repo.deletedLocale != "en" {
		t.Fatalf("delete status=%d locale=%q body=%s", response.Code, repo.deletedLocale, response.Body.String())
	}
}

func TestBulletinRevisionRoutesListAndRestoreDraft(t *testing.T) {
	repo := &apiRepository{
		issue: bulletinIssue(),
		revisions: []bulletins.Revision{{
			Version: 1,
			Snapshot: bulletins.Issue{ID: "issue-1", IssueDate: "2026-07-13", Versions: []bulletins.Version{{
				ID: "version-1", IssueID: "issue-1", Locale: "zh-Hant", Title: "舊週報", PDFAssetID: "asset-1", PDFFileName: "weekly.pdf",
			}}},
		}},
	}
	uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant"}}
	handler := testHandlerWithUploads(repo, uploads)

	request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletins/issue-1/revisions", nil)
	trusted(request, "cms:bulletins:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var listed envelope
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/revisions/1/restore", nil)
	trusted(request, "cms:bulletins:write")
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("restore status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if repo.restoredRevision != 1 || repo.issue.Status != "draft" {
		t.Fatalf("repo=%#v", repo)
	}
}

func TestBulletinRevisionRestoreRejectsMissingAssetBeforeMutation(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue(), revisions: []bulletins.Revision{{
		Version:  1,
		Snapshot: bulletins.Issue{ID: "issue-1", Versions: []bulletins.Version{{IssueID: "issue-1", Locale: "zh-Hant", PDFAssetID: "missing-asset"}}},
	}}}
	uploads := &apiUploads{getError: assetclient.ErrNotFound}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/revisions/1/restore", nil)
	trusted(request, "cms:bulletins:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	testHandlerWithUploads(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || repo.restoredRevision != 0 {
		t.Fatalf("status=%d restored=%d body=%s", response.Code, repo.restoredRevision, response.Body.String())
	}
}

func TestBulletinRevisionRestoreRejectsUnsupportedEditionBeforeAssetLookup(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue(), revisions: []bulletins.Revision{{
		Version:  1,
		Snapshot: bulletins.Issue{ID: "issue-1", Versions: []bulletins.Version{{IssueID: "issue-1", Locale: "ja", PDFAssetID: "asset-ja"}}},
	}}}
	uploads := &apiUploads{}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/revisions/1/restore", nil)
	trusted(request, "cms:bulletins:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	testHandlerWithUploads(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || uploads.getCalls != 0 || repo.restoredRevision != 0 {
		t.Fatalf("status=%d get=%d restored=%d body=%s", response.Code, uploads.getCalls, repo.restoredRevision, response.Body.String())
	}
}

func TestBulletinRevisionRestoreMapsStaleVersion(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue(), revisions: []bulletins.Revision{{Version: 1, Snapshot: bulletins.Issue{ID: "issue-1"}}}, restoreError: bulletins.ErrPrecondition}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/revisions/1/restore", nil)
	trusted(request, "cms:bulletins:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	testHandlerWithUploads(repo, &apiUploads{}).ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBulletinUploadSessionRequiresBulletinWriteWithoutAssetPermission(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue()}
	uploads := &apiUploads{}
	handler := testHandlerWithUploads(repo, uploads)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/upload-sessions", bytes.NewBufferString(`{"locale":"zh-Hant","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128}`))
	trusted(request, "assets:write")
	request.Header.Set("Idempotency-Key", "upload-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/upload-sessions", bytes.NewBufferString(`{"locale":"zh-Hant","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128}`))
	trusted(request, "cms:bulletins:write")
	request.Header.Set("Idempotency-Key", "upload-1")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if uploads.createdIssue != "issue-1" || uploads.createdLocale != "zh-Hant" {
		t.Fatalf("upload issue=%q locale=%q", uploads.createdIssue, uploads.createdLocale)
	}
}

func TestBulletinUploadCreateAndCompletionRejectUnsupportedEditionBeforeAssetCalls(t *testing.T) {
	for _, locale := range []string{"ja", "ko"} {
		t.Run(locale, func(t *testing.T) {
			repo := &apiRepository{issue: bulletinIssue()}
			uploads := &apiUploads{}
			handler := testHandlerWithUploads(repo, uploads)

			create := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/upload-sessions", bytes.NewBufferString(`{"locale":"`+locale+`","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128}`))
			trusted(create, "cms:bulletins:write")
			create.Header.Set("Idempotency-Key", "upload-1")
			createResponse := httptest.NewRecorder()
			handler.ServeHTTP(createResponse, create)

			complete := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/assets/asset-1/complete", bytes.NewBufferString(`{"locale":"`+locale+`","title":"Weekly","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
			trusted(complete, "cms:bulletins:write")
			complete.Header.Set("If-Match", `"1"`)
			completeResponse := httptest.NewRecorder()
			handler.ServeHTTP(completeResponse, complete)

			if createResponse.Code != http.StatusBadRequest || completeResponse.Code != http.StatusBadRequest || uploads.createdLocale != "" || uploads.getCalls != 0 || uploads.completeCalls != 0 || repo.put.Locale != "" {
				t.Fatalf("create=%d complete=%d created=%q get=%d completed=%d put=%#v", createResponse.Code, completeResponse.Code, uploads.createdLocale, uploads.getCalls, uploads.completeCalls, repo.put)
			}
		})
	}
}

func TestCompleteBulletinUploadAttachesOwnedAsset(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	repo := &apiRepository{issue: bulletinIssue()}
	uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant", OriginalFileName: "weekly.pdf"}}
	handler := testHandlerWithUploads(repo, uploads)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/assets/asset-1/complete", bytes.NewBufferString(`{"locale":"zh-Hant","title":"週報","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:bulletins:write")
	request.Header.Set("X-HHC-Request-ID", "request-upload-1")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.put.PDFAssetID != "asset-1" || repo.put.Locale != "zh-Hant" || uploads.serviceGrantCalls != 1 {
		t.Fatalf("put=%#v serviceGrantCalls=%d", repo.put, uploads.serviceGrantCalls)
	}
	logged := output.String()
	if !strings.Contains(logged, `"asset_id":"asset-1"`) || !strings.Contains(logged, `"request_id":"request-upload-1"`) || !strings.Contains(logged, `"resource_id":"issue-1"`) {
		t.Fatalf("missing asset correlation log: %s", logged)
	}
}

func TestBulletinAssetStatusReturnsOnlyOwnedAsset(t *testing.T) {
	uploads := &apiUploads{completed: assetclient.Asset{
		ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api",
		OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant",
		UploadStatus: "completed", ScanStatus: "pending", ProcessingStatus: "pending",
	}}
	handler := testHandlerWithUploads(&apiRepository{issue: bulletinIssue()}, uploads)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletins/issue-1/assets/asset-1", nil)
	trusted(request, "cms:bulletins:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"scanStatus":"pending"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "ownerService") || !strings.Contains(response.Body.String(), `"retryable":false`) {
		t.Fatalf("unsafe status body=%s", response.Body.String())
	}

	uploads.completed.OwnerID = "another-issue"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("owner mismatch status=%d body=%s", response.Code, response.Body.String())
	}

	uploads.getError = assetclient.ErrUnavailable
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestBulletinFailedAssetCanBeRetriedByAssetWriter(t *testing.T) {
	uploads := &apiUploads{completed: assetclient.Asset{
		ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api",
		OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant",
		UploadStatus: "completed", ScanStatus: "failed", ProcessingStatus: "not_required",
	}}
	handler := testHandlerWithUploads(&apiRepository{issue: bulletinIssue()}, uploads)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/assets/asset-1/scan/retry", nil)
	trusted(request, "cms:bulletins:write")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || uploads.requeueCalls != 1 || !strings.Contains(response.Body.String(), `"scanStatus":"pending"`) {
		t.Fatalf("status=%d requeues=%d body=%s", response.Code, uploads.requeueCalls, response.Body.String())
	}
}

func TestCompleteBulletinUploadRejectsOwnerBeforeMutation(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue()}
	uploads := &apiUploads{completed: assetclient.Asset{
		ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api",
		OwnerType: "bulletin_issue", OwnerID: "another-issue", Locale: "zh-Hant",
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/assets/asset-1/complete", bytes.NewBufferString(`{"locale":"zh-Hant","title":"週報","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:bulletins:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	testHandlerWithUploads(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || uploads.completeCalls != 0 {
		t.Fatalf("status=%d completeCalls=%d", response.Code, uploads.completeCalls)
	}
}

func TestCompleteBulletinUploadReportsAssetServiceUnavailable(t *testing.T) {
	uploads := &apiUploads{getError: assetclient.ErrUnavailable}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/assets/asset-1/complete", bytes.NewBufferString(`{"locale":"zh-Hant","title":"週報","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:bulletins:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	testHandlerWithUploads(&apiRepository{issue: bulletinIssue()}, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || uploads.completeCalls != 0 {
		t.Fatalf("status=%d completeCalls=%d", response.Code, uploads.completeCalls)
	}
}

func testHandler(repo bulletins.Repository) http.Handler {
	return testHandlerWithUploads(repo, nil)
}
func testHandlerWithUploads(repo bulletins.Repository, uploads assetUploads) http.Handler {
	return New(bulletins.NewService(repo, time.Now), nil, uploads).Routes()
}
func trusted(request *http.Request, scopes string) {
	request.Header.Set("Dapr-Caller-App-Id", "api-gateway")
	request.Header.Set("X-HHC-User-ID", "user-1")
	request.Header.Set("X-HHC-Auth-Provider", "account-api")
	request.Header.Set("X-HHC-Scopes", scopes)
	request.Header.Set("X-HHC-Request-ID", "request-1")
}

type apiRepository struct {
	actor, idempotency string
	issue              bulletins.Issue
	put                bulletins.PutVersionInput
	revisions          []bulletins.Revision
	restoredRevision   int64
	restoreError       error
	deleted            bool
	deletedLocale      string
	notifySubscribers  bool
}

type notificationAPIRepository struct {
	*apiRepository
	submitted bool
}

func (*notificationAPIRepository) BulletinNotificationState(context.Context, string, string) (*int, *time.Time, error) {
	generation := 0
	now := time.Now()
	return &generation, &now, nil
}

func (r *notificationAPIRepository) SubmitBulletinNotification(_ context.Context, issueID, series, _, _ string, _ time.Time) (bulletins.NotificationSubmissionResult, error) {
	r.submitted = true
	return bulletins.NotificationSubmissionResult{IssueID: issueID, Series: series, NotificationGeneration: 1}, nil
}

func (r *apiRepository) CreateIssue(_ context.Context, number int, date, actor, key string, now time.Time) (bulletins.Issue, error) {
	r.actor = actor
	r.idempotency = key
	return bulletins.Issue{ID: "issue-1", IssueNumber: &number, IssueDate: date, Status: "draft", Version: 1, CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now, Versions: []bulletins.Version{}}, nil
}
func (*apiRepository) ListIssues(context.Context, int, int, string, string, string, string) (bulletins.Page, error) {
	return bulletins.Page{Items: []bulletins.Issue{}, Page: 1, PageSize: 20}, nil
}
func (r *apiRepository) GetIssue(context.Context, string) (bulletins.Issue, error) {
	if r.issue.ID == "" {
		return bulletins.Issue{}, bulletins.ErrNotFound
	}
	return r.issue, nil
}
func (r *apiRepository) UpdateIssue(_ context.Context, _ string, _ int64, input bulletins.UpdateIssueInput, _ string, _ time.Time) (bulletins.Issue, error) {
	r.issue = bulletinIssue()
	r.issue.IssueNumber = &input.IssueNumber
	r.issue.IssueDate = input.IssueDate
	r.issue.Version++
	return r.issue, nil
}
func (r *apiRepository) PutVersion(_ context.Context, _ string, _ int64, input bulletins.PutVersionInput, _ string, _ time.Time) (bulletins.Issue, error) {
	r.put = input
	r.issue.Version++
	return r.issue, nil
}

func bulletinIssue() bulletins.Issue {
	return bulletins.Issue{ID: "issue-1", IssueDate: "2026-07-13", Status: "draft", Version: 1, Versions: []bulletins.Version{}}
}

type apiUploads struct {
	createdIssue, createdLocale string
	createdPurpose              string
	createdHomeID               string
	completed                   assetclient.Asset
	completeCalls               int
	serviceGrantCalls           int
	getError                    error
	getCalls                    int
	requeueCalls                int
}

func (u *apiUploads) CreateBulletinUpload(_ context.Context, issueID, locale, fileName, mimeType string, sizeBytes int64, key string) (assetclient.CreatedUpload, error) {
	u.createdIssue = issueID
	u.createdLocale = locale
	return assetclient.CreatedUpload{Asset: assetclient.Asset{ID: "asset-1"}, UploadTarget: assetclient.UploadTarget{URL: "http://example.test/upload", Method: http.MethodPut}}, nil
}
func (u *apiUploads) CreateNewsCoverUpload(_ context.Context, newsID, purpose, fileName, mimeType string, sizeBytes int64, key string) (assetclient.CreatedUpload, error) {
	u.createdPurpose = purpose
	return assetclient.CreatedUpload{Asset: assetclient.Asset{ID: "news-asset", OwnerID: newsID}, UploadTarget: assetclient.UploadTarget{URL: "http://example.test/upload", Method: http.MethodPut}}, nil
}
func (u *apiUploads) CreateHomeBannerUpload(_ context.Context, homeID, fileName, mimeType string, sizeBytes int64, key string) (assetclient.CreatedUpload, error) {
	u.createdHomeID = homeID
	return assetclient.CreatedUpload{Asset: assetclient.Asset{ID: "banner-1", OwnerID: homeID}, UploadTarget: assetclient.UploadTarget{URL: "http://example.test/upload", Method: http.MethodPut}}, nil
}
func (u *apiUploads) CompleteUpload(context.Context, string, assetclient.CompleteUploadInput) (assetclient.Asset, error) {
	u.completeCalls++
	return u.completed, nil
}
func (u *apiUploads) CreateServiceGrant(context.Context, string, string) (assetclient.Grant, error) {
	u.serviceGrantCalls++
	return assetclient.Grant{ID: "service-grant"}, nil
}
func (u *apiUploads) Get(context.Context, string) (assetclient.Asset, error) {
	u.getCalls++
	return u.completed, u.getError
}
func (u *apiUploads) RequeueScan(context.Context, string) error {
	u.requeueCalls++
	return nil
}
func (r *apiRepository) StartPublish(_ context.Context, _ string, _ string, _ string, _ int64, notifySubscribers bool, _ string, _ time.Time) (bulletins.Workflow, error) {
	r.notifySubscribers = notifySubscribers
	return bulletins.Workflow{}, nil
}
func (*apiRepository) Unpublish(context.Context, string, string, string, int64, string, time.Time) (bulletins.Issue, error) {
	return bulletins.Issue{}, nil
}
func (r *apiRepository) DeleteIssue(context.Context, string, int64, string, time.Time) error {
	r.deleted = true
	return nil
}
func (r *apiRepository) UpdateVersion(_ context.Context, _ string, _ string, locale string, _ int64, title, subtitle, _ string, _ time.Time) (bulletins.Issue, error) {
	r.issue = bulletinIssue()
	r.issue.Version++
	r.issue.Versions = []bulletins.Version{{Locale: locale, Title: title, Subtitle: subtitle}}
	return r.issue, nil
}
func (r *apiRepository) DeleteVersion(_ context.Context, _ string, _ string, locale string, _ int64, _ string, _ time.Time) (bulletins.Issue, error) {
	r.issue = bulletinIssue()
	r.issue.Version++
	r.issue.Versions = nil
	r.deletedLocale = locale
	return r.issue, nil
}
func (r *apiRepository) IssueRevisions(context.Context, string) ([]bulletins.Revision, error) {
	return r.revisions, nil
}
func (r *apiRepository) RestoreIssueRevision(_ context.Context, _ string, revision, _ int64, actor string, _ time.Time) (bulletins.Issue, error) {
	r.restoredRevision = revision
	if r.restoreError != nil {
		return bulletins.Issue{}, r.restoreError
	}
	r.issue.Status = "draft"
	r.issue.Version++
	r.issue.UpdatedBy = actor
	return r.issue, nil
}

func TestRecordingSearchAccessLogOmitsQueryWords(t *testing.T) {
	var output bytes.Buffer
	previous := logging.Access
	logging.Access = slog.New(slog.NewJSONHandler(&output, nil))
	t.Cleanup(func() { logging.Access = previous })
	handler := testHandler(&apiRepository{})
	for _, path := range []string{"/api/member/recordings?limit=12&q=search-private-sentinel", "/api/member/recordings/live?q=search-private-sentinel"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	if strings.Contains(output.String(), "search-private-sentinel") || strings.Contains(output.String(), "?q=") {
		t.Fatal("search query exposed in access log")
	}
}
