package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func scanCapture(row *sql.Row) (recordings.Capture, error) {
	var c recordings.Capture
	var raw []byte
	err := row.Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, recordings.ErrNotFound
	}
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}
func saveCapture(ctx context.Context, tx *sql.Tx, c recordings.Capture) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_capture SET payload_json=$2,capture_id=NULLIF($3,''),active=$4 WHERE id=$1 AND (payload_json,capture_id,active) IS DISTINCT FROM ($2::jsonb,NULLIF($3,''),$4::boolean)`, c.IntentID, raw, c.Status.CaptureID, c.Active())
	return recordingConflict(err)
}
func (r *RecordingRepository) PrepareCapture(ctx context.Context, c recordings.Capture, now time.Time) (recordings.Capture, error) {
	tx, finishTx, err := r.sourceTransaction(ctx)
	if err != nil {
		return c, err
	}
	defer finishTx()

	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "recording-capture:"+c.ActorID+":"+c.Create.OperationKey); err != nil {
		return c, err
	}
	existing, err := scanCapture(tx.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.member_recording_capture WHERE actor_id::text=$1 AND operation_key=$2`, c.ActorID, c.Create.OperationKey))
	if err == nil {
		if existing.RecordingID != c.RecordingID || existing.Create != c.Create {
			return c, recordings.ErrConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, recordings.ErrNotFound) {
		return c, err
	}
	var b1active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture WHERE input_active AND broadcast_epoch IS NOT NULL) OR EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast_source WHERE prepared)`).Scan(&b1active); err != nil {
		return c, err
	}
	if b1active {
		return c, recordings.ErrConflict
	}
	var version, generation int64
	var status string
	var expires time.Time
	var occupied bool
	err = tx.QueryRowContext(ctx, `SELECT version,human_edit_generation,status,COALESCE(expires_at,draft_expires_at),primary_asset_version_id IS NOT NULL OR source_id IS NOT NULL OR package_id IS NOT NULL FROM hhc_web.member_recording WHERE id::text=$1 AND deleted_at IS NULL AND NOT media_terminal AND NOT is_broadcast FOR UPDATE`, c.RecordingID).Scan(&version, &generation, &status, &expires, &occupied)
	if errors.Is(err, sql.ErrNoRows) {
		return c, recordings.ErrNotFound
	}
	if err != nil {
		return c, err
	}
	if version != c.Create.ExpectedVersion {
		return c, recordings.ErrPrecondition
	}
	if occupied || status != "draft" || !now.Before(expires) {
		return c, recordings.ErrConflict
	}
	c.Generation = generation
	c.Status.RecordingVersion = version
	if c.Create.LiveEnabled {
		defaults, e := defaultLiveCover(ctx, tx, true)
		if e != nil {
			return c, e
		}
		defaults.Settings.InheritedRevision = defaults.Settings.Revision
		defaults.Settings.Revision = 1
		defaults.ReferenceID = platform.NewID()
		defaults.AutoReferenceID = platform.NewID()
		if defaults.Settings.Selection.Mode == "custom" {
			defaults.Settings.CoverState = "pending"
			if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_live_cover_reference(id,target,cover_id,recording_id) VALUES($1,$2,$3,$4)`, defaults.ReferenceID, c.IntentID, defaults.Settings.Selection.UploadID, c.RecordingID); e != nil {
				return c, e
			}
		}
		c.LiveCover = &defaults
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_capture(id,recording_id,actor_id,operation_key,active,payload_json) VALUES($1,$2,$3,$4,true,$5)`, c.IntentID, c.RecordingID, c.ActorID, c.Create.OperationKey, raw)
	if err != nil {
		return c, recordingConflict(err)
	}
	return c, tx.Commit()
}
func (r *RecordingRepository) GetCapture(ctx context.Context, id, recording string) (recordings.Capture, error) {
	return scanCapture(r.db.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.member_recording_capture WHERE (id::text=$1 OR capture_id=$1) AND ($2='' OR recording_id::text=$2)`, id, recording))
}

