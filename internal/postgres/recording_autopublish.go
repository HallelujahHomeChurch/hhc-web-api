package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (r *RecordingRepository) enqueueCaptureAudit(ctx context.Context, tx *sql.Tx, c recordings.Capture, action string, now time.Time) error {
	if r.auditOutbox == nil {
		return recordings.ErrAuditUnavailable
	}
	event, err := auditclient.NewEventForPrincipal(action, c.RecordingID, "service", "hhc-web-api", "capture:"+c.IntentID, now, auditclient.Metadata{"authorizerId": c.ActorID, "captureId": c.Status.CaptureID, "intentId": c.IntentID})
	if err == nil {
		_, err = r.auditOutbox.EnqueueTx(ctx, tx, event, now)
	}
	if err != nil {
		return recordings.ErrAuditUnavailable
	}
	return nil
}

// The parent lock is shared with cancellation and all manual recording writes.
// The terminal intent and publication effects commit together, providing replay
// without another queue or a synthetic human request.
func (r *RecordingRepository) PublishCapture(ctx context.Context, id string, checkedAt time.Time, allowed bool, now time.Time) error {
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return err
	}
	defer finishTx()
	c, err := lockCapture(ctx, tx, id)
	if err != nil {
		return err
	}
	if c.Status.AutoPublish != "pending" {
		return tx.Commit()
	}
	if c.BroadcastEpoch > 0 {
		b, e := scanBroadcast(tx.QueryRowContext(ctx, `SELECT b.payload_json,r.title_override,r.description,b.source_managed FROM hhc_web.member_recording_broadcast b JOIN hhc_web.member_recording r ON r.id=b.recording_id WHERE b.recording_id::text=$1`, c.RecordingID))
		if e != nil {
			return e
		}
		if b.Revoked || b.PublicationWithdrawn || b.RangePending || b.Projection == nil || b.Projection.State != "ready" || b.View.Boundaries.EndSequenceExclusive == nil {
			return recordings.ErrConflict
		}
	}
	var generation int64
	if err = tx.QueryRowContext(ctx, `SELECT human_edit_generation FROM hhc_web.member_recording WHERE id::text=$1`, c.RecordingID).Scan(&generation); err != nil {
		return err
	}
	c.PermissionCheckedAt = &checkedAt
	c.AutoAttempts++
	if !allowed || generation != c.Generation {
		c.Status.AutoPublish = "blocked"
		if err = r.enqueueCaptureAudit(ctx, tx, c, "cms.recording.capture.block", now); err != nil {
			return err
		}
	} else {
		current, err := scanRecording(tx.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM hhc_web.member_recording WHERE id::text=$1`, c.RecordingID))
		if err != nil {
			return err
		}
		if c.Terminal() || !c.NormalEnd || c.Status.StopAcceptedAt == nil || c.Status.State != "ready" || c.Status.PackageID == nil || *c.Status.PackageID != c.Status.CaptureID || current.PackageID != c.Status.CaptureID || c.VerifiedReadyAt == nil || current.ReadyAt == nil || !c.VerifiedReadyAt.Equal(*current.ReadyAt) || current.MediaTerminal || current.Expired(now) || checkedAt.IsZero() || checkedAt.After(now) {
			return recordings.ErrConflict
		}
		if err = r.enqueueCaptureAudit(ctx, tx, c, "cms.recording.capture.publish", now); err != nil {
			return err
		}
		// Existing notification command is authorizer/account-bound; execution audit
		// separately records the fixed CMS service principal.
		current, err = r.mutateTx(ctx, tx, c.RecordingID, current.Version, c.ActorID, now, "cms.recording.capture.publish", publishRecordingQuery, "hhc-web-api", now)
		if err != nil {
			return err
		}
		c.Status.AutoPublish = "published"
		c.Status.RecordingVersion = current.Version
	}
	c.AutoNextAttemptAt = nil
	if err = saveCapture(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *RecordingRepository) ListAutoPublicationCaptures(ctx context.Context, now time.Time, limit int) ([]recordings.Capture, error) {
	if limit < 1 || limit > 1000 {
		return nil, recordings.ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, `SELECT payload_json FROM hhc_web.member_recording_capture WHERE payload_json->'Status'->>'autoPublish'='pending' AND payload_json->'Status'->>'state'='ready' AND (payload_json->>'AutoNextAttemptAt' IS NULL OR (payload_json->>'AutoNextAttemptAt')::timestamptz<=$1) ORDER BY COALESCE((payload_json->>'AutoNextAttemptAt')::timestamptz,checked_at),id LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []recordings.Capture{}
	for rows.Next() {
		var raw []byte
		var c recordings.Capture
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
