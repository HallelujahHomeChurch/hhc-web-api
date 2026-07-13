package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

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
}

func TestCreateIssueRequiresIdempotencyAndReturnsETag(t *testing.T) {
	repo := &apiRepository{}
	handler := testHandler(repo)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins", bytes.NewBufferString(`{"issueDate":"2026-07-12"}`))
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
	request := httptest.NewRequest(http.MethodPost, "/api/admin/bulletins/issue-1/versions", bytes.NewBufferString(`{"locale":"zh-Hant","title":"週報","pdfAssetId":"asset-1","pdfFileName":"weekly.pdf"}`))
	trusted(request, "cms:write")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatalf("status=%d", response.Code)
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

func testHandler(repo bulletins.Repository) http.Handler {
	return testHandlerWithUploads(repo, nil)
}
func testHandlerWithUploads(repo bulletins.Repository, uploads assetUploads) http.Handler {
	return New(bulletins.NewService(repo, time.Now), nil, uploads).Routes()
}
func trusted(request *http.Request, scopes string) {
	request.Header.Set("X-HHC-User-ID", "user-1")
	request.Header.Set("X-HHC-Auth-Provider", "account-api")
	request.Header.Set("X-HHC-Scopes", scopes)
}

type apiRepository struct {
	actor, idempotency string
	public             bulletins.PublicBulletin
	issue              bulletins.Issue
	put                bulletins.PutVersionInput
}

func (r *apiRepository) CreateIssue(_ context.Context, date, actor, key string, now time.Time) (bulletins.Issue, error) {
	r.actor = actor
	r.idempotency = key
	return bulletins.Issue{ID: "issue-1", IssueDate: date, Status: "draft", Version: 1, CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now, Versions: []bulletins.Version{}}, nil
}
func (*apiRepository) ListIssues(context.Context, int, int, string) (bulletins.Page, error) {
	return bulletins.Page{Items: []bulletins.Issue{}, Page: 1, PageSize: 20}, nil
}
func (r *apiRepository) GetIssue(context.Context, string) (bulletins.Issue, error) {
	if r.issue.ID == "" {
		return bulletins.Issue{}, bulletins.ErrNotFound
	}
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
	completed                   assetclient.Asset
}

func (u *apiUploads) CreateBulletinUpload(_ context.Context, issueID, locale, fileName, mimeType string, sizeBytes int64, key string) (assetclient.CreatedUpload, error) {
	u.createdIssue = issueID
	u.createdLocale = locale
	return assetclient.CreatedUpload{Asset: assetclient.Asset{ID: "asset-1"}, UploadTarget: assetclient.UploadTarget{URL: "http://example.test/upload", Method: http.MethodPut}}, nil
}
func (u *apiUploads) CreateNewsCoverUpload(_ context.Context, newsID, fileName, mimeType string, sizeBytes int64, key string) (assetclient.CreatedUpload, error) {
	return assetclient.CreatedUpload{Asset: assetclient.Asset{ID: "news-asset", OwnerID: newsID}, UploadTarget: assetclient.UploadTarget{URL: "http://example.test/upload", Method: http.MethodPut}}, nil
}
func (u *apiUploads) CompleteUpload(context.Context, string, assetclient.CompleteUploadInput) (assetclient.Asset, error) {
	return u.completed, nil
}
func (u *apiUploads) Get(context.Context, string) (assetclient.Asset, error) { return u.completed, nil }
func (*apiUploads) CreatePublicGrant(context.Context, string, string) (assetclient.Grant, error) {
	return assetclient.Grant{ID: "grant-1"}, nil
}
func (*apiUploads) RevokeGrant(context.Context, string, string) error { return nil }
func (*apiRepository) StartPublish(context.Context, string, string, int64, string, time.Time) (bulletins.Workflow, error) {
	return bulletins.Workflow{}, nil
}
func (*apiRepository) Unpublish(context.Context, string, string, int64, string, time.Time) (bulletins.Issue, error) {
	return bulletins.Issue{}, nil
}
func (r *apiRepository) GetPublicLatest(context.Context, string) (bulletins.PublicBulletin, error) {
	return r.public, nil
}
func (r *apiRepository) GetPublicByDate(context.Context, string, string) (bulletins.PublicBulletin, error) {
	return r.public, nil
}
func (r *apiRepository) ListPublic(context.Context, string, int, int) (bulletins.PublicPage, error) {
	return bulletins.PublicPage{Items: []bulletins.PublicBulletin{r.public}, Page: 1, PageSize: 20, Total: 1}, nil
}
