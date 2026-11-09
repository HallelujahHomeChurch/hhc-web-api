package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

type Worker struct {
	summaries       SummaryGenerator
	repository      Repository
	assets          AssetClient
	notifications   NotificationClient
	maxAttempts     int
	now             func() time.Time
	watermarkQueue  MessageQueue
	extractionQueue MessageQueue
	auditClient     interface {
		Append(context.Context, []byte) auditclient.AppendResult
	}
	auditStats interface {
		Stats(context.Context, time.Time) (auditclient.PipelineStats, error)
	}
	auditLog        *slog.Logger
	recordingPolicy func(context.Context, string) (bool, error)
}

func (w *Worker) WithRecordingPolicy(policy func(context.Context, string) (bool, error)) {
	w.recordingPolicy = policy
}

func (w *Worker) WithWatermarkQueue(queue MessageQueue)  { w.watermarkQueue = queue }
func (w *Worker) WithExtractionQueue(queue MessageQueue) { w.extractionQueue = queue }
func (w *Worker) WithAudit(client interface {
	Append(context.Context, []byte) auditclient.AppendResult
}, stats interface {
	Stats(context.Context, time.Time) (auditclient.PipelineStats, error)
}, logger *slog.Logger) {
	w.auditClient, w.auditStats, w.auditLog = client, stats, logger
	if w.auditLog == nil {
		w.auditLog = slog.Default()
	}
}

const assetReadinessDeadline = 30 * time.Minute

