package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
)

type downloadJobStore struct {
	*watermarkStoreStub
	job         bulletinwatermark.DownloadJob
	request     bulletinwatermark.DownloadJobRequest
	createCalls int
	getCalls    int
}

func (s *downloadJobStore) CreateDownloadJob(_ context.Context, request bulletinwatermark.DownloadJobRequest) (bulletinwatermark.DownloadJob, error) {
	s.createCalls++
	s.request = request
	if s.job.ID == "" {
		s.job = bulletinwatermark.DownloadJob{ID: request.ID, AccountID: request.AccountID, IssueID: request.Source.IssueID, IssueDate: request.IssueDate, Locale: request.Source.Locale, Revision: request.Source.Revision, SourceAssetID: request.Source.AssetID, SourceSHA256: request.SourceSHA256, Filename: request.Source.Filename, Status: "queued", Progress: 5, CreatedAt: request.Now, UpdatedAt: request.Now, ExpiresAt: request.Now.Add(time.Hour)}
	}
	return s.job, nil
}

func (s *downloadJobStore) GetDownloadJob(_ context.Context, id, account string, _ time.Time) (bulletinwatermark.DownloadJob, error) {
	s.getCalls++
	if id != s.job.ID || account != s.job.AccountID {
		return bulletinwatermark.DownloadJob{}, bulletinwatermark.ErrNotFound
	}
	return s.job, nil
}

type downloadJobUploads struct {
	apiUploads
	assets map[string]assetclient.Asset
}

func (u *downloadJobUploads) Get(_ context.Context, id string) (assetclient.Asset, error) {
	u.getCalls++
	asset, ok := u.assets[id]
	if !ok {
		return assetclient.Asset{}, assetclient.ErrNotFound
	}
	return asset, nil
}

func TestCreateBulletinDownloadJobRequiresEntitlementBeforeDataReads(t *testing.T) {
	fixture := newDownloadJobFixture(t)
	request := downloadJobRequest(http.MethodPost, "/api/member/bulletin-download-jobs?series=general&locale=zh-Hant", fixture.userID, `{"issueId":"`+fixture.item.IssueID+`"}`)
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	fixture.handler("deny").ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || fixture.repository.entitlementReads != 1 || fixture.repository.dataReads != 0 || fixture.store.createCalls != 0 || fixture.store.getCalls != 0 || fixture.uploads.getCalls != 0 {
		t.Fatalf("status=%d entitlement=%d data=%d create=%d get=%d asset=%d body=%s", response.Code, fixture.repository.entitlementReads, fixture.repository.dataReads, fixture.store.createCalls, fixture.store.getCalls, fixture.uploads.getCalls, response.Body.String())
	}
}

