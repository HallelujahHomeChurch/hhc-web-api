package bulletinwatermark

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/google/uuid"
)

type DownloadWorkerRepository interface {
	ClaimDownloadJob(context.Context, string, time.Time) (DownloadJob, error)
	CompleteDownloadJob(context.Context, string, string, string, Receipt, time.Time) error
	FailDownloadJob(context.Context, string, string, string, time.Time) error
	ExhaustDownloadJob(context.Context, string, time.Time) error
	UpdateDownloadJobProgress(context.Context, string, string, int, time.Time) error
	// ReleaseDownloadJob returns a running job to queued after a retryable
	// failure. It reports exhausted=true when no claim attempts remain and
	// the job was marked failed instead.
	ReleaseDownloadJob(context.Context, string, string, time.Time) (bool, error)
}

type DownloadWorkerAssets interface {
	AuthorizedDownload(context.Context, string, string, string, string) (*http.Response, error)
	CreateDerivedBulletinUpload(context.Context, string, string, string, int64, string) (assetclient.CreatedUpload, error)
	Upload(context.Context, assetclient.UploadTarget, io.Reader, int64) error
	CompleteDerivedBulletin(context.Context, string, string, string, assetclient.CompleteUploadInput) (assetclient.Asset, error)
	CreateServiceGrant(context.Context, string, string) (assetclient.Grant, error)
	Delete(context.Context, string) error
}

type DownloadWorker struct {
	repository DownloadWorkerRepository
	renderer   watermarkRenderer
	assets     DownloadWorkerAssets
	privacy    string
	now        func() time.Time
}

type watermarkRenderer interface {
	Render(context.Context, io.Reader, string) (*Output, error)
}

type progressiveWatermarkRenderer interface {
	RenderProgress(context.Context, io.Reader, string, func(int)) (*Output, error)
}

type scopedWatermarkRenderer interface {
	RenderScoped(context.Context, io.Reader, string, PDFScopeMetadata, func(int)) (*Output, error)
}

func NewDownloadWorker(repository DownloadWorkerRepository, renderer watermarkRenderer, assets DownloadWorkerAssets, privacy string) *DownloadWorker {
	return &DownloadWorker{repository: repository, renderer: renderer, assets: assets, privacy: privacy, now: time.Now}
}

func (w *DownloadWorker) Exhaust(ctx context.Context, id string) error {
	return w.repository.ExhaustDownloadJob(ctx, id, w.now().UTC())
}

// RetryableError reports a job failure that should be redelivered by the
// queue after a short backoff. Exhausted means the DB attempt budget is spent
// and the job is already failed, so the message only needs to be acked.
type RetryableError struct {
	Err       error
	Exhausted bool
}

func (e *RetryableError) Error() string {
	return "retryable bulletin watermark failure: " + e.Err.Error()
}
func (e *RetryableError) Unwrap() error { return e.Err }

// terminalFailure is a job outcome that retrying cannot change.
type terminalFailure struct{ code string }

func (e terminalFailure) Error() string { return "terminal bulletin download failure: " + e.code }

// Process claims and renders one download job. Terminal failures are recorded
// on the job and return nil. Every other failure releases the DB lease and
// returns *RetryableError so one bad job never stops the worker process.
func (w *DownloadWorker) Process(ctx context.Context, id string) error {
	job, err := w.repository.ClaimDownloadJob(ctx, id, w.now().UTC())
	markPhase(ctx, "claim")
	if errors.Is(err, ErrNotFound) {
		setPhaseAttr(ctx, "outcome", "not_claimable")
		return nil
	}
	if err != nil {
		setPhaseAttr(ctx, "error_class", "claim")
		return &RetryableError{Err: err}
	}
	err = w.process(ctx, job)
	var terminal terminalFailure
	switch {
	case err == nil:
		return nil
	case errors.As(err, &terminal):
		// The lease is fenced; a lost lease means another attempt owns the job.
		_ = w.repository.FailDownloadJob(context.WithoutCancel(ctx), job.ID, job.LeaseToken, terminal.code, w.now().UTC())
		setPhaseAttr(ctx, "outcome", "failed")
		setPhaseAttr(ctx, "error_class", terminal.code)
		return nil
	default:
		if _, ok := phaseAttr(ctx, "error_class"); !ok {
			setPhaseAttr(ctx, "error_class", "retryable")
		}
		exhausted, releaseErr := w.repository.ReleaseDownloadJob(context.WithoutCancel(ctx), job.ID, job.LeaseToken, w.now().UTC())
		if releaseErr != nil && !errors.Is(releaseErr, ErrDownloadLease) {
			return &RetryableError{Err: errors.Join(err, releaseErr)}
		}
		return &RetryableError{Err: err, Exhausted: exhausted}
	}
}

