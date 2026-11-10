package bulletinwatermark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/google/uuid"
)

type downloadWorkerRepo struct {
	job              DownloadJob
	assetID          string
	receipt          Receipt
	failed           string
	progress         []int
	completeErr      error
	released         int
	releaseExhausted bool
}

func (r *downloadWorkerRepo) ClaimDownloadJob(context.Context, string, time.Time) (DownloadJob, error) {
	return r.job, nil
}
func (r *downloadWorkerRepo) CompleteDownloadJob(_ context.Context, _, _, asset string, receipt Receipt, _ time.Time) error {
	r.assetID, r.receipt = asset, receipt
	return r.completeErr
}
func (r *downloadWorkerRepo) FailDownloadJob(_ context.Context, _, _, code string, _ time.Time) error {
	r.failed = code
	return nil
}
func (r *downloadWorkerRepo) ExhaustDownloadJob(context.Context, string, time.Time) error {
	r.failed = "retry_exhausted"
	return nil
}
func (r *downloadWorkerRepo) ReleaseDownloadJob(context.Context, string, string, time.Time) (bool, error) {
	r.released++
	return r.releaseExhausted, nil
}
func (r *downloadWorkerRepo) UpdateDownloadJobProgress(_ context.Context, _, _ string, progress int, _ time.Time) error {
	r.progress = append(r.progress, progress)
	return nil
}

type downloadWorkerRenderer struct {
	t                      *testing.T
	sourceHash, outputHash string
}

type scopedDownloadWorkerRenderer struct {
	downloadWorkerRenderer
	scope PDFScopeMetadata
}

func (r *scopedDownloadWorkerRenderer) RenderScoped(ctx context.Context, source io.Reader, payload string, scope PDFScopeMetadata, _ func(int)) (*Output, error) {
	r.scope = scope
	return r.downloadWorkerRenderer.Render(ctx, source, payload)
}

func (r downloadWorkerRenderer) Render(_ context.Context, source io.Reader, payload string) (*Output, error) {
	data, _ := io.ReadAll(source)
	sourceSum := sha256.Sum256(data)
	r.sourceHash = hex.EncodeToString(sourceSum[:])
	out := []byte("%PDF-clean-output")
	outputSum := sha256.Sum256(out)
	r.outputHash = hex.EncodeToString(outputSum[:])
	f, err := os.CreateTemp(r.t.TempDir(), "rendered-*.pdf")
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(out); err != nil {
		return nil, err
	}
	_, _ = f.Seek(0, io.SeekStart)
	return &Output{File: f, Metadata: RenderMetadata{SourceSHA256: r.sourceHash, OutputSHA256: r.outputHash, RendererVersion: "trustmark-pdf-v2", ModelSHA256: strings.Repeat("d", 64), Pages: []PageMetadata{{Page: 1, Width: 100, Height: 100, SourceSHA256: strings.Repeat("e", 64), OutputSHA256: strings.Repeat("f", 64)}}}}, nil
}

type downloadWorkerAssetStub struct {
	sourceStatus int
	uploadErr    error
	uploaded     bool
	deleted      bool
	uploadKeys   []string
	completion   *assetclient.Asset
}

type workQueueStub struct {
	message    LeasedMessage
	dequeueErr error
	poisoned   bool
	acked      bool
	retryDelay time.Duration
}

func (q *workQueueStub) Dequeue(context.Context) (LeasedMessage, bool, error) {
	return q.message, true, q.dequeueErr
}
func (q *workQueueStub) Ack(context.Context, LeasedMessage) error { q.acked = true; return nil }
func (q *workQueueStub) Retry(_ context.Context, _ LeasedMessage, delay time.Duration) error {
	q.retryDelay = delay
	return nil
}
func (q *workQueueStub) Poison(context.Context, LeasedMessage) error {
	q.poisoned = true
	return nil
}