func TestCreateBulletinDownloadJobUsesIssueIDExactLocaleAndIdempotencyKey(t *testing.T) {
	fixture := newDownloadJobFixture(t)
	key := uuid.NewString()
	request := downloadJobRequest(http.MethodPost, "/api/member/bulletin-download-jobs?series=general&locale=zh-Hant", fixture.userID, `{"issueId":"`+fixture.item.IssueID+`"}`)
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	fixture.handler("allow").ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || response.Header().Get("Retry-After") != "2" || fixture.store.createCalls != 1 || fixture.repository.dataReads != 1 || fixture.uploads.getCalls != 1 {
		t.Fatalf("status=%d headers=%v reads=%d asset=%d body=%s", response.Code, response.Header(), fixture.repository.dataReads, fixture.uploads.getCalls, response.Body.String())
	}
	if fixture.store.request.IdempotencyKey != key || fixture.store.request.AccountID != fixture.userID || fixture.store.request.Source.IssueID != fixture.item.IssueID || fixture.store.request.Source.Locale != "zh-Hant" || fixture.store.request.Source.Revision != fixture.item.Version || fixture.store.request.Source.AssetID != fixture.item.ProtectedAssetID || fixture.store.request.SourceSHA256 != strings.Repeat("b", 64) {
		t.Fatalf("request=%+v", fixture.store.request)
	}
	var body struct {
		Data bulletinwatermark.DownloadJobView `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Data.OperationProgress.Status != "queued" || body.Data.OperationProgress.Percent != 5 {
		t.Fatalf("body=%s err=%v", response.Body.String(), err)
	}
}

func TestCreateBulletinDownloadJobReturnsReadyCacheHit(t *testing.T) {
	fixture := newDownloadJobFixture(t)
	now := time.Now().UTC()
	fixture.store.job = fixture.job("ready")
	fixture.store.job.CacheExpiresAt = now.Add(time.Hour)
	request := downloadJobRequest(http.MethodPost, "/api/member/bulletin-download-jobs?series=general&locale=zh-Hant", fixture.userID, `{"issueId":"`+fixture.item.IssueID+`"}`)
	request.Header.Set("Idempotency-Key", uuid.NewString())
	response := httptest.NewRecorder()
	fixture.handler("allow").ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Retry-After") != "" || !strings.Contains(response.Body.String(), `"status":"ready"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestGetBulletinDownloadJobRejectsDifferentOwner(t *testing.T) {
	fixture := newDownloadJobFixture(t)
	fixture.store.job = fixture.job("queued")
	response := httptest.NewRecorder()
	fixture.handler("allow").ServeHTTP(response, downloadJobRequest(http.MethodGet, "/api/member/bulletin-download-jobs/"+fixture.store.job.ID+"?series=general&locale=zh-Hant", uuid.NewString(), ""))
	if response.Code != http.StatusNotFound || fixture.store.getCalls != 1 || fixture.repository.dataReads != 0 {
		t.Fatalf("status=%d get=%d reads=%d body=%s", response.Code, fixture.store.getCalls, fixture.repository.dataReads, response.Body.String())
	}
}

func TestGetBulletinDownloadJobRejectsChangedOrUnpublishedSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*downloadJobFixture)
	}{
		{name: "changed", mutate: func(f *downloadJobFixture) { f.repository.item.Version++ }},
		{name: "unpublished", mutate: func(f *downloadJobFixture) { f.repository.item = bulletins.ProtectedBulletin{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDownloadJobFixture(t)
			fixture.store.job = fixture.job("running")
			test.mutate(fixture)
			response := httptest.NewRecorder()
			fixture.handler("allow").ServeHTTP(response, downloadJobRequest(http.MethodGet, "/api/member/bulletin-download-jobs/"+fixture.store.job.ID+"?series=general&locale=zh-Hant", fixture.userID, ""))
			if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"code":"bulletin_changed"`) || fixture.store.getCalls != 1 || fixture.repository.dataReads != 1 {
				t.Fatalf("status=%d get=%d reads=%d body=%s", response.Code, fixture.store.getCalls, fixture.repository.dataReads, response.Body.String())
			}
		})
	}
}

func TestGetBulletinDownloadJobMapsExpiredToTerminalFailure(t *testing.T) {
	fixture := newDownloadJobFixture(t)
	fixture.store.job = fixture.job("expired")
	fixture.store.job.Progress = 100
	response := httptest.NewRecorder()
	fixture.handler("allow").ServeHTTP(response, downloadJobRequest(http.MethodGet, "/api/member/bulletin-download-jobs/"+fixture.store.job.ID+"?series=general&locale=zh-Hant", fixture.userID, ""))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"failed"`) || !strings.Contains(response.Body.String(), `"errorCode":"expired"`) || response.Header().Get("Retry-After") != "" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestGetBulletinDownloadJobFileRequiresReadyPrivateAsset(t *testing.T) {
	fixture := newDownloadJobFixture(t)
	fixture.store.job = fixture.job("running")
	response := httptest.NewRecorder()
	fixture.handler("allow").ServeHTTP(response, downloadJobRequest(http.MethodGet, "/api/member/bulletin-download-jobs/"+fixture.store.job.ID+"/file?series=general&locale=zh-Hant", fixture.userID, ""))
	if response.Code != http.StatusConflict || fixture.downloads.calls != 0 || fixture.uploads.getCalls != 0 {
		t.Fatalf("status=%d downloads=%d asset=%d body=%s", response.Code, fixture.downloads.calls, fixture.uploads.getCalls, response.Body.String())
	}
}

func TestGetBulletinDownloadJobFileStreamsPDFPrivateNoStoreWithoutRanges(t *testing.T) {
	fixture := newDownloadJobFixture(t)
	fixture.store.job = fixture.job("ready")
	fixture.store.job.CacheExpiresAt = time.Now().Add(time.Hour)
	fixture.uploads.assets[fixture.store.job.DerivedAssetID] = derivedDownloadAsset(fixture.store.job)
	response := httptest.NewRecorder()
	fixture.handler("allow").ServeHTTP(response, downloadJobRequest(http.MethodGet, "/api/member/bulletin-download-jobs/"+fixture.store.job.ID+"/file?series=general&locale=zh-Hant", fixture.userID, ""))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Accept-Ranges") != "none" || response.Header().Get("Content-Type") != "application/pdf" || fixture.downloads.calls != 1 || response.Body.String() != "%PDF-original" {
		t.Fatalf("status=%d headers=%v calls=%d body=%q", response.Code, response.Header(), fixture.downloads.calls, response.Body.String())
	}
}

func TestGetBulletinDownloadJobFileUsesUnicodeContentDisposition(t *testing.T) {
	fixture := newDownloadJobFixture(t)
	fixture.store.job = fixture.job("ready")
	fixture.store.job.CacheExpiresAt = time.Now().Add(time.Hour)
	fixture.uploads.assets[fixture.store.job.DerivedAssetID] = derivedDownloadAsset(fixture.store.job)
	response := httptest.NewRecorder()
	fixture.handler("allow").ServeHTTP(response, downloadJobRequest(http.MethodGet, "/api/member/bulletin-download-jobs/"+fixture.store.job.ID+"/file?series=general&locale=zh-Hant", fixture.userID, ""))
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Disposition"), "filename*=") {
		t.Fatalf("status=%d disposition=%q", response.Code, response.Header().Get("Content-Disposition"))
	}
}

func TestBulletinDownloadJobRequiresTrustedPrincipal(t *testing.T) {
	fixture := newDownloadJobFixture(t)
	request := httptest.NewRequest(http.MethodGet, "/api/member/bulletin-download-jobs/"+uuid.NewString()+"?series=general&locale=zh-Hant", nil)
	response := httptest.NewRecorder()
	fixture.handler("allow").ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || fixture.repository.entitlementReads != 0 || fixture.store.getCalls != 0 {
		t.Fatalf("status=%d entitlement=%d get=%d", response.Code, fixture.repository.entitlementReads, fixture.store.getCalls)
	}
}

type downloadJobFixture struct {
	userID     string
	item       bulletins.ProtectedBulletin
	repository *protectedAPIRepository
	store      *downloadJobStore
	uploads    *downloadJobUploads
	downloads  *fullBulletinDownloadStub
}

func newDownloadJobFixture(t *testing.T) *downloadJobFixture {
	t.Helper()
	userID, issueID := uuid.NewString(), uuid.NewString()
	item := bulletins.ProtectedBulletin{IssueID: issueID, IssueDate: "2026-09-13", Series: "general", Locale: "zh-Hant", Title: "詩篇廿七篇", DownloadName: "1737-詩篇廿七篇.pdf", Version: 3, ProtectedAssetID: strings.Repeat("a", 32)}
	return &downloadJobFixture{
		userID:     userID,
		item:       item,
		repository: &protectedAPIRepository{apiRepository: &apiRepository{}, item: item},
		store:      &downloadJobStore{watermarkStoreStub: &watermarkStoreStub{}},
		uploads: &downloadJobUploads{assets: map[string]assetclient.Asset{item.ProtectedAssetID: {
			ID: item.ProtectedAssetID, Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: issueID, Purpose: "weekly_bulletin", Locale: "zh-Hant", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "not_required", Visibility: "private", ChecksumSHA256: strings.Repeat("b", 64),
		}}},
		downloads: &fullBulletinDownloadStub{},
	}
}

func (f *downloadJobFixture) handler(decision string) http.Handler {
	return NewWithContent(bulletins.NewService(f.repository, nil), nil, nil, f.uploads, "api-gateway", "", true).
		WithBulletinEntitlements(entitlementCheckerStub{decision: decision}).
		WithAssetDownloads(f.downloads).
		WithBulletinWatermark(f.store, nil, "privacy-v1").Routes()
}

func (f *downloadJobFixture) job(status string) bulletinwatermark.DownloadJob {
	now := time.Now().UTC()
	return bulletinwatermark.DownloadJob{ID: uuid.NewString(), AccountID: f.userID, IssueID: f.item.IssueID, IssueDate: f.item.IssueDate, Series: f.item.Series, Locale: f.item.Locale, Revision: f.item.Version, SourceAssetID: f.item.ProtectedAssetID, Filename: f.item.DownloadName, Status: status, Progress: 5, DerivedAssetID: strings.Repeat("c", 32), ReceiptID: uuid.NewString(), CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
}

func derivedDownloadAsset(job bulletinwatermark.DownloadJob) assetclient.Asset {
	return assetclient.Asset{ID: job.DerivedAssetID, Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_download_job", OwnerID: job.ID, Purpose: "weekly_bulletin_member", Locale: job.Locale, UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "not_required", Visibility: "private"}
}

func downloadJobRequest(method, path, userID, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Dapr-Caller-App-Id", "api-gateway")
	request.Header.Set("X-HHC-User-ID", userID)
	request.Header.Set("X-HHC-Auth-Provider", "account-api")
	request.Header.Set("X-HHC-Request-ID", uuid.NewString())
	return request
}
