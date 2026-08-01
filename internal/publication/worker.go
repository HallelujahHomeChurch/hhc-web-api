package publication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type Worker struct {
	repository  Repository
	assets      AssetClient
	maxAttempts int
	now         func() time.Time
}

const assetReadinessDeadline = 30 * time.Minute

func NewWorker(repository Repository, assets AssetClient, maxAttempts int) *Worker {
	return &Worker{repository: repository, assets: assets, maxAttempts: maxAttempts, now: time.Now}
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
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
	case "bulletin.publish.ensure_asset":
		err = w.publish(ctx, event, now)
	case "bulletin.unpublish.revoke_asset":
		err = w.unpublish(ctx, event, now)
	case "bulletin.asset.retire":
		err = w.retire(ctx, event, now)
	case "news.publish.ensure_asset":
		err = w.publishNews(ctx, event, now)
	case "news.unpublish.revoke_asset":
		err = w.unpublishNews(ctx, event, now)
	case "asset.grant.revoke":
		err = w.revokeGrant(ctx, event, now)
	default:
		err = terminalError{fmt.Errorf("unsupported outbox event %s", event.EventType)}
	}
	if err == nil {
		return true, nil
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
	if errors.As(err, &terminal) || (event.Attempts >= w.maxAttempts && !errors.As(err, &compensation)) {
		return true, w.repository.Fail(ctx, event, safeError(err), now)
	}
	backoff := time.Duration(1<<min(event.Attempts-1, 5)) * 5 * time.Second
	return true, w.repository.Retry(ctx, event.ID, safeError(err), now.Add(backoff), now)
}

func (w *Worker) publish(ctx context.Context, event Event, now time.Time) error {
	var payload PublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return terminalError{err}
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
	grant, err := w.assets.CreatePublicGrant(ctx, payload.AssetID, "bulletin:"+payload.IssueID+":"+payload.Locale+":v"+fmt.Sprint(payload.AggregateVersion))
	if err != nil {
		return compensationError{err}
	}
	if err := w.repository.CompletePublish(ctx, event, grant.ID, w.assets.PublicURL(payload.AssetID), now); err != nil {
		return w.handlePublishFailure(ctx, event, payload.AssetID, grant.ID, err, now)
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
	asset, err := w.assets.Get(ctx, payload.AssetID)
	if err != nil {
		return err
	}
	if asset.OwnerService != "hhc-web-api" || asset.Namespace != "cms.news.cover" ||
		asset.OwnerType != "news" || asset.OwnerID != payload.ContentID {
		return terminalError{fmt.Errorf("asset owner mismatch")}
	}
	if err := readyAsset(asset); err != nil {
		return err
	}
	grant, err := w.assets.CreatePublicGrant(ctx, payload.AssetID, "news:"+payload.ContentID+":publish:v"+fmt.Sprint(payload.AggregateVersion))
	if err != nil {
		return compensationError{err}
	}
	if err := w.repository.CompleteContentPublish(ctx, event, grant.ID, w.assets.PublicURL(payload.AssetID), now); err != nil {
		return w.handlePublishFailure(ctx, event, payload.AssetID, grant.ID, err, now)
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

func (w *Worker) unpublish(ctx context.Context, event Event, now time.Time) error {
	var payload UnpublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return terminalError{err}
	}
	if payload.GrantID == "" {
		return terminalError{fmt.Errorf("published grant id is missing")}
	}
	if err := w.assets.RevokeGrant(ctx, payload.AssetID, payload.GrantID); err != nil && !errors.Is(err, ErrGrantNotFound) {
		return compensationError{err}
	}
	return w.repository.CompleteUnpublish(ctx, event, now)
}

func (w *Worker) unpublishNews(ctx context.Context, event Event, now time.Time) error {
	var payload ContentUnpublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return terminalError{err}
	}
	if payload.GrantID == "" {
		return terminalError{fmt.Errorf("published grant id is missing")}
	}
	if err := w.assets.RevokeGrant(ctx, payload.AssetID, payload.GrantID); err != nil && !errors.Is(err, ErrGrantNotFound) {
		return compensationError{err}
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
