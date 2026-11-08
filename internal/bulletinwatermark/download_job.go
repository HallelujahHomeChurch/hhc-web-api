package bulletinwatermark

import (
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

const DownloadCacheTTL = 7 * 24 * time.Hour

var (
	ErrInvalidDownloadJob = errors.New("invalid bulletin download job")
	ErrDownloadConflict   = errors.New("bulletin download idempotency conflict")
	ErrDownloadLease      = errors.New("bulletin download lease unavailable")
)

type DownloadJob struct {
	ID, IdempotencyKey, AccountID, IssueID, IssueDate, Series, Locale, SourceAssetID, SourceSHA256, Filename string
	RendererVersion, FingerprintPayload, TraceCode, Status, DerivedAssetID, ReceiptID, ErrorCode             string
	Revision                                                                                                 int64
	Generation, Attempts                                                                                     int
	Progress                                                                                                 int
	LeaseToken                                                                                               string
	LeaseUntil, CacheExpiresAt, CreatedAt, UpdatedAt, ExpiresAt                                              time.Time
}

func (j DownloadJob) Ready(now time.Time) bool {
	return j.Status == "ready" && j.DerivedAssetID != "" && j.ReceiptID != "" && j.CacheExpiresAt.After(now)
}

type DownloadJobRequest struct {
	ID, IdempotencyKey, AccountID, IssueDate, RendererVersion, FingerprintPayload, TraceCode string
	Source                                                                                   Source
	SourceSHA256                                                                             string
	Now                                                                                      time.Time
}

type DownloadJobView struct {
	ID                string                     `json:"id"`
	OperationProgress platform.OperationProgress `json:"operationProgress"`
	CreatedAt         time.Time                  `json:"createdAt"`
	ExpiresAt         time.Time                  `json:"expiresAt"`
}

func (j DownloadJob) View() DownloadJobView {
	percent := min(100, max(0, j.Progress))
	progress := platform.OperationProgress{Status: "running", Stage: "preparing", Percent: percent, UpdatedAt: j.UpdatedAt, RetryAfterMS: 2000}
	switch {
	case j.Status == "queued":
		progress.Status, progress.Stage = "queued", "queued"
	case j.Status == "ready":
		progress.Status, progress.Stage, progress.Percent = "ready", "ready", 100
	case j.Status == "failed":
		progress.Status, progress.Stage, progress.ErrorCode = "failed", "failed", j.ErrorCode
	case j.Status == "expired":
		progress.Status, progress.Stage, progress.ErrorCode = "failed", "failed", "expired"
	case j.Status != "running":
		progress.Status, progress.Stage, progress.ErrorCode = "failed", "failed", "download_failed"
	case percent < 15:
		progress.Stage = "preparing"
	case percent <= 80:
		progress.Stage = "rendering"
	case percent <= 88:
		progress.Stage = "storing"
	case percent <= 92:
		progress.Stage = "security_check"
	default:
		progress.Stage = "authorizing"
	}
	return DownloadJobView{ID: j.ID, OperationProgress: progress, CreatedAt: j.CreatedAt, ExpiresAt: j.ExpiresAt}
}