func NewWorker(repository Repository, assets AssetClient, maxAttempts int, notifications ...NotificationClient) *Worker {
	worker := &Worker{repository: repository, assets: assets, maxAttempts: maxAttempts, now: time.Now}
	if len(notifications) > 0 {
		worker.notifications = notifications[0]
	}
	return worker
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	if w.auditStats != nil {
		snapshots := time.NewTicker(time.Minute)
		defer snapshots.Stop()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-ctx.Done():
					return
				case <-snapshots.C:
					stats, err := w.auditStats.Stats(ctx, w.now().UTC())
					if err != nil {
						w.auditLog.Error("audit_pipeline_snapshot_failed", "sourceService", "hhc-web-api")
						continue
					}
					w.auditLog.Info("audit_pipeline_snapshot", "sourceService", "hhc-web-api", "deliveryMode", "outbox", "pendingCount", stats.PendingCount, "oldestPendingSeconds", stats.OldestPendingSeconds, "deadLetterCount", stats.DeadLetterCount)
				}
			}
		}()
		defer func() { <-done }()
	}
	nextWatermarkPurge := time.Time{}
	nextDownloadSweep := time.Time{}
	for {
		if !w.now().Before(nextWatermarkPurge) {
			if purger, ok := w.repository.(interface {
				PurgeWatermarkRecords(context.Context, time.Time, int) (int64, error)
			}); ok {
				n, err := purger.PurgeWatermarkRecords(ctx, w.now().UTC(), 1000)
				switch {
				case err != nil:
					slog.Error("watermark cleanup failed; retrying")
					nextWatermarkPurge = w.now().Add(time.Minute)
				case n > 0:
					slog.Info("watermark expired records removed", "count", n)
					nextWatermarkPurge = w.now().Add(2 * time.Second)
				default:
					nextWatermarkPurge = w.now().Add(time.Minute)
				}
			} else {
				nextWatermarkPurge = w.now().Add(24 * time.Hour)
			}
		}
		if !w.now().Before(nextDownloadSweep) {
			nextDownloadSweep = w.now().Add(time.Minute)
			if sweeper, ok := w.repository.(interface {
				RecoverStalledDownloadJobs(context.Context, time.Time, int) (int64, int64, error)
			}); ok {
				requeued, failed, err := sweeper.RecoverStalledDownloadJobs(ctx, w.now().UTC(), 100)
				switch {
				case err != nil:
					slog.Error("bulletin download recovery failed; retrying")
				case requeued > 0 || failed > 0:
					// Counter for stuck-job monitoring; counts only, no identifiers.
					slog.Warn("bulletin download jobs recovered", "requeued", requeued, "failed", failed)
				}
			} else {
				nextDownloadSweep = w.now().Add(24 * time.Hour)
			}
		}
		processed, err := w.processNext(ctx)
		if err != nil {
			slog.Error("publication worker", "error", err)
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (w *Worker) processNext(ctx context.Context) (bool, error) {
	now := w.now().UTC()
	event, found, err := w.repository.Claim(ctx, now, 30*time.Second)
	if err != nil || !found {
		return false, err
	}

	switch event.EventType {
	case "bulletin.unpublish.revoke_asset", "bulletin.asset.retire", "asset.owner.delete", "asset.grant.revoke":
		var source struct {
			ActorID   string `json:"actorId"`
			RequestID string `json:"requestId"`
			GrantID   string `json:"grantId"`
		}
		_ = json.Unmarshal(event.Payload, &source)
		if source.ActorID == "" {
			source.ActorID = event.ActorID
		}
		if source.RequestID == "" {
			source.RequestID = event.ID
		}
		if auditclient.ValidActorRequest(source.ActorID, source.RequestID) {
			ctx = auditclient.WithProvenance(ctx, source.ActorID, source.RequestID)
		} else if event.EventType != "asset.owner.delete" || source.GrantID != "" {
			slog.ErrorContext(ctx, "publication cleanup provenance unavailable", "failure_reason", "audit_provenance_missing", "request_id", event.ID)
		}
	}
	switch event.EventType {
	case "audit.append":
		return true, w.appendAudit(ctx, event, now)
	case "news.summary.generate":
		return true, w.generateSummary(ctx, event)
	case "bulletin.publish.ensure_asset":
		err = w.publish(ctx, event, now)
	case "bulletin.online.publish_simplified":
		return true, w.publishSimplified(ctx, event)
	case "bulletin.online.learn":
		return true, w.learnBulletin(ctx, event)
	case "bulletin.online.ensure_extraction":
		err = w.ensureBulletinExtraction(ctx, event)
	case "bulletin.unpublish.revoke_asset":
		err = w.unpublish(ctx, event, now)
	case "bulletin.asset.retire":
		err = w.retire(ctx, event, now)
	case "asset.owner.delete":
		err = w.retire(ctx, event, now)
	case "news.publish.ensure_asset":
		err = w.publishNews(ctx, event, now)
	case "home.publish.ensure_asset":
		err = w.publishHome(ctx, event, now)
	case "news.unpublish.revoke_asset":
		err = w.unpublishNews(ctx, event, now)
	case "asset.grant.revoke":
		err = w.revokeGrant(ctx, event, now)
	case "statement.notification.queue":
		err = w.notifyStatement(ctx, event, now)
	case "bulletin.notification.queue":
		err = w.notifyBulletin(ctx, event, now)
	case "content.notification.submit", "bulletin.notification.submit", "recording.notification.submit":
		err = w.submitNotifications(ctx, event, now)
	case "bulletin.online.wake":
		if w.extractionQueue == nil {
			err = fmt.Errorf("extraction queue is not configured")
		} else if err = w.extractionQueue.Enqueue(ctx, event.Payload); err == nil {
			err = w.repository.Complete(ctx, event.ID, now)
		}
	case "bulletin.watermark.enqueue":
		if w.watermarkQueue == nil {
			err = terminalError{fmt.Errorf("watermark queue is not configured")}
		} else if err = w.watermarkQueue.Enqueue(ctx, event.Payload); err == nil {
			err = w.repository.Complete(ctx, event.ID, now)
		}
	default:
		err = terminalError{fmt.Errorf("unsupported outbox event %s", event.EventType)}
	}
	if err == nil {
		return true, nil
	}
	if event.EventType == "bulletin.online.ensure_extraction" {
		return true, w.rescheduleBulletinExtraction(ctx, event, err)
	}
	var pending assetPendingError
	if errors.As(err, &pending) {
		if !event.CreatedAt.IsZero() && now.Sub(event.CreatedAt) >= assetReadinessDeadline {
			return true, w.repository.Fail(ctx, event, "asset readiness deadline exceeded", now)
		}
		return true, w.repository.Defer(ctx, event.ID, safeError(err), now.Add(10*time.Second), now)
	}
	var terminal terminalError
	var compensation compensationError
	var permanent interface{ Permanent() bool }
	// No cron remains to rescue queued work if its last wake is dropped.
	if errors.As(err, &terminal) || (errors.As(err, &permanent) && permanent.Permanent()) || (event.EventType != "bulletin.online.wake" && event.Attempts >= w.maxAttempts && !errors.As(err, &compensation)) {
		return true, w.repository.Fail(ctx, event, safeError(err), now)
	}
	backoff := time.Duration(1<<min(event.Attempts-1, 5)) * 5 * time.Second
	return true, w.repository.Retry(ctx, event.ID, safeError(err), now.Add(backoff), now)
}

func (w *Worker) submitNotifications(ctx context.Context, event Event, now time.Time) error {
	if w.notifications == nil {
		return terminalError{fmt.Errorf("notification client is not configured")}
	}
	var payload NotificationSubmissionPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.Name == "" || len(payload.Notifications) == 0 || payload.ActorID == "" || payload.RequestID == "" || payload.IdempotencyKey == "" {
		return terminalError{fmt.Errorf("invalid notification submission payload")}
	}
	if event.EventType == "recording.notification.submit" {
		if payload.Source == nil || payload.Source.Kind != "recording" || payload.Source.RecordingID != event.AggregateID {
			return terminalError{fmt.Errorf("invalid recording notification source")}
		}
		if w.recordingPolicy == nil {
			return fmt.Errorf("recording policy is not configured")
		}
		available, err := w.recordingPolicy(ctx, event.AggregateID)
		if err != nil {
			return err
		}
		if !available {
			return w.repository.Complete(ctx, event.ID, now)
		}
	}
	if err := w.notifications.SubmitNotifications(ctx, NotificationSubmission{
		Name: payload.Name, Source: payload.Source, Notifications: payload.Notifications, ActorID: payload.ActorID,
		RequestID: payload.RequestID, IdempotencyKey: payload.IdempotencyKey,
	}); err != nil {
		return err
	}
	return w.repository.Complete(ctx, event.ID, now)
}

func (w *Worker) appendAudit(ctx context.Context, event Event, now time.Time) error {
	if _, err := auditclient.Decode(event.Payload); err != nil {
		return w.repository.Fail(ctx, event, "invalid_event", now)
	}
	if w.auditClient == nil {
		return w.repository.Fail(ctx, event, "audit_client_unavailable", now)
	}
	result := w.auditClient.Append(ctx, event.Payload)
	level := slog.LevelInfo
	if result.Result == "conflict" {
		level = slog.LevelError
	}
	w.auditLog.Log(ctx, level, "audit_append_result", "sourceService", "hhc-web-api", "result", result.Result, "httpStatus", result.HTTPStatus, "durationMs", result.DurationMs)
	switch result.Result {
	case "accepted", "duplicate":
		return w.repository.Complete(ctx, event.ID, now)
	case "retry":
		if event.Attempts < w.maxAttempts {
			return w.repository.Retry(ctx, event.ID, "retry", now.Add(auditRetryDelay(event.Attempts)), now)
		}
		return w.repository.Fail(ctx, event, "dead_letter", now)
	case "conflict":
		return w.repository.Fail(ctx, event, "conflict", now)
	default:
		return w.repository.Fail(ctx, event, "dead_letter", now)
	}
}
func auditRetryDelay(attempt int) time.Duration {
	switch attempt {
	case 1:
		return 30 * time.Second
	case 2:
		return 2 * time.Minute
	case 3:
		return 5 * time.Minute
	case 4:
		return 15 * time.Minute
	case 5:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

func (w *Worker) notifyBulletin(ctx context.Context, event Event, now time.Time) error {
	if w.notifications == nil {
		return terminalError{fmt.Errorf("notification client is not configured")}
	}
	var payload BulletinNotificationPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.IssueID == "" || payload.ActorID == "" || payload.Name == "" || len(payload.Translations) == 0 {
		return terminalError{fmt.Errorf("invalid bulletin notification payload")}
	}
	if err := w.notifications.QueueBulletinNotification(ctx, payload); err != nil {
		return err
	}
	return w.repository.CompleteNotification(ctx, event, now)
}

func (w *Worker) publish(ctx context.Context, event Event, now time.Time) error {
	var payload PublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return terminalError{err}
	}
	if _, ok := bulletins.NormalizeBulletinEdition(payload.Series, payload.Locale); !ok {
		return terminalError{fmt.Errorf("unsupported bulletin edition")}
	}
	asset, err := w.assets.Get(ctx, payload.AssetID)
	if err != nil {
		return err
	}
	if asset.OwnerService != "hhc-web-api" || asset.Namespace != "cms.weekly.pdf" ||
		asset.OwnerType != "bulletin_issue" || asset.OwnerID != payload.IssueID || asset.Locale != payload.Locale {
		return terminalError{fmt.Errorf("asset owner mismatch")}
	}
	if err := readyAsset(asset); err != nil {
		return err
	}
	if err := w.repository.CompletePublish(ctx, event, "", "", now); err != nil {
		return w.handlePublishFailure(ctx, event, payload.AssetID, "", err, now)
	}
	return nil
}

func readyAsset(asset Asset) error {
	if asset.UploadStatus != "completed" {
		return terminalError{fmt.Errorf("asset upload status %s", asset.UploadStatus)}
	}
	switch asset.ScanStatus {
	case "infected", "failed":
		return terminalError{fmt.Errorf("asset scan status %s", asset.ScanStatus)}
	case "pending":
		return assetPendingError{fmt.Errorf("asset scan pending")}
	case "clean":
	default:
		return terminalError{fmt.Errorf("asset scan status %s", asset.ScanStatus)}
	}
	switch asset.ProcessingStatus {
	case "ready", "not_required":
		return nil
	case "pending":
		return assetPendingError{fmt.Errorf("asset processing pending")}
	default:
		return terminalError{fmt.Errorf("asset processing status %s", asset.ProcessingStatus)}
	}
}

func (w *Worker) publishNews(ctx context.Context, event Event, now time.Time) error {
	var payload ContentPublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return terminalError{err}
	}
	references := newsPublishReferences(payload)
	for _, reference := range references {
		asset, err := w.assets.Get(ctx, reference.AssetID)
		if err != nil {
			return err
		}
		if err := validNewsAsset(asset, payload.ContentID, reference.Usage); err != nil {
			return err
		}
		if err := readyAsset(asset); err != nil {
			return err
		}
	}
	published := make([]PublishedAsset, 0, len(references))
	for _, reference := range references {
		key := "news:" + payload.ContentID + ":publish:v" + fmt.Sprint(payload.AggregateVersion)
		switch reference.Usage {
		case "home":
			key = "news:" + payload.ContentID + ":publish:home:v" + fmt.Sprint(payload.AggregateVersion)
		case "inline":
			key = "news:" + payload.ContentID + ":publish:inline:" + reference.AssetID + ":v" + fmt.Sprint(payload.AggregateVersion)
		}
		grant, err := w.assets.CreatePublicGrant(ctx, reference.AssetID, key)
		if err != nil {
			return w.handleContentPublishFailure(ctx, event, published, err, now)
		}
		published = append(published, PublishedAsset{Usage: reference.Usage, AssetID: reference.AssetID, GrantID: grant.ID, PublicURL: w.assets.PublicURL(reference.AssetID)})
	}
	if err := w.repository.CompleteContentPublish(ctx, event, published, now); err != nil {
		return w.handleContentPublishFailure(ctx, event, published, err, now)
	}
	return nil
}

func (w *Worker) publishHome(ctx context.Context, event Event, now time.Time) error {
	var payload ContentPublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.ContentID == "" || payload.AssetID == "" || payload.AggregateVersion < 1 {
		return terminalError{fmt.Errorf("invalid Home publication payload")}
	}
	asset, err := w.assets.Get(ctx, payload.AssetID)
	if err != nil {
		return err
	}
	if asset.ID != payload.AssetID || asset.Namespace != "cms.home.banner" || asset.OwnerService != "hhc-web-api" || asset.OwnerType != "page" || asset.OwnerID != payload.ContentID || asset.Purpose != "home_banner" || asset.DetectedMIMEType != "image/jpeg" {
		return terminalError{fmt.Errorf("Home Banner contract mismatch")}
	}
	if err := readyAsset(asset); err != nil {
		return err
	}
	if asset.ProcessingStatus != "not_required" {
		return terminalError{fmt.Errorf("Home Banner processing status %s", asset.ProcessingStatus)}
	}
	if err := w.assets.ValidateHomeBanner(ctx, asset.ID, asset.ChecksumSHA256); err != nil {
		return err
	}
	grant, err := w.assets.CreatePublicGrant(ctx, payload.AssetID, "home:"+payload.ContentID+":publish:v"+fmt.Sprint(payload.AggregateVersion))
	if err != nil {
		return compensationError{err}
	}
	published := []PublishedAsset{{Usage: "banner", AssetID: payload.AssetID, GrantID: grant.ID, PublicURL: w.assets.PublicURL(payload.AssetID)}}
	if err := w.repository.CompleteContentPublish(ctx, event, published, now); err != nil {
		return w.handleContentPublishFailure(ctx, event, published, err, now)
	}
	return nil
}

func newsPublishReferences(payload ContentPublishPayload) []PublishedAsset {
	references := make([]PublishedAsset, 0, 2+len(payload.InlineAssetIDs))
	if payload.AssetID != "" {
		references = append(references, PublishedAsset{Usage: "detail", AssetID: payload.AssetID})
	}
	if payload.HomeAssetID != "" {
		references = append(references, PublishedAsset{Usage: "home", AssetID: payload.HomeAssetID})
	}
	for _, assetID := range payload.InlineAssetIDs {
		references = append(references, PublishedAsset{Usage: "inline", AssetID: assetID})
	}
	return references
}

func validNewsAsset(asset Asset, contentID, usage string) error {
	if asset.OwnerService != "hhc-web-api" || asset.Namespace != "cms.news.cover" || asset.OwnerType != "news" || asset.OwnerID != contentID {
		return terminalError{fmt.Errorf("asset owner mismatch")}
	}
	switch usage {
	case "detail":
		if asset.Purpose != "" && asset.Purpose != "news_cover" && asset.Purpose != "news_detail_cover" {
			return terminalError{fmt.Errorf("asset purpose mismatch")}
		}
	case "home":
		if asset.Purpose != "news_home_cover" {
			return terminalError{fmt.Errorf("asset purpose mismatch")}
		}
	case "inline":
		if asset.Purpose != "statement_inline" {
			return terminalError{fmt.Errorf("asset purpose mismatch")}
		}
	default:
		return terminalError{fmt.Errorf("asset usage is invalid")}
	}
	return nil
}

func (w *Worker) handlePublishFailure(ctx context.Context, event Event, assetID, grantID string, cause error, now time.Time) error {
	if !errors.Is(cause, ErrStalePublication) {
		if event.Attempts < w.maxAttempts {
			return cause
		}
		delivered, err := w.repository.EventDelivered(ctx, event.ID)
		if err != nil {
			return compensationError{fmt.Errorf("confirm publication before compensation: %w", err)}
		}
		if delivered {
			return nil
		}
	}
	if err := w.repository.FailPublish(ctx, event, assetID, grantID, safeError(cause), now); err != nil {
		return compensationError{fmt.Errorf("persist publication compensation: %w", err)}
	}
	return nil
}

func (w *Worker) handleContentPublishFailure(ctx context.Context, event Event, assets []PublishedAsset, cause error, now time.Time) error {
	if !errors.Is(cause, ErrStalePublication) {
		if event.Attempts < w.maxAttempts {
			return cause
		}
		delivered, err := w.repository.EventDelivered(ctx, event.ID)
		if err != nil {
			return compensationError{fmt.Errorf("confirm publication before compensation: %w", err)}
		}
		if delivered {
			return nil
		}
	}
	if err := w.repository.FailContentPublish(ctx, event, assets, safeError(cause), now); err != nil {
		return compensationError{fmt.Errorf("persist publication compensation: %w", err)}
	}
	return nil
}

func (w *Worker) unpublish(ctx context.Context, event Event, now time.Time) error {
	var payload UnpublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return terminalError{err}
	}
	if payload.GrantID != "" {
		if err := w.assets.RevokeGrant(ctx, payload.AssetID, payload.GrantID); err != nil && !errors.Is(err, ErrGrantNotFound) {
			return compensationError{err}
		}
	}
	return w.repository.CompleteUnpublish(ctx, event, now)
}

func (w *Worker) unpublishNews(ctx context.Context, event Event, now time.Time) error {
	var payload ContentUnpublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return terminalError{err}
	}
	if event.ActorID == "" {
		return terminalError{ErrStalePublication}
	}
	ctx = auditclient.WithProvenance(ctx, event.ActorID, event.ID)
	assets := payload.Assets
	if len(assets) == 0 && (payload.AssetID != "" || payload.GrantID != "") {
		assets = []PublishedAsset{{Usage: "detail", AssetID: payload.AssetID, GrantID: payload.GrantID}}
	}
	for _, asset := range assets {
		if asset.AssetID == "" || asset.GrantID == "" {
			return terminalError{fmt.Errorf("published grant reference is missing")}
		}
		if err := w.assets.RevokeGrant(ctx, asset.AssetID, asset.GrantID); err != nil && !errors.Is(err, ErrGrantNotFound) {
			return compensationError{err}
		}
	}
	return w.repository.CompleteContentUnpublish(ctx, event, now)
}

