package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

// A preflight rolls back before Asset I/O; durable references fence late responses.
var errCoverRetainRequired = errors.New("cover retain required")

func (r *RecordingRepository) GetCoverOperation(ctx context.Context, id string, expected int64, input recordings.CoverSelection, actor, key string, now time.Time) (recordings.CoverResult, error) {
	kind, err := publicationActor(ctx, actor)
	if err != nil {
		return recordings.CoverResult{}, err
	}
	var savedID, cover, state string
	var savedExpected int64
	var selected sql.NullInt64
	var payload []byte
	err = r.db.QueryRowContext(ctx, `SELECT recording_id::text,cover_id,expected_version,selection_json,selected_version,state FROM hhc_web.member_recording_cover_operation WHERE actor_type=$1 AND actor_id=$2 AND operation_key=$3`, kind, actor, key).Scan(&savedID, &cover, &savedExpected, &payload, &selected, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return recordings.CoverResult{}, recordings.ErrNotFound
	}
	if err != nil {
		return recordings.CoverResult{}, err
	}
	var selection recordings.CoverSelection
	if json.Unmarshal(payload, &selection) != nil || selection != input || savedID != id || savedExpected != expected {
		return recordings.CoverResult{}, recordings.ErrConflict
	}
	if !selected.Valid {
		if state != "pending" {
			return recordings.CoverResult{}, recordings.ErrConflict
		}
		return recordings.CoverResult{}, recordings.ErrNotFound
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return recordings.CoverResult{}, err
	}
	return recordings.NewCoverResult(recordings.CoverReceipt{CoverID: cover, RecordingVersion: selected.Int64}, current, now), nil
}

// This intent commits before contacting Asset. A unique reference plus Asset's
// release tombstone makes even delayed, response-lost retains recoverable.
func (r *RecordingRepository) PrepareCoverOperation(ctx context.Context, op recordings.CoverOperation, now time.Time) (recordings.CoverOperation, error) {
	kind, err := publicationActor(ctx, op.Actor)
	if err != nil {
		return op, err
	}
	payload, err := json.Marshal(op.Selection)
	if err != nil {
		return op, err
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return op, err
	}
	defer finishTx()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "recording-cover:"+kind+":"+op.Actor+":"+op.Key); err != nil {
		return op, err
	}
	var existing recordings.CoverOperation
	var data []byte
	var state string
	err = tx.QueryRowContext(ctx, `SELECT id::text,recording_id::text,package_id,cover_id,expected_version,selection_json,state FROM hhc_web.member_recording_cover_operation WHERE actor_type=$1 AND actor_id=$2 AND operation_key=$3`, kind, op.Actor, op.Key).Scan(&existing.ID, &existing.RecordingID, &existing.PackageID, &existing.CoverID, &existing.Expected, &data, &state)
	if err == nil {
		if json.Unmarshal(data, &existing.Selection) != nil || existing.RecordingID != op.RecordingID || existing.Expected != op.Expected || existing.Selection != op.Selection || existing.PackageID != op.PackageID || existing.CoverID != op.CoverID || state == "released" || state == "releasing" {
			return op, recordings.ErrConflict
		}
		op.ID = existing.ID
	} else if errors.Is(err, sql.ErrNoRows) {
		provenance, _ := auditclient.ProvenanceFromContext(ctx)
		_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_cover_operation(id,actor_type,actor_id,operation_key,recording_id,package_id,cover_id,expected_version,selection_json,created_at,credential_id,request_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, op.ID, kind, op.Actor, op.Key, op.RecordingID, op.PackageID, op.CoverID, op.Expected, payload, now, provenance.CredentialID, provenance.RequestID)
		if err != nil {
			return op, recordingConflict(err)
		}
	} else {
		return op, err
	}
	return op, tx.Commit()
}