func (w *DownloadWorker) process(ctx context.Context, job DownloadJob) error {
	response, err := w.assets.AuthorizedDownload(ctx, http.MethodGet, job.SourceAssetID, "", "")
	if err != nil {
		setPhaseAttr(ctx, "error_class", "source")
		return err
	}
	defer response.Body.Close()
	markPhase(ctx, "source_open")
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusGone:
		return terminalFailure{code: "source_unavailable"}
	default:
		setPhaseAttr(ctx, "error_class", "source")
		return fmt.Errorf("source status %d", response.StatusCode)
	}
	checkpoint := func(progress int) error {
		return w.repository.UpdateDownloadJobProgress(ctx, job.ID, job.LeaseToken, progress, w.now().UTC())
	}
	if err = checkpoint(15); err != nil {
		return err
	}
	scope := PDFScopeMetadata{Version: 1, IssueID: job.IssueID, Series: job.Series, Locale: job.Locale, Revision: job.Revision, SourceSHA256: job.SourceSHA256, RendererVersion: job.RendererVersion}
	var output *Output
	if renderer, ok := w.renderer.(scopedWatermarkRenderer); ok {
		var progressErr error
		output, err = renderer.RenderScoped(ctx, response.Body, job.FingerprintPayload, scope, func(progress int) {
			if progressErr == nil {
				progressErr = checkpoint(progress)
			}
		})
		if progressErr != nil {
			return progressErr
		}
	} else if renderer, ok := w.renderer.(progressiveWatermarkRenderer); ok {
		var progressErr error
		output, err = renderer.RenderProgress(ctx, response.Body, job.FingerprintPayload, func(progress int) {
			if progressErr == nil {
				progressErr = checkpoint(progress)
			}
		})
		if progressErr != nil {
			return progressErr
		}
	} else {
		output, err = w.renderer.Render(ctx, response.Body, job.FingerprintPayload)
	}
	if errors.Is(err, ErrInvalidFingerprint) {
		return terminalFailure{code: "invalid_payload"}
	}
	if err != nil {
		setPhaseAttr(ctx, "error_class", "render")
		return err
	}
	defer output.Close()
	setPhaseAttr(ctx, "pages", strconv.Itoa(len(output.Metadata.Pages)))
	if err = checkpoint(82); err != nil {
		return err
	}
	if output.Metadata.SourceSHA256 != job.SourceSHA256 {
		return terminalFailure{code: "source_changed"}
	}
	info, err := output.Stat()
	if err != nil || info.Size() < 5 || info.Size() > MaxOutputBytes {
		return terminalFailure{code: "output_too_large"}
	}
	upload, err := w.assets.CreateDerivedBulletinUpload(ctx, job.ID, job.Locale, job.Filename, info.Size(), fmt.Sprintf("bulletin-download:%s:%d:%d", job.ID, job.Generation, job.Attempts))
	markPhase(ctx, "upload_create")
	if err != nil {
		setPhaseAttr(ctx, "error_class", "asset")
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = w.assets.Delete(context.WithoutCancel(ctx), upload.Asset.ID)
		}
	}()
	if _, err = output.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err = w.assets.Upload(ctx, upload.UploadTarget, output, info.Size()); err != nil {
		setPhaseAttr(ctx, "error_class", "asset")
		return err
	}
	markPhase(ctx, "upload")
	if err = checkpoint(88); err != nil {
		return err
	}
	completed, err := w.assets.CompleteDerivedBulletin(ctx, upload.Asset.ID, job.SourceAssetID, job.SourceSHA256, assetclient.CompleteUploadInput{SizeBytes: info.Size(), ChecksumSHA256: output.Metadata.OutputSHA256, MIMEType: "application/pdf"})
	if err != nil {
		setPhaseAttr(ctx, "error_class", "asset")
		return err
	}
	// Trusted derivation replaces the second ClamAV scan (asset-api #196); the
	// worker never polls or accepts an ordinary scan result here.
	if completed.ID != upload.Asset.ID || completed.ScanStatus != "clean" || completed.ScanSignature != "trusted-bulletin-watermark-v1" {
		setPhaseAttr(ctx, "error_class", "asset_untrusted_completion")
		return errors.New("trusted bulletin completion unavailable")
	}
	markPhase(ctx, "complete")
	if _, err = w.assets.CreateServiceGrant(ctx, upload.Asset.ID, fmt.Sprintf("bulletin-download-grant:%s:%d:%d", job.ID, job.Generation, job.Attempts)); err != nil {
		setPhaseAttr(ctx, "error_class", "asset")
		return err
	}
	markPhase(ctx, "grant")
	if err = checkpoint(98); err != nil {
		return err
	}
	completedAt := w.now().UTC()
	receipt := Receipt{ID: uuid.NewString(), Code: job.TraceCode, UserID: job.AccountID, IssueID: job.IssueID, Series: job.Series, Locale: job.Locale, Revision: job.Revision, SourceAssetID: job.SourceAssetID, SourceSHA256: job.SourceSHA256, OutputSHA256: output.Metadata.OutputSHA256, FingerprintPayload: job.FingerprintPayload, ModelSHA256: output.Metadata.ModelSHA256, Pages: output.Metadata.Pages, RendererVersion: output.Metadata.RendererVersion, PrivacyVersion: w.privacy, IssuedAt: completedAt, ExpiresAt: completedAt.Add(Retention)}
	if receipt.ValidateFingerprint() != nil || len(receipt.OutputSHA256) != 64 || strings.Trim(receipt.OutputSHA256, "0123456789abcdef") != "" {
		return ErrUnavailable
	}
	if err = w.repository.CompleteDownloadJob(ctx, job.ID, job.LeaseToken, upload.Asset.ID, receipt, completedAt); err != nil {
		if errors.Is(err, ErrCodeCollision) {
			return terminalFailure{code: "receipt_collision"}
		}
		return err
	}
	markPhase(ctx, "finalize")
	setPhaseAttr(ctx, "outcome", "ready")
	cleanup = false
	return nil
}