// Viewer observations do not need the producer's growing operation journal.
const captureObservationJSON = `(payload_json - 'Operations') #- '{Status,objects}' || '{"Operations":{}}'::jsonb`

func (r *RecordingRepository) GetLiveCapture(ctx context.Context, id, recording string) (recordings.Capture, error) {
	return scanCapture(r.db.QueryRowContext(ctx, `SELECT `+captureObservationJSON+` FROM hhc_web.member_recording_capture WHERE (id::text=$1 OR capture_id=$1) AND recording_id::text=$2`, id, recording))
}
func lockCapture(ctx context.Context, tx *sql.Tx, id string) (recordings.Capture, error) {
	return lockCaptureRows(ctx, tx, id, false)
}

func shareCapture(ctx context.Context, tx *sql.Tx, id string) (recordings.Capture, error) {
	return lockCaptureRows(ctx, tx, id, true)
}

func lockCaptureRows(ctx context.Context, tx *sql.Tx, id string, shared bool) (recordings.Capture, error) {
	locking := " FOR UPDATE"
	if shared {
		// Key-share locks would let non-key stop/deletion updates overtake issuance.
		locking = " FOR SHARE"
	}
	// All capture/scope/close operations lock the parent before the aggregate.
	var recording string
	if err := tx.QueryRowContext(ctx, `SELECT recording_id::text FROM hhc_web.member_recording_capture WHERE id::text=$1 OR capture_id=$1`, id).Scan(&recording); errors.Is(err, sql.ErrNoRows) {
		return recordings.Capture{}, recordings.ErrNotFound
	} else if err != nil {
		return recordings.Capture{}, err
	}
	var version int64
	var deleted, mediaExpires sql.NullTime
	var terminal bool
	if err := tx.QueryRowContext(ctx, `SELECT version,deleted_at,media_terminal,expires_at FROM hhc_web.member_recording WHERE id::text=$1`+locking, recording).Scan(&version, &deleted, &terminal, &mediaExpires); err != nil {
		return recordings.Capture{}, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT 1 FROM hhc_web.member_recording_broadcast WHERE recording_id::text=$1`+locking, recording); err != nil {
		return recordings.Capture{}, err
	}
	projection := "payload_json"
	if shared {
		projection = captureObservationJSON
	}
	c, err := scanCapture(tx.QueryRowContext(ctx, `SELECT `+projection+` FROM hhc_web.member_recording_capture WHERE id::text=$1 OR capture_id=$1`+locking, id))
	if err != nil {
		return c, err
	}
	c.Status.RecordingVersion = version
	if mediaExpires.Valid {
		deadline := mediaExpires.Time.UTC()
		c.MediaExpiresAt = &deadline
	}
	if mediaExpires.Valid && c.Status.ReplayUntil != nil && mediaExpires.Time.Before(*c.Status.ReplayUntil) {
		deadline := mediaExpires.Time.UTC()
		c.Status.ReplayUntil = &deadline
	}
	if deleted.Valid {
		c.Terminate("aborted", "recording_deleted", deleted.Time.UTC())
		reason := "recording_deleted"
		c.Status.TerminalReason = &reason
	} else if terminal {
		c.Terminate("failed", "recording_media_unavailable", time.Now().UTC())
		reason := "recording_media_unavailable"
		c.Status.TerminalReason = &reason
	}
	return c, nil
}
func (r *RecordingRepository) UpdateCapture(ctx context.Context, id string, fn func(*recordings.Capture) error) (recordings.Capture, error) {
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return recordings.Capture{}, err
	}
	defer finishTx()
	c, err := lockCapture(ctx, tx, id)
	if err != nil {
		return c, err
	}
	if err = fn(&c); err != nil {
		return c, err
	}
	now := time.Now().UTC()
	if c.BroadcastEpoch == 0 && c.InboxLiveReady(now) {
		if err = recordInboxPublication(ctx, tx, r.inboxPublicationsAfter, "recording.published", c.RecordingID, "", "", c.Status.RecordingVersion, now); err != nil {
			return c, err
		}
	}
	if c.LiveCover != nil && c.Status.StopAcceptedAt != nil && c.LiveCover.Settings.CoverState == "ready" {
		c.LiveCover.AutoFrozen = true
	}
	if c.Status.ReplayUntil != nil {
		var mediaExpires sql.NullTime
		if err = tx.QueryRowContext(ctx, `SELECT expires_at FROM hhc_web.member_recording WHERE id::text=$1`, c.RecordingID).Scan(&mediaExpires); err != nil {
			return c, err
		}
		if mediaExpires.Valid && mediaExpires.Time.Before(*c.Status.ReplayUntil) {
			deadline := mediaExpires.Time.UTC()
			c.Status.ReplayUntil = &deadline
		}
	}
	if err = saveCapture(ctx, tx, c); err != nil {
		return c, err
	}
	if c.NormalEnd && !c.Terminal() && c.Status.PackageID != nil {
		result, err := tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET package_id=$2 WHERE id::text=$1 AND deleted_at IS NULL AND NOT media_terminal AND source_id IS NULL AND primary_asset_version_id IS NULL AND (package_id IS NULL OR package_id=$2)`, c.RecordingID, *c.Status.PackageID)
		if err != nil {
			return c, err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return c, recordings.ErrConflict
		}
	}
	return c, tx.Commit()
}
func (r *RecordingRepository) ListPendingCaptures(ctx context.Context, now time.Time, limit int) ([]recordings.Capture, error) {
	if limit < 1 || limit > 1000 {
		return nil, recordings.ErrInvalid
	}
	// Acknowledged producer deletion fences late creates; retain their history without retrying impossible compensation.
	rows, err := r.db.QueryContext(ctx, `WITH picked AS (
 SELECT c.id FROM hhc_web.member_recording_capture c
 WHERE (c.active OR c.payload_json->'Status'->>'liveEnabled'='true' OR jsonb_path_exists(c.payload_json,'$.Operations.* ? (@.Pending == true)'))
 AND NOT EXISTS (
  SELECT 1 FROM hhc_web.member_recording r WHERE r.id=c.recording_id
  AND r.deleted_at IS NOT NULL AND r.deletion_delivered_at IS NOT NULL
  AND NOT c.active AND c.payload_json->'Status'->>'state' IN ('failed','expired','aborted')
 )
 ORDER BY c.checked_at NULLS FIRST,c.id LIMIT $1 FOR UPDATE SKIP LOCKED
) UPDATE hhc_web.member_recording_capture SET checked_at=$2 WHERE id IN(SELECT id FROM picked) RETURNING payload_json`, limit, now)
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
func closeCapturePublicationTx(ctx context.Context, tx *sql.Tx, id string, deletedAt ...time.Time) error {
	if err := closeBroadcastPublicationTx(ctx, tx, id); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload_json FROM hhc_web.member_recording_capture WHERE recording_id::text=$1 FOR UPDATE`, id)
	if err != nil {
		return err
	}
	var captures []recordings.Capture
	for rows.Next() {
		var raw []byte
		var c recordings.Capture
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &c)
		}
		if err != nil {
			rows.Close()
			return err
		}
		captures = append(captures, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range captures {
		c.ClosePublication()
		if len(deletedAt) > 0 {
			c.Terminate("aborted", "recording_deleted", deletedAt[0])
		}
		if err = saveCapture(ctx, tx, c); err != nil {
			return err
		}
	}
	return nil
}
func (r *RecordingRepository) RegisterCaptureScope(ctx context.Context, recording, capture, user, scope string, now time.Time) error {
	return r.WithCaptureScope(ctx, recording, capture, user, scope, now, false, func(recordings.Capture, string) error { return nil })
}