func (w *Worker) revokeGrant(ctx context.Context, event Event, now time.Time) error {
	var payload ContentUnpublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return terminalError{err}
	}
	if payload.AssetID == "" || payload.GrantID == "" {
		return terminalError{fmt.Errorf("asset grant reference is missing")}
	}
	if err := w.assets.RevokeGrant(ctx, payload.AssetID, payload.GrantID); err != nil && !errors.Is(err, ErrGrantNotFound) {
		return compensationError{err}
	}
	return w.repository.Complete(ctx, event.ID, now)
}

func (w *Worker) retire(ctx context.Context, event Event, now time.Time) error {
	var payload UnpublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return terminalError{err}
	}
	if event.EventType == "bulletin.asset.retire" {
		if _, ok := bulletins.NormalizeBulletinEdition(payload.Series, payload.Locale); !ok {
			return terminalError{fmt.Errorf("unsupported bulletin edition")}
		}
	}
	if payload.AssetID == "" {
		return terminalError{fmt.Errorf("retiring asset id is missing")}
	}
	if payload.GrantID != "" {
		if err := w.assets.RevokeGrant(ctx, payload.AssetID, payload.GrantID); err != nil && !errors.Is(err, ErrGrantNotFound) {
			return compensationError{err}
		}
	}
	if err := w.assets.Delete(ctx, payload.AssetID); err != nil && !errors.Is(err, ErrGrantNotFound) {
		return compensationError{err}
	}
	return w.repository.Complete(ctx, event.ID, now)
}

type terminalError struct{ error }
type compensationError struct{ error }
type assetPendingError struct{ error }

var ErrGrantNotFound = errors.New("grant not found")

func safeError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}