func (r *RecordingRepository) CommitCoverOperation(ctx context.Context, op recordings.CoverOperation, canPublish bool, now time.Time, retain func(context.Context) error) (recordings.CoverResult, error) {
	value, err := r.commitCoverOperation(ctx, op, canPublish, now, false)
	if err != errCoverRetainRequired {
		return value, err
	}
	if err = retain(ctx); err != nil {
		return recordings.CoverResult{}, err
	}
	return r.commitCoverOperation(ctx, op, canPublish, now, true)
}
func (r *RecordingRepository) commitCoverOperation(ctx context.Context, op recordings.CoverOperation, canPublish bool, now time.Time, retained bool) (recordings.CoverResult, error) {
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return recordings.CoverResult{}, err
	}
	defer finishTx()
	var state string
	var selected sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT state,selected_version FROM hhc_web.member_recording_cover_operation WHERE id=$1 FOR UPDATE`, op.ID).Scan(&state, &selected); err != nil {
		return recordings.CoverResult{}, err
	}
	if selected.Valid {
		current, err := scanRecording(tx.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM hhc_web.member_recording WHERE id=$1 AND deleted_at IS NULL`, op.RecordingID))
		return recordings.NewCoverResult(recordings.CoverReceipt{CoverID: op.CoverID, RecordingVersion: selected.Int64}, current, now), err
	}
	if state != "pending" {
		return recordings.CoverResult{}, recordings.ErrConflict
	}
	var previousReference sql.NullString
	var version int64
	var status, pkg string
	var expires time.Time
	err = tx.QueryRowContext(ctx, `SELECT version,status,COALESCE(package_id,''),COALESCE(expires_at,draft_expires_at),selected_cover_reference_id::text FROM hhc_web.member_recording WHERE id=$1 AND deleted_at IS NULL FOR UPDATE`, op.RecordingID).Scan(&version, &status, &pkg, &expires, &previousReference)
	if errors.Is(err, sql.ErrNoRows) {
		return recordings.CoverResult{}, recordings.ErrNotFound
	}
	if err != nil {
		return recordings.CoverResult{}, err
	}
	if !now.Before(expires) {
		return recordings.CoverResult{}, recordings.ErrNotFound
	}
	if status == "published" && !canPublish {
		return recordings.CoverResult{}, recordings.ErrForbidden
	}
	if version != op.Expected {
		return recordings.CoverResult{}, recordings.ErrPrecondition
	}
	if pkg != op.PackageID {
		return recordings.CoverResult{}, recordings.ErrConflict
	}
	// The prepared intent survives a lost retain response; the final CAS runs fresh.
	if !retained {
		return recordings.CoverResult{}, errCoverRetainRequired
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET selected_cover_id=$2,selected_cover_reference_id=$3,human_edit_generation=human_edit_generation+1,version=version+1,updated_by=$4,updated_at=$5 WHERE id=$1`, op.RecordingID, op.CoverID, op.ID, op.Actor, now); err != nil {
		return recordings.CoverResult{}, err
	}
	if r.auditOutbox == nil {
		return recordings.CoverResult{}, recordings.ErrAuditUnavailable
	}
	if err = r.enqueueAudit(ctx, tx, "cms.recording.cover.update", op.RecordingID, op.Actor, now); err != nil {
		return recordings.CoverResult{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_cover_operation SET state='selected',selected_version=$2 WHERE id=$1`, op.ID, version+1); err != nil {
		return recordings.CoverResult{}, err
	}
	if previousReference.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_cover_operation SET state='releasing' WHERE id::text=$1`, previousReference.String); err != nil {
			return recordings.CoverResult{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return recordings.CoverResult{}, err
	}
	current, err := r.Get(ctx, op.RecordingID)
	if err != nil {
		return recordings.CoverResult{}, err
	}
	return recordings.NewCoverResult(recordings.CoverReceipt{CoverID: op.CoverID, RecordingVersion: version + 1}, current, now), nil
}
func (r *RecordingRepository) FailCoverOperation(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_cover_operation SET state='releasing' WHERE id=$1 AND state='pending' AND selected_version IS NULL`, id)
	return err
}

func (r *RecordingRepository) ReconcileCoverReferences(ctx context.Context, now time.Time, release func(context.Context, recordings.CoverOperation) error) error {
	var failures error
	for i := 0; i < 100; i++ {
		tx, finishTx, err := r.beginRecordingTransaction(ctx)
		if err != nil {
			return errors.Join(failures, err)
		}
		var op recordings.CoverOperation
		err = tx.QueryRowContext(ctx, `SELECT o.id::text,o.recording_id::text,o.package_id,o.cover_id FROM hhc_web.member_recording_cover_operation o JOIN hhc_web.member_recording r ON r.id=o.recording_id WHERE (o.state='releasing' OR o.state='pending' AND o.created_at<=$1 OR o.state='selected' AND (r.deleted_at IS NOT NULL OR r.media_terminal)) AND NOT (o.state='pending' AND r.deleted_at IS NULL AND NOT r.media_terminal AND COALESCE(r.expires_at,r.draft_expires_at)>$2 AND EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c WHERE c.recording_id=o.recording_id AND c.capture_id=o.package_id AND c.payload_json->'Status'->>'state'='ready' AND c.payload_json->>'NormalEnd'='true' AND c.payload_json->'LiveCover'->>'ReferenceID'=o.id::text AND COALESCE(c.payload_json->'LiveCover'->>'PromotionReady','false')='false')) AND (o.checked_at IS NULL OR o.checked_at<$2::timestamptz-interval '1 minute') AND (r.selected_cover_reference_id IS DISTINCT FROM o.id OR r.deleted_at IS NOT NULL OR r.media_terminal) ORDER BY o.checked_at NULLS FIRST,o.created_at,o.id LIMIT 1 FOR UPDATE OF o SKIP LOCKED`, now.Add(-10*time.Minute), now).Scan(&op.ID, &op.RecordingID, &op.PackageID, &op.CoverID)
		if errors.Is(err, sql.ErrNoRows) {
			finishTx()
			return failures
		}
		if err != nil {
			finishTx()
			return errors.Join(failures, err)
		}
		// Fence selection durably before release; failures remain retryable.
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_cover_operation SET state='releasing',checked_at=$2 WHERE id=$1`, op.ID, now); err == nil {
			err = tx.Commit()
		}
		finishTx()
		if err != nil {
			return errors.Join(failures, err)
		}
		state := "released"
		if err = release(ctx, op); err != nil {
			state = "releasing"
			failures = errors.Join(failures, err)
		}
		if _, err = r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_cover_operation SET state=$2 WHERE id=$1 AND state='releasing'`, op.ID, state); err != nil {
			return errors.Join(failures, err)
		}
	}
	return failures
}
