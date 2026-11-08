package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (r *RecordingRepository) LatestRecordingCapture(ctx context.Context, id string) (recordings.Capture, error) {
	return readSubjectLatestRecordingCapture(ctx, r.db, id)
}
func readSubjectLatestRecordingCapture(ctx context.Context, q recordingRowReader, id string) (recordings.Capture, error) {
	return scanCapture(q.QueryRowContext(ctx, `SELECT `+captureObservationJSON+` FROM hhc_web.member_recording_capture WHERE recording_id::text=$1 ORDER BY (payload_json->'Status'->>'createdAt')::timestamptz DESC,id DESC LIMIT 1`, id))
}
func (r *RecordingRepository) ListLiveCaptures(ctx context.Context, now time.Time) ([]recordings.Capture, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT (c.payload_json - 'Operations') #- '{Status,objects}' || '{"Operations":{}}'::jsonb FROM hhc_web.member_recording_capture c JOIN hhc_web.member_recording r ON r.id=c.recording_id WHERE r.deleted_at IS NULL AND NOT r.media_terminal AND c.capture_id IS NOT NULL AND c.payload_json->'Status'->>'liveEnabled'='true' AND (NOT r.is_broadcast OR EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast b WHERE b.recording_id=r.id AND b.payload_json->'View'->>'phase' IN ('live','end_pending') AND b.payload_json->>'Revoked'='false')) ORDER BY c.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []recordings.Capture{}
	for rows.Next() {
		var c recordings.Capture
		var raw []byte
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

// Inspect before external issuance without registering a scope or holding locks.
func (r *RecordingRepository) InspectCaptureScope(ctx context.Context, recording, capture, user, scope string, now time.Time, trace bool) (recordings.Capture, error) {
	var current recordings.Capture
	err := r.withCaptureScope(ctx, recording, capture, user, scope, now, trace, false, func(c recordings.Capture, _ string) error { current = c; return nil })
	return current, err
}

// Final acceptance holds subject -> recording -> capture fences through commit.
// The callback must not perform network I/O; revalidate externally issued grants here.
func (r *RecordingRepository) WithCaptureScope(ctx context.Context, recording, capture, user, scope string, now time.Time, trace bool, issue func(recordings.Capture, string) error) error {
	return r.withCaptureScope(ctx, recording, capture, user, scope, now, trace, true, issue)
}

func (r *RecordingRepository) withCaptureScope(ctx context.Context, recording, capture, user, scope string, now time.Time, trace, commit bool, issue func(recordings.Capture, string) error) error {
	tx, finishTx, err := r.beginSubjectReadTransaction(ctx)
	if err != nil {
		return err
	}
	defer finishTx()
	if err = shareWatermarkSubject(ctx, tx, user); err != nil {
		return err
	}
	blocked, err := watermarkRestricted(ctx, tx, user)
	if err != nil {
		return err
	}
	if blocked {
		return bulletinwatermark.ErrRestricted
	}
	c, err := shareCapture(ctx, tx, capture)
	if err != nil {
		if errors.Is(err, recordings.ErrNotFound) {
			return captureScopeNotFound(ctx, "capture_missing")
		}
		return err
	}
	if c.RecordingID != recording {
		return captureScopeNotFound(ctx, "recording_mismatch")
	}
	c.Expire(now)
	c.DeriveLiveState(now)
	var reasons []string
	if !c.LiveAvailable(now) {
		reasons = append(reasons, "capture_unavailable")
	}
	if c.BroadcastEpoch != 0 {
		var allowed bool
		if err = tx.QueryRowContext(ctx, `SELECT payload_json->'View'->>'phase' IN ('live','end_pending','processing','archived') AND payload_json->>'Revoked'='false' AND COALESCE(payload_json->>'MemberState','blocked') IN ('live','vod') FROM hhc_web.member_recording_broadcast WHERE recording_id::text=$1`, recording).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			reasons = append(reasons, "broadcast_denied")
		}
	}
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT user_id::text FROM hhc_web.member_recording_capture_scope WHERE capture_intent_id=$1 AND scope_id=$2 FOR UPDATE`, c.IntentID, scope).Scan(&owner)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return err
	}
	if missing && c.BroadcastEpoch != 0 {
		var admitting bool
		if err = tx.QueryRowContext(ctx, `SELECT payload_json->'View'->>'phase' IN ('live','end_pending') FROM hhc_web.member_recording_broadcast WHERE recording_id::text=$1`, recording).Scan(&admitting); err != nil {
			return err
		}
		if !admitting {
			reasons = append(reasons, "admission_closed")
		}
	}
	if !missing && owner != user {
		reasons = append(reasons, "scope_owner_mismatch")
	}
	if missing && c.Status.StopAcceptedAt != nil {
		reasons = append(reasons, "scope_missing_after_stop")
	}
	if len(reasons) > 0 {
		// Release shared fences before persisting terminal/expiry side effects.
		if err = tx.Rollback(); err != nil {
			return err
		}
		finishTx()
		if _, err = r.UpdateCapture(ctx, c.IntentID, func(current *recordings.Capture) error {
			current.Expire(now)
			current.DeriveLiveState(now)
			return nil
		}); err != nil {
			return err
		}
		return captureScopeNotFound(ctx, reasons...)
	}
	if missing && commit {
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_capture_scope(capture_intent_id,scope_id,user_id,registered_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, c.IntentID, scope, user, now); err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `SELECT user_id::text FROM hhc_web.member_recording_capture_scope WHERE capture_intent_id=$1 AND scope_id=$2 FOR UPDATE`, c.IntentID, scope).Scan(&owner); err != nil {
			return err
		}
		if owner != user {
			return captureScopeNotFound(ctx, "scope_owner_changed")
		}
	}
	code := ""
	if trace {
		var receipt recordings.WatermarkReceipt
		if commit {
			receipt, err = ensureRecordingWatermarkTx(ctx, tx, user, recording, c.Status.CaptureID, now, recordings.NewWatermarkCode)
		} else {
			receipt, err = readRecordingWatermarkTx(ctx, tx, user, recording, c.Status.CaptureID, now)
			if errors.Is(err, sql.ErrNoRows) {
				err = nil
			}
		}
		if err != nil {
			return err
		}
		code = receipt.Code
	}
	if err = issue(c, code); err != nil {
		return err
	}
	if !commit {
		return nil
	}
	return tx.Commit()
}

// Reasons are internal constants; callers still receive the privacy-preserving 404.
func captureScopeNotFound(ctx context.Context, reasons ...string) error {
	provenance, _ := auditclient.ProvenanceFromContext(ctx)
	slog.WarnContext(ctx, "recording_capture_scope_denied", "reasons", reasons, "request_id", provenance.RequestID)
	return recordings.ErrNotFound
}