type WorkQueue interface {
	Dequeue(context.Context) (LeasedMessage, bool, error)
	Ack(context.Context, LeasedMessage) error
	Poison(context.Context, LeasedMessage) error
	Retry(context.Context, LeasedMessage, time.Duration) error
}

// RetryDelay is the redelivery backoff for a retryable failure: 15s, 30s,
// then 60s, each with up to 20% jitter so a burst does not retry in lockstep.
func RetryDelay(dequeueCount int64) time.Duration {
	base := 15 * time.Second
	switch {
	case dequeueCount >= 3:
		base = 60 * time.Second
	case dequeueCount == 2:
		base = 30 * time.Second
	}
	return base + time.Duration(rand.Int64N(int64(base/5)+1))
}

type InvestigationProcessor interface {
	Process(context.Context, string) error
}

func ProcessOne(ctx context.Context, queue WorkQueue, downloads *DownloadWorker, investigations ...InvestigationProcessor) (processed bool, err error) {
	message, found, err := queue.Dequeue(ctx)
	markPhase(ctx, "dequeue")
	if found && message.Kind != "" {
		setPhaseAttr(ctx, "kind", message.Kind)
	}
	if found {
		defer func() {
			if err != nil {
				setPhaseAttr(ctx, "outcome", "error")
			} else if _, ok := phaseAttr(ctx, "outcome"); !ok {
				setPhaseAttr(ctx, "outcome", "acked")
			}
			markPhase(ctx, "ack")
			phaseTimerFrom(ctx).Log(ctx)
		}()
	}
	if err != nil {
		if found && message.MessageID != "" {
			// A malformed message can never succeed; quarantine it and keep
			// serving other work. Only a failed quarantine stops the process.
			if poisonErr := queue.Poison(ctx, message); poisonErr != nil {
				return true, errors.Join(err, poisonErr)
			}
			setPhaseAttr(ctx, "outcome", "invalid_message")
			return true, nil
		}
		return found, err
	}
	if !found {
		return false, nil
	}
	if message.DequeueCount > 3 {
		switch message.Kind {
		case "download":
			err = downloads.Exhaust(ctx, message.ID)
		case "investigation":
			if len(investigations) == 0 || investigations[0] == nil {
				err = errors.New("investigation worker is not configured")
			} else if exhauster, ok := investigations[0].(interface {
				Exhaust(context.Context, string) error
			}); ok {
				err = exhauster.Exhaust(ctx, message.ID)
			}
		}
		if err != nil {
			return true, err
		}
		setPhaseAttr(ctx, "outcome", "exhausted")
		return true, queue.Poison(ctx, message)
	}
	switch message.Kind {
	case "download":
		err = downloads.Process(ctx, message.ID)
	case "investigation":
		if len(investigations) == 0 || investigations[0] == nil {
			// Process-level configuration error: exit and let the message return.
			return true, errors.New("investigation worker is not configured")
		}
		if err = investigations[0].Process(ctx, message.ID); err != nil {
			setPhaseAttr(ctx, "error_class", "investigation")
			err = &RetryableError{Err: err}
		}
	default:
		return true, errors.New("unsupported watermark work kind")
	}
	var retryable *RetryableError
	if errors.As(err, &retryable) {
		if retryable.Exhausted {
			setPhaseAttr(ctx, "outcome", "exhausted")
			return true, queue.Ack(context.WithoutCancel(ctx), message)
		}
		setPhaseAttr(ctx, "outcome", "retry")
		// Shutdown must not strand the message behind the full dequeue
		// visibility; the lease is already released.
		return true, queue.Retry(context.WithoutCancel(ctx), message, RetryDelay(message.DequeueCount))
	}
	if err != nil {
		return true, err
	}
	return true, queue.Ack(ctx, message)
}

type InvestigationWorker struct {
	runner     *Runner
	repository InvestigationByIDRepository
	assets     InvestigationAssets
}

func NewInvestigationWorker(runner *Runner, repository InvestigationByIDRepository, assets InvestigationAssets) *InvestigationWorker {
	return &InvestigationWorker{runner: runner, repository: repository, assets: assets}
}

func (w *InvestigationWorker) Process(ctx context.Context, id string) error {
	return w.runner.ProcessInvestigation(ctx, w.repository, w.assets, id)
}

func (w *InvestigationWorker) Exhaust(ctx context.Context, id string) error {
	return w.repository.ExhaustWatermarkInvestigation(ctx, id, time.Now().UTC())
}