func (a *downloadWorkerAssetStub) AuthorizedDownload(context.Context, string, string, string, string) (*http.Response, error) {
	status := a.sourceStatus
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("%PDF-source"))}, nil
}
func (a *downloadWorkerAssetStub) CreateDerivedBulletinUpload(_ context.Context, _, _, _ string, _ int64, key string) (assetclient.CreatedUpload, error) {
	a.uploadKeys = append(a.uploadKeys, key)
	return assetclient.CreatedUpload{Asset: assetclient.Asset{ID: strings.Repeat("a", 32)}, UploadTarget: assetclient.UploadTarget{URL: "https://example.invalid/upload", Method: http.MethodPut}}, nil
}
func (a *downloadWorkerAssetStub) Upload(context.Context, assetclient.UploadTarget, io.Reader, int64) error {
	if a.uploadErr != nil {
		return a.uploadErr
	}
	a.uploaded = true
	return nil
}
func (a *downloadWorkerAssetStub) CompleteDerivedBulletin(context.Context, string, string, string, string, assetclient.CompleteUploadInput) (assetclient.Asset, error) {
	if a.completion != nil {
		return *a.completion, nil
	}
	return assetclient.Asset{ID: strings.Repeat("a", 32), ScanStatus: "clean", ScanSignature: "trusted-bulletin-watermark-v1"}, nil
}

func TestDownloadWorkerRejectsUntrustedCompletionWithoutPolling(t *testing.T) {
	for _, result := range []assetclient.Asset{
		{ID: strings.Repeat("a", 32), ScanStatus: "pending"},
		{ID: strings.Repeat("a", 32), ScanStatus: "clean", ScanSignature: "clamav"},
		{ID: strings.Repeat("b", 32), ScanStatus: "clean", ScanSignature: "trusted-bulletin-watermark-v1"},
	} {
		sum := sha256.Sum256([]byte("%PDF-source"))
		repo := &downloadWorkerRepo{job: DownloadJob{ID: uuid.NewString(), SourceSHA256: hex.EncodeToString(sum[:]), FingerprintPayload: strings.Repeat("0", 61)}}
		assets := &downloadWorkerAssetStub{completion: &result}
		worker := NewDownloadWorker(repo, downloadWorkerRenderer{t: t}, assets, "privacy")
		if err := worker.Process(context.Background(), repo.job.ID); err == nil || !assets.deleted || repo.assetID != "" || repo.released != 1 {
			t.Fatalf("untrusted completion accepted: err=%v deleted=%v ready=%v released=%d", err, assets.deleted, repo.assetID != "", repo.released)
		}
	}
}
func (a *downloadWorkerAssetStub) CreateServiceGrant(context.Context, string, string) (assetclient.Grant, error) {
	return assetclient.Grant{ID: "grant"}, nil
}
func (a *downloadWorkerAssetStub) Delete(context.Context, string) error { a.deleted = true; return nil }

func TestDownloadWorkerPersistsReadyReceiptAfterPrivateAsset(t *testing.T) {
	now := time.Date(2026, 9, 13, 1, 2, 3, 0, time.UTC)
	sourceSum := sha256.Sum256([]byte("%PDF-source"))
	repo := &downloadWorkerRepo{job: DownloadJob{ID: uuid.NewString(), AccountID: uuid.NewString(), IssueID: uuid.NewString(), Locale: "zh-Hant", Revision: 3, SourceAssetID: strings.Repeat("b", 32), SourceSHA256: hex.EncodeToString(sourceSum[:]), Filename: "1737-詩篇廿七篇.pdf", RendererVersion: "trustmark-pdf-v2", FingerprintPayload: strings.Repeat("0", 61), TraceCode: "0000000000000000J3", LeaseToken: uuid.NewString(), Generation: 1}}
	assets := &downloadWorkerAssetStub{}
	renderer := &scopedDownloadWorkerRenderer{downloadWorkerRenderer: downloadWorkerRenderer{t: t}}
	worker := NewDownloadWorker(repo, renderer, assets, "privacy-v1")
	worker.now = func() time.Time { return now }
	if err := worker.Process(context.Background(), repo.job.ID); err != nil {
		t.Fatal(err)
	}
	if !assets.uploaded || assets.deleted || repo.assetID != strings.Repeat("a", 32) || repo.receipt.UserID != repo.job.AccountID || repo.receipt.FingerprintPayload != repo.job.FingerprintPayload {
		t.Fatalf("asset=%q receipt=%+v uploaded=%v deleted=%v", repo.assetID, repo.receipt, assets.uploaded, assets.deleted)
	}
	if renderer.scope != (PDFScopeMetadata{Version: 1, IssueID: repo.job.IssueID, Locale: repo.job.Locale, Revision: repo.job.Revision, SourceSHA256: repo.job.SourceSHA256, RendererVersion: repo.job.RendererVersion}) {
		t.Fatalf("scope metadata=%+v", renderer.scope)
	}
	if len(repo.progress) == 0 {
		t.Fatal("worker did not checkpoint progress")
	}
}

