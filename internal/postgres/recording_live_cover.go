package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func defaultLiveCover(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, lock bool) (recordings.CaptureLiveCover, error) {
	var v recordings.CaptureLiveCover
	var raw []byte
	query := `SELECT payload_json FROM hhc_web.member_recording_live_cover_default WHERE singleton`
	if lock {
		query += " FOR UPDATE"
	}
	err := q.QueryRowContext(ctx, query).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &v)
	}
	return v, err
}
func (r *RecordingRepository) GetLiveCoverSettings(ctx context.Context, target string) (recordings.CaptureLiveCover, error) {
	if target == "defaults" {
		return defaultLiveCover(ctx, r.db, false)
	}
	c, err := r.GetCapture(ctx, target, "")
	if err != nil {
		return recordings.CaptureLiveCover{}, err
	}
	if c.LiveCover == nil || c.Terminal() {
		return recordings.CaptureLiveCover{}, recordings.ErrNotFound
	}
	return *c.LiveCover, nil
}
func (r *RecordingRepository) PrepareLiveCoverOperation(ctx context.Context, op recordings.LiveCoverOperation, now time.Time) (recordings.LiveCoverOperation, error) {
	data, err := json.Marshal(op.Selection)
	if err != nil {
		return op, err
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return op, err
	}
	defer finishTx()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "live-cover:"+op.Actor+":"+op.Key); err != nil {
		return op, err
	}
	var old recordings.LiveCoverOperation
	var selection, receipt []byte
	err = tx.QueryRowContext(ctx, `SELECT id::text,target,expected_revision,selection_json,receipt_json FROM hhc_web.member_recording_live_cover_operation WHERE actor_id=$1 AND operation_key=$2`, op.Actor, op.Key).Scan(&old.ID, &old.Target, &old.Expected, &selection, &receipt)
	if err == nil {
		if json.Unmarshal(selection, &old.Selection) != nil || old.Target != op.Target || old.Expected != op.Expected || old.Selection != op.Selection {
			return op, recordings.ErrConflict
		}
		op.ID = old.ID
		if len(receipt) > 0 {
			var result recordings.LiveCoverSettings
			if json.Unmarshal(receipt, &result) != nil {
				return op, recordings.ErrConflict
			}
			op.Receipt = &result
		}
		return op, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return op, err
	}
	var cover recordings.CaptureLiveCover
	var scope, recording string
	if op.Target == "defaults" {
		cover, err = defaultLiveCover(ctx, tx, true)
		scope = "defaults"
	} else {
		var c recordings.Capture
		c, err = lockCapture(ctx, tx, op.Target)
		if err == nil && (c.Terminal() || c.LiveCover == nil || !c.Create.LiveEnabled || c.Status.State == "ready" || c.MediaExpiresAt != nil && !now.Before(*c.MediaExpiresAt)) {
			err = recordings.ErrNotFound
		}
		if err == nil {
			cover = *c.LiveCover
			scope = c.Status.CaptureID
			recording = c.RecordingID
		}
	}
	if err != nil {
		return op, err
	}
	if cover.Settings.Revision != op.Expected {
		return op, recordings.ErrPrecondition
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_live_cover_operation(id,target,actor_id,operation_key,expected_revision,selection_json,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, op.ID, op.Target, op.Actor, op.Key, op.Expected, data, now); err != nil {
		return op, recordingConflict(err)
	}
	if op.Selection.Mode == "custom" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_live_cover_reference(id,target,cover_id,scope,recording_id,wanted,created_at) VALUES($1,$2,$3,$4,$5,false,$6)`, op.ID, op.Target, op.Selection.UploadID, scope, recording, now); err != nil {
			return op, err
		}
	}
	return op, tx.Commit()
}
func (r *RecordingRepository) CommitLiveCoverOperation(ctx context.Context, op recordings.LiveCoverOperation, now time.Time, retain func(context.Context) error) (recordings.LiveCoverSettings, error) {
	value, err := r.commitLiveCoverOperation(ctx, op, now, false)
	if err != errCoverRetainRequired {
		return value, err
	}
	if err = retain(ctx); err != nil {
		return recordings.LiveCoverSettings{}, err
	}
	return r.commitLiveCoverOperation(ctx, op, now, true)
}
func (r *RecordingRepository) commitLiveCoverOperation(ctx context.Context, op recordings.LiveCoverOperation, now time.Time, retained bool) (recordings.LiveCoverSettings, error) {
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return recordings.LiveCoverSettings{}, err
	}
	defer finishTx()
	var cover recordings.CaptureLiveCover
	var c recordings.Capture
	if op.Target == "defaults" {
		cover, err = defaultLiveCover(ctx, tx, true)
	} else {
		c, err = lockCapture(ctx, tx, op.Target)
		if err == nil && (c.Terminal() || c.LiveCover == nil || c.Status.State == "ready") {
			err = recordings.ErrNotFound
		}
		if err == nil {
			cover = *c.LiveCover
		}
	}
	if err != nil {
		return recordings.LiveCoverSettings{}, err
	}
	var receipt []byte
	if err = tx.QueryRowContext(ctx, `SELECT receipt_json FROM hhc_web.member_recording_live_cover_operation WHERE id=$1 FOR UPDATE`, op.ID).Scan(&receipt); err != nil {
		return cover.Settings, err
	}
	if len(receipt) > 0 {
		err = json.Unmarshal(receipt, &cover.Settings)
		return cover.Settings, err
	}
	if cover.Settings.Revision != op.Expected {
		return cover.Settings, recordings.ErrPrecondition
	}
	if op.Selection.Mode == "custom" {
		var released, checked sql.NullTime
		if err = tx.QueryRowContext(ctx, `SELECT released_at,checked_at FROM hhc_web.member_recording_live_cover_reference WHERE id=$1 FOR UPDATE`, op.ID).Scan(&released, &checked); err != nil {
			return cover.Settings, err
		}
		if released.Valid || checked.Valid {
			return cover.Settings, recordings.ErrConflict
		}
	}
	if !retained {
		return cover.Settings, errCoverRetainRequired
	}
	oldReference := cover.ReferenceID
	cover.Settings.Revision++
	cover.Settings.Selection = op.Selection
	cover.Settings.ContentPath = ""
	cover.Settings.CoverID = ""
	cover.Settings.CoverState = "waiting"
	cover.PromotionReady = false
	if op.Selection.Mode == "custom" {
		cover.ReferenceID = op.ID
		cover.Settings.CoverID = op.Selection.UploadID
		cover.Settings.CoverState = "ready"
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET wanted=true,applied=true WHERE id=$1`, op.ID); err != nil {
			return cover.Settings, err
		}
	} else {
		if cover.AutoState == "ready" {
			cover.Settings.CoverID = cover.AutoUploadID
			cover.Settings.CoverState = "ready"
		}
		cover.ReferenceID = platform.NewID()
	}
	if oldReference != "" && oldReference != cover.ReferenceID {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET wanted=false WHERE id=$1`, oldReference); err != nil {
			return cover.Settings, err
		}
	}
	action, resource := "cms.recording.cover.update", c.RecordingID
	if op.Target == "defaults" {
		action, resource = "cms.site_settings.update", "singleton"
		var raw []byte
		raw, err = json.Marshal(cover)
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_default SET payload_json=$1,updated_at=$2 WHERE singleton`, raw, now)
		}
	} else {
		c.LiveCover = &cover
		err = saveCapture(ctx, tx, c)
	}
	if err != nil {
		return cover.Settings, err
	}
	if err = r.enqueueAudit(ctx, tx, action, resource, op.Actor, now); err != nil {
		return cover.Settings, err
	}
	receipt, err = json.Marshal(cover.Settings)
	if err != nil {
		return cover.Settings, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_operation SET receipt_json=$2 WHERE id=$1`, op.ID, receipt); err != nil {
		return cover.Settings, err
	}
	return cover.Settings, tx.Commit()
}
func (r *RecordingRepository) EnsureLiveCoverReference(ctx context.Context, ref recordings.LiveCoverReference) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_live_cover_reference(id,target,cover_id,scope,recording_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING`, ref.ID, ref.Target, ref.CoverID, ref.Scope, ref.RecordingID)
	return err
}
func (r *RecordingRepository) ApplyLiveCoverReference(ctx context.Context, ref recordings.LiveCoverReference, retain func(context.Context) error) error {
	err := r.applyLiveCoverReference(ctx, ref, false)
	if err != errCoverRetainRequired {
		return err
	}
	if err = retain(ctx); err != nil {
		return err
	}
	return r.applyLiveCoverReference(ctx, ref, true)
}
func (r *RecordingRepository) applyLiveCoverReference(ctx context.Context, ref recordings.LiveCoverReference, retained bool) error {
	if strings.HasPrefix(ref.Target, "broadcast:") {
		return r.applyBroadcastCoverReference(ctx, ref, retained)
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return err
	}
	defer finishTx()
	c, err := lockCapture(ctx, tx, ref.Target)
	if err != nil {
		return err
	}
	if c.Terminal() || c.LiveCover == nil {
		return recordings.ErrNotFound
	}
	var cover, target string
	var applied, wanted bool
	var released sql.NullTime
	if err = tx.QueryRowContext(ctx, `SELECT cover_id,target,applied,wanted,released_at FROM hhc_web.member_recording_live_cover_reference WHERE id=$1 FOR UPDATE`, ref.ID).Scan(&cover, &target, &applied, &wanted, &released); err != nil {
		return err
	}
	if cover != ref.CoverID || target != ref.Target || released.Valid || !wanted || c.Status.CaptureID != ref.Scope || c.RecordingID != ref.RecordingID {
		return recordings.ErrConflict
	}
	if !applied {
		if !retained {
			return errCoverRetainRequired
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET scope=$2,recording_id=$3,applied=true WHERE id=$1`, ref.ID, ref.Scope, ref.RecordingID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ApplyLiveAutoCover persists the immutable reference before retaining outside SQL.
// The final transaction repeats the same custom-selection and End fences.
func (r *RecordingRepository) ApplyLiveAutoCover(ctx context.Context, snapshot recordings.Capture, upload assetclient.RecordingCoverUpload, retain func(context.Context) error) error {
	err := r.applyLiveAutoCover(ctx, snapshot, upload, false)
	if err != errCoverRetainRequired {
		return err
	}
	if err = retain(ctx); err != nil {
		return err
	}
	return r.applyLiveAutoCover(ctx, snapshot, upload, true)
}
func (r *RecordingRepository) applyLiveAutoCover(ctx context.Context, snapshot recordings.Capture, upload assetclient.RecordingCoverUpload, retained bool) error {
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return err
	}
	defer finishTx()
	c, err := lockCapture(ctx, tx, snapshot.IntentID)
	if err != nil {
		return err
	}
	cover := c.LiveCover
	if c.Terminal() || cover == nil || snapshot.LiveCover == nil {
		return nil
	}
	if cover.PromotionReady || cover.AutoFrozen && !cover.PendingFirstAutoCover() {
		return nil
	}
	if c.Status.CaptureID != snapshot.Status.CaptureID || c.BroadcastEpoch != snapshot.BroadcastEpoch || cover.AutoFrozen && cover.Settings.Revision != snapshot.LiveCover.Settings.Revision || cover.AutoUploadID != upload.UploadID || cover.AutoReferenceID != snapshot.LiveCover.AutoReferenceID {
		return nil
	}
	var b recordings.Broadcast
	if c.BroadcastEpoch > 0 {
		var raw []byte
		if err = tx.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.member_recording_broadcast WHERE recording_id::text=$1`, c.RecordingID).Scan(&raw); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &b); err != nil {
			return err
		}
		if b.Epoch != c.BroadcastEpoch || b.Revoked || b.RangePending || b.View.Binding == nil || b.View.Binding.CaptureID != c.Status.CaptureID || b.View.Binding.Epoch != c.BroadcastEpoch || b.View.Boundaries.StartSequence == nil {
			return nil
		}
		if cover.PendingFirstAutoCover() {
			end := b.View.Boundaries.EndSequenceExclusive
			if end == nil || *end <= *b.View.Boundaries.StartSequence || (b.View.Phase != "end_pending" && b.View.Phase != "processing" && b.View.Phase != "archived") {
				return nil
			}
		} else if b.CoverSnapshot.AutoFrozen || b.View.Phase != "live" || b.View.Boundaries.EndSequenceExclusive != nil || b.Projection == nil || b.Projection.State != "ready" {
			return nil
		}
	}
	if upload.State == "ready" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_live_cover_reference(id,target,cover_id,scope,recording_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT(id) DO NOTHING`, cover.AutoReferenceID, c.IntentID, upload.UploadID, c.Status.CaptureID, c.RecordingID); err != nil {
			return err
		}
		var applied, wanted bool
		var released sql.NullTime
		var id string
		if err = tx.QueryRowContext(ctx, `SELECT cover_id,applied,wanted,released_at FROM hhc_web.member_recording_live_cover_reference WHERE id=$1 FOR UPDATE`, cover.AutoReferenceID).Scan(&id, &applied, &wanted, &released); err != nil {
			return err
		}
		if id != upload.UploadID || released.Valid || !wanted {
			return recordings.ErrConflict
		}
		if !applied {
			if !retained {
				// This is the only reference not prepared by a separate durable-intent method.
				if err = tx.Commit(); err != nil {
					return err
				}
				return errCoverRetainRequired
			}
			if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET applied=true WHERE id=$1`, cover.AutoReferenceID); err != nil {
				return err
			}
		}
	}
	cover.AutoState = upload.State
	if cover.Settings.Selection.Mode == "auto" && cover.Settings.Revision == snapshot.LiveCover.Settings.Revision {
		if upload.State == "ready" {
			if cover.Settings.CoverID != "" && cover.Settings.CoverID != upload.UploadID {
				cover.Settings.Revision++
			}
			cover.Settings.CoverState = "ready"
			cover.Settings.CoverID = upload.UploadID
		} else if cover.Settings.CoverState != "ready" {
			cover.Settings.CoverState = "pending"
			if upload.State == "failed" || upload.State == "expired" {
				cover.Settings.CoverState = "failed"
			}
		}
	}
	if c.BroadcastEpoch > 0 {
		if cover.Settings.Revision >= b.CoverSnapshot.Settings.Revision {
			b.CoverSnapshot = *cover
		}
		if err = saveBroadcast(ctx, tx, b); err != nil {
			return err
		}
	}
	if err = saveCapture(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *RecordingRepository) ReconcileLiveCoverReferences(ctx context.Context, now time.Time, release func(context.Context, recordings.LiveCoverReference) error) error {
	var failures error
	for i := 0; i < 100; i++ {
		tx, finishTx, err := r.beginRecordingTransaction(ctx)
		if err != nil {
			return errors.Join(failures, err)
		}
		var ref recordings.LiveCoverReference
		err = tx.QueryRowContext(ctx, `SELECT f.id::text,f.target,f.cover_id,CASE WHEN f.scope='' THEN COALESCE(c.capture_id,'') ELSE f.scope END,f.recording_id,f.wanted,f.applied FROM hhc_web.member_recording_live_cover_reference f LEFT JOIN hhc_web.member_recording_capture c ON c.id::text=f.target LEFT JOIN hhc_web.member_recording r ON r.id=c.recording_id WHERE f.released_at IS NULL AND (f.checked_at IS NULL OR f.checked_at<$1::timestamptz-interval '1 minute') AND (NOT f.wanted AND f.created_at<$1::timestamptz-interval '10 minutes' OR c.id IS NOT NULL AND (r.deleted_at IS NOT NULL OR r.media_terminal OR c.payload_json->'Status'->>'state' IN ('aborted','expired','failed') OR c.payload_json->'LiveCover'->>'PromotionReady'='true' OR f.id::text NOT IN (COALESCE(c.payload_json->'LiveCover'->>'ReferenceID',''),COALESCE(c.payload_json->'LiveCover'->>'AutoReferenceID','')) AND f.cover_id<>COALESCE(c.payload_json->'LiveCover'->'Settings'->>'coverId','') AND f.created_at<$1::timestamptz-interval '10 minutes')) AND NOT EXISTS(SELECT 1 FROM hhc_web.member_recording_live_cover_reference pin WHERE pin.id<>f.id AND pin.cover_id=f.cover_id AND pin.scope=f.scope AND pin.recording_id=f.recording_id AND pin.wanted AND NOT pin.applied AND pin.released_at IS NULL) AND NOT EXISTS(SELECT 1 FROM hhc_web.member_recording_live_cover_reference bw WHERE f.target='defaults' AND bw.target LIKE 'broadcast:%' AND bw.cover_id=f.cover_id AND bw.wanted AND NOT bw.applied AND bw.released_at IS NULL) AND NOT EXISTS(SELECT 1 FROM hhc_web.member_recording_live_cover_reference waiting JOIN hhc_web.member_recording_capture wc ON wc.id::text=waiting.target JOIN hhc_web.member_recording wr ON wr.id=wc.recording_id WHERE f.target='defaults' AND waiting.cover_id=f.cover_id AND waiting.wanted AND NOT waiting.applied AND waiting.released_at IS NULL AND wr.deleted_at IS NULL AND NOT wr.media_terminal AND wc.payload_json->'Status'->>'state' NOT IN ('aborted','expired','failed')) ORDER BY f.checked_at NULLS FIRST,f.created_at LIMIT 1 FOR UPDATE OF f SKIP LOCKED`, now).Scan(&ref.ID, &ref.Target, &ref.CoverID, &ref.Scope, &ref.RecordingID, &ref.Wanted, &ref.Applied)
		if errors.Is(err, sql.ErrNoRows) {
			finishTx()
			return failures
		}
		if err != nil {
			finishTx()
			return errors.Join(failures, err)
		}
		// wanted=false fences retain/apply; checked_at also fences an unselected custom operation.
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET wanted=false,checked_at=$2 WHERE id=$1`, ref.ID, now); err == nil {
			err = tx.Commit()
		}
		finishTx()
		if err != nil {
			return errors.Join(failures, err)
		}
		if ref.Scope != "" {
			err = release(ctx, ref)
		}
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if _, err = r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET released_at=COALESCE(released_at,$2) WHERE id=$1 AND NOT wanted`, ref.ID, now); err != nil {
			return errors.Join(failures, err)
		}
	}
	return failures
}
func (r *RecordingRepository) ListLiveCoverCaptures(ctx context.Context, now time.Time, limit int) ([]recordings.Capture, error) {
	if limit < 1 || limit > 100 {
		return nil, recordings.ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, `WITH picked AS(SELECT id FROM hhc_web.member_recording_capture WHERE payload_json->'LiveCover' IS NOT NULL AND payload_json->'LiveCover'!='null'::jsonb AND payload_json->'Status'->>'state' NOT IN ('aborted','expired','failed') AND COALESCE(payload_json->'LiveCover'->>'PromotionReady','false')!='true' ORDER BY checked_at NULLS FIRST,id LIMIT $1 FOR UPDATE SKIP LOCKED) UPDATE hhc_web.member_recording_capture SET checked_at=$2 WHERE id IN(SELECT id FROM picked) RETURNING payload_json`, limit, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []recordings.Capture{}
	for rows.Next() {
		var raw []byte
		var c recordings.Capture
		if err = rows.Scan(&raw); err == nil {
			err = json.Unmarshal(raw, &c)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (r *RecordingRepository) InheritLiveCover(ctx context.Context, snapshot recordings.Capture, coverID string, now time.Time, retain func(context.Context) error) error {
	err := r.inheritLiveCover(ctx, snapshot, coverID, now, false)
	if err != errCoverRetainRequired {
		return err
	}
	if err = retain(ctx); err != nil {
		return err
	}
	return r.inheritLiveCover(ctx, snapshot, coverID, now, true)
}
func (r *RecordingRepository) inheritLiveCover(ctx context.Context, snapshot recordings.Capture, coverID string, now time.Time, retained bool) error {
	selection := recordings.CoverSelection{Mode: "custom", UploadID: coverID}
	if snapshot.LiveCover.Settings.Selection.Mode == "auto" {
		selection = recordings.CoverSelection{Mode: "auto", CandidateID: coverID}
	}
	kind, err := publicationActor(ctx, snapshot.ActorID)
	if err != nil {
		return err
	}
	key := "live-inherit:" + snapshot.IntentID + ":" + coverID
	expected := snapshot.Status.RecordingVersion
	// Inheritance retries keep their durable intent; current capture/selection is fenced below.
	err = r.db.QueryRowContext(ctx, `SELECT expected_version FROM hhc_web.member_recording_cover_operation WHERE actor_type=$1 AND actor_id=$2 AND operation_key=$3`, kind, snapshot.ActorID, key).Scan(&expected)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	op, err := r.PrepareCoverOperation(ctx, recordings.CoverOperation{ID: snapshot.LiveCover.ReferenceID, RecordingID: snapshot.RecordingID, PackageID: snapshot.Status.CaptureID, CoverID: coverID, Expected: expected, Selection: selection, Actor: snapshot.ActorID, Key: key}, now)
	if err != nil {
		return err
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return err
	}
	defer finishTx()
	c, err := lockCapture(ctx, tx, snapshot.IntentID)
	if err != nil {
		return err
	}
	if c.Terminal() || !c.NormalEnd || c.Status.State != "ready" || c.LiveCover == nil || c.LiveCover.Settings.Revision != snapshot.LiveCover.Settings.Revision || c.LiveCover.Settings.CoverID != snapshot.LiveCover.Settings.CoverID {
		return recordings.ErrConflict
	}
	if c.LiveCover.PromotionReady {
		return nil
	}
	var selected, pkg string
	var prior sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(selected_cover_id,''),COALESCE(package_id,''),selected_cover_reference_id::text FROM hhc_web.member_recording WHERE id::text=$1 AND deleted_at IS NULL AND NOT media_terminal`, c.RecordingID).Scan(&selected, &pkg, &prior); err != nil {
		return err
	}
	if pkg != c.Status.CaptureID || selected != "" && selected != c.LiveCover.PromotionID {
		return recordings.ErrConflict
	}
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM hhc_web.member_recording_cover_operation WHERE id=$1 FOR UPDATE`, op.ID).Scan(&state); err != nil {
		return err
	}
	if state != "pending" {
		return recordings.ErrConflict
	}
	if !retained {
		return errCoverRetainRequired
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET selected_cover_id=$2,selected_cover_reference_id=$3,version=version+1 WHERE id::text=$1`, c.RecordingID, coverID, op.ID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_cover_operation SET state='selected',selected_version=$2 WHERE id=$1`, op.ID, c.Status.RecordingVersion+1); err != nil {
		return err
	}
	if prior.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_cover_operation SET state='releasing' WHERE id::text=$1 AND id!=$2`, prior.String, op.ID); err != nil {
			return err
		}
	}
	if err = r.enqueueAudit(ctx, tx, "cms.recording.cover.update", c.RecordingID, c.ActorID, now); err != nil {
		return err
	}
	c.LiveCover.PromotionID = coverID
	c.LiveCover.PromotionRevision = c.LiveCover.Settings.Revision
	c.LiveCover.PromotionReady = true
	// A newly ready dependency makes the prior publication retry delay obsolete.
	if c.Status.AutoPublish == "pending" {
		c.AutoNextAttemptAt = nil
	}
	if err = saveCapture(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit()
}
