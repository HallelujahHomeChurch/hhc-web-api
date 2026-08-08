package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

type engagementProxyFake struct {
	path  string
	actor string
}

func (f *engagementProxyFake) Forward(_ context.Context, _ string, path string, _ io.Reader, actor string) (*http.Response, error) {
	f.path, f.actor = path, actor
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
	}, nil
}

func TestCampaignProxyRequiresCMSScopeAndForwardsActor(t *testing.T) {
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
	trusted(request, "cms:read")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || proxy.path != "/priv/campaigns?q=August&page=2" || proxy.actor != "user-1" {
		t.Fatalf("status=%d path=%q actor=%q body=%s", response.Code, proxy.path, proxy.actor, response.Body.String())
	}
}

func TestCampaignDeliveryProxyRoutes(t *testing.T) {
	proxy := &engagementProxyFake{}
	handler := NewWithContent(bulletins.NewService(&apiRepository{}, time.Now), nil, nil, nil,
		"api-gateway", "", true, proxy).Routes()

	request := httptest.NewRequest(http.MethodGet, "/api/admin/campaigns/campaign-1/deliveries?page=1", nil)
	trusted(request, "cms:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || proxy.path != "/priv/campaigns/campaign-1/deliveries?page=1" {
		t.Fatalf("status=%d path=%q", response.Code, proxy.path)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/campaigns/campaign-1/retry-failed", nil)
	trusted(request, "cms:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || proxy.path != "/priv/campaigns/campaign-1/retry-failed" {
		t.Fatalf("status=%d path=%q", response.Code, proxy.path)
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
	trusted(request, "cms:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/bulletins", nil)
	trusted(request, "cms:read")
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
	trusted(request, "cms:read")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/bulletins", nil)
	trusted(request, "cms:read")
	request.Header.Set("dapr-api-token", "sidecar-token")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid token status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRequestsAreLoggedWithStatusAndRequestID(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

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
	trusted(request, "cms:write")
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
	trusted(request, "cms:publish")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestDeleteIssueRequiresWriteScopeAndVersion(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue()}
	handler := testHandler(repo)

	request := httptest.NewRequest(http.MethodDelete, "/api/admin/bulletins/issue-1", nil)
	trusted(request, "cms:read")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("read-only status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/admin/bulletins/issue-1", nil)
	trusted(request, "cms:write")
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
	trusted(request, "cms:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("update status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "/api/admin/bulletins/issue-1/versions/en", nil)
	trusted(request, "cms:write")
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
	trusted(request, "cms:read")
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
	trusted(request, "cms:write")
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
	trusted(request, "cms:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	testHandlerWithUploads(repo, uploads).ServeHTTP(response, request)
	if response.Code != http.StatusUnprocessableEntity || repo.restoredRevision != 0 {
		t.Fatalf("status=%d restored=%d body=%s", response.Code, repo.restoredRevision, response.Body.String())
	}
}

func TestBulletinRevisionRestoreMapsStaleVersion(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue(), revisions: []bulletins.Revision{{Version: 1, Snapshot: bulletins.Issue{ID: "issue-1"}}}, restoreError: bulletins.ErrPrecondition}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/revisions/1/restore", nil)
	trusted(request, "cms:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	testHandlerWithUploads(repo, &apiUploads{}).ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionFailed {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPublicLatestUsesPublishedProjection(t *testing.T) {
	repo := &apiRepository{public: bulletins.PublicBulletin{IssueDate: "2026-07-12", Locale: "zh-Hant", Title: "週報", DownloadURL: "/api/assets/public/asset-1", Version: 3}}
	handler := testHandler(repo)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/bulletins/latest?locale=zh-Hant", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	if response.Header().Get("Cache-Control") == "" {
		t.Fatal("missing cache policy")
	}
}

func TestPublicByNumberUsesCanonicalIssueNumberAndLocale(t *testing.T) {
	number := 1732
	repo := &apiRepository{public: bulletins.PublicBulletin{IssueNumber: &number, IssueDate: "2026-08-02", Locale: "zh-Hant", Title: "週報", DownloadURL: "/api/assets/public/asset-1", Version: 3}}
	handler := testHandler(repo)

	for _, test := range []struct {
		path   string
		locale string
	}{
		{path: "/api/bulletins/by-number/1732", locale: "zh-Hant"},
		{path: "/api/bulletins/by-number/1732?locale=en", locale: "en"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("path=%q status=%d body=%s", test.path, response.Code, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "public, max-age=60, stale-while-revalidate=300" || response.Header().Get("ETag") != `"bulletin-3"` {
			t.Fatalf("path=%q headers=%v", test.path, response.Header())
		}
		if repo.publicLocale != test.locale {
			t.Fatalf("path=%q locale=%q", test.path, repo.publicLocale)
		}
	}
	if repo.publicIssueNumber != 1732 {
		t.Fatalf("issueNumber=%d locale=%q", repo.publicIssueNumber, repo.publicLocale)
	}
}

func TestPublicByNumberRejectsNonCanonicalValues(t *testing.T) {
	handler := testHandler(&apiRepository{})
	for _, value := range []string{"0", "-1", "+1", "01", "2147483648", "1.0", "abc", "1%20"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/bulletins/by-number/"+value, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("value=%q status=%d body=%s", value, response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("value=%q body=%s", value, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("value=%q Cache-Control=%q", value, response.Header().Get("Cache-Control"))
		}
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/bulletins/by-number/1?locale=fr", nil))
	if response.Code != http.StatusBadRequest || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("invalid locale status=%d headers=%v", response.Code, response.Header())
	}
}

func TestPublicByNumberReturnsNotFoundWithoutFallingThroughToDateRoute(t *testing.T) {
	repo := &apiRepository{publicError: bulletins.ErrNotFound}
	response := httptest.NewRecorder()
	testHandler(repo).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/bulletins/by-number/1732?locale=en", nil))
	if response.Code != http.StatusNotFound || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"code":"not_found"`) {
		t.Fatalf("body=%s", response.Body.String())
	}
	if repo.publicIssueNumber != 1732 || repo.publicLocale != "en" {
		t.Fatalf("route fell through: issueNumber=%d locale=%q", repo.publicIssueNumber, repo.publicLocale)
	}
}

func TestBulletinUploadSessionRequiresBothCMSAndAssetScopes(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue()}
	uploads := &apiUploads{}
	handler := testHandlerWithUploads(repo, uploads)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/upload-sessions", bytes.NewBufferString(`{"locale":"zh-Hant","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128}`))
	trusted(request, "cms:write")
	request.Header.Set("Idempotency-Key", "upload-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/upload-sessions", bytes.NewBufferString(`{"locale":"zh-Hant","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128}`))
	trusted(request, "cms:write assets:write")
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

func TestCompleteBulletinUploadAttachesOwnedAsset(t *testing.T) {
	repo := &apiRepository{issue: bulletinIssue()}
	uploads := &apiUploads{completed: assetclient.Asset{ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant", OriginalFileName: "weekly.pdf"}}
	handler := testHandlerWithUploads(repo, uploads)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/assets/asset-1/complete", bytes.NewBufferString(`{"locale":"zh-Hant","title":"週報","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":128,"checksumSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`))
	trusted(request, "cms:write assets:write")
	request.Header.Set("If-Match", `"1"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if repo.put.PDFAssetID != "asset-1" || repo.put.Locale != "zh-Hant" {
		t.Fatalf("put = %#v", repo.put)
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
	trusted(request, "cms:read")
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
	trusted(request, "cms:write assets:write")
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
	trusted(request, "cms:write assets:write")
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
	trusted(request, "cms:write assets:write")
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
}

type apiRepository struct {
	actor, idempotency string
	public             bulletins.PublicBulletin
	issue              bulletins.Issue
	put                bulletins.PutVersionInput
	revisions          []bulletins.Revision
	restoredRevision   int64
	restoreError       error
	deleted            bool
	deletedLocale      string
	publicIssueNumber  int
	publicLocale       string
	publicError        error
}

func (r *apiRepository) CreateIssue(_ context.Context, number int, date, actor, key string, now time.Time) (bulletins.Issue, error) {
	r.actor = actor
	r.idempotency = key
	return bulletins.Issue{ID: "issue-1", IssueNumber: &number, IssueDate: date, Status: "draft", Version: 1, CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now, Versions: []bulletins.Version{}}, nil
}
func (*apiRepository) ListIssues(context.Context, int, int, string, string) (bulletins.Page, error) {
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
	completed                   assetclient.Asset
	completeCalls               int
	getError                    error
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
func (u *apiUploads) CompleteUpload(context.Context, string, assetclient.CompleteUploadInput) (assetclient.Asset, error) {
	u.completeCalls++
	return u.completed, nil
}
func (u *apiUploads) Get(context.Context, string) (assetclient.Asset, error) {
	return u.completed, u.getError
}
func (u *apiUploads) RequeueScan(context.Context, string) error {
	u.requeueCalls++
	return nil
}
func (*apiRepository) StartPublish(context.Context, string, string, int64, string, time.Time) (bulletins.Workflow, error) {
	return bulletins.Workflow{}, nil
}
func (*apiRepository) Unpublish(context.Context, string, string, int64, string, time.Time) (bulletins.Issue, error) {
	return bulletins.Issue{}, nil
}
func (r *apiRepository) DeleteIssue(context.Context, string, int64, string, time.Time) error {
	r.deleted = true
	return nil
}
func (r *apiRepository) UpdateVersion(_ context.Context, _ string, locale string, _ int64, title, subtitle, _ string, _ time.Time) (bulletins.Issue, error) {
	r.issue = bulletinIssue()
	r.issue.Version++
	r.issue.Versions = []bulletins.Version{{Locale: locale, Title: title, Subtitle: subtitle}}
	return r.issue, nil
}
func (r *apiRepository) DeleteVersion(_ context.Context, _ string, locale string, _ int64, _ string, _ time.Time) (bulletins.Issue, error) {
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
func (r *apiRepository) GetPublicLatest(context.Context, string) (bulletins.PublicBulletin, error) {
	return r.public, nil
}
func (r *apiRepository) GetPublicByDate(context.Context, string, string) (bulletins.PublicBulletin, error) {
	return r.public, nil
}
func (r *apiRepository) GetPublicByNumber(_ context.Context, issueNumber int, locale string) (bulletins.PublicBulletin, error) {
	r.publicIssueNumber, r.publicLocale = issueNumber, locale
	return r.public, r.publicError
}
func (r *apiRepository) ListPublic(context.Context, int, int) (bulletins.PublicPage, error) {
	return bulletins.PublicPage{Items: []bulletins.PublicIssue{{IssueDate: r.public.IssueDate, Versions: []bulletins.PublicBulletin{r.public}}}, Page: 1, PageSize: 20, Total: 1}, nil
}