func TestWorkMessageContainsOnlyKindAndUUID(t *testing.T) {
	if !validWorkMessage(WorkMessage{Kind: "download", ID: uuid.NewString()}) || validWorkMessage(WorkMessage{Kind: "download", ID: "account@example.com"}) || validWorkMessage(WorkMessage{Kind: "other", ID: uuid.NewString()}) {
		t.Fatal("work message validation")
	}
}

func TestExhaustedDownloadIsFailedBeforePoisoning(t *testing.T) {
	repo := &downloadWorkerRepo{}
	queue := &workQueueStub{message: LeasedMessage{WorkMessage: WorkMessage{Kind: "download", ID: uuid.NewString()}, DequeueCount: 4}}
	processed, err := ProcessOne(context.Background(), queue, NewDownloadWorker(repo, nil, nil, ""))
	if err != nil || !processed || !queue.poisoned || repo.failed != "retry_exhausted" {
		t.Fatalf("processed=%v poisoned=%v failed=%q error=%v", processed, queue.poisoned, repo.failed, err)
	}
}

func TestDownloadWorkerCollisionFailsJob(t *testing.T) {
	sourceSum := sha256.Sum256([]byte("%PDF-source"))
	repo := &downloadWorkerRepo{job: DownloadJob{ID: uuid.NewString(), SourceSHA256: hex.EncodeToString(sourceSum[:]), FingerprintPayload: strings.Repeat("0", 61), Generation: 2, Attempts: 1}, completeErr: ErrCodeCollision}
	assets := &downloadWorkerAssetStub{}
	worker := NewDownloadWorker(repo, downloadWorkerRenderer{t: t}, assets, "privacy-v1")
	if err := worker.Process(context.Background(), repo.job.ID); err != nil || repo.failed != "receipt_collision" || !assets.deleted {
		t.Fatalf("error=%v failed=%q deleted=%v", err, repo.failed, assets.deleted)
	}
}

func TestDownloadWorkerRetryDoesNotReuseDeletedUpload(t *testing.T) {
	sourceSum := sha256.Sum256([]byte("%PDF-source"))
	repo := &downloadWorkerRepo{job: DownloadJob{ID: uuid.NewString(), SourceSHA256: hex.EncodeToString(sourceSum[:]), FingerprintPayload: strings.Repeat("0", 61), Generation: 2, Attempts: 1}, completeErr: io.ErrUnexpectedEOF}
	assets := &downloadWorkerAssetStub{}
	worker := NewDownloadWorker(repo, downloadWorkerRenderer{t: t}, assets, "privacy-v1")
	if err := worker.Process(context.Background(), repo.job.ID); err == nil || !assets.deleted {
		t.Fatal("expected failed attempt with deleted upload")
	}
	repo.job.Attempts++
	repo.completeErr = nil
	if err := worker.Process(context.Background(), repo.job.ID); err != nil {
		t.Fatal(err)
	}
	if len(assets.uploadKeys) != 2 || assets.uploadKeys[0] == assets.uploadKeys[1] {
		t.Fatalf("retry reused deleted upload: %v", assets.uploadKeys)
	}
}
