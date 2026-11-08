package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"time"
)

type directSettings struct {
	Title, Description string
	Cover              recordings.CaptureLiveCover
}

func rememberDirectSettings(ctx context.Context, tx *sql.Tx, b recordings.Broadcast, now time.Time) error {
	var id string
	var raw []byte
	e := tx.QueryRowContext(ctx, `SELECT id::text,direct_settings FROM hhc_web.member_recording_broadcast_source WHERE direct_recording_id=$1 AND deleted_at IS NULL FOR UPDATE`, b.View.RecordingID).Scan(&id, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var old directSettings
	if e = json.Unmarshal(raw, &old); e != nil {
		return e
	}
	settings := directSettings{Title: b.View.Title, Description: b.View.Description, Cover: b.CoverSnapshot}
	unchanged := old.Cover.Settings.Selection == settings.Cover.Settings.Selection && old.Cover.Scope == settings.Cover.Scope
	if unchanged {
		settings.Cover.ReferenceID = old.Cover.ReferenceID
	} else {
		if old.Cover.ReferenceID != "" {
			if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET wanted=false WHERE id=$1 AND target=$2`, old.Cover.ReferenceID, "source:"+id); e != nil {
				return e
			}
		}
		settings.Cover.ReferenceID = ""
		if settings.Cover.Settings.Selection.Mode == "custom" {
			settings.Cover.ReferenceID = platform.NewID()
			if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_live_cover_reference(id,target,cover_id,scope,recording_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, settings.Cover.ReferenceID, "source:"+id, settings.Cover.Settings.Selection.UploadID, settings.Cover.Scope, "", now); e != nil {
				return e
			}
		}
	}
	raw, e = json.Marshal(settings)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET direct_settings=$2 WHERE id=$1`, id, raw)
	return e
}
func (r *RecordingRepository) RetainStudioCovers(ctx context.Context, now time.Time, limit int, retain func(context.Context, recordings.LiveCoverReference) error) error {
	var failures error
	for i := 0; i < limit; i++ {
		tx, finishTx, e := r.beginRecordingTransaction(ctx)
		if e != nil {
			return errors.Join(failures, e)
		}
		var ref recordings.LiveCoverReference
		e = tx.QueryRowContext(ctx, `SELECT id::text,target,cover_id,scope,recording_id FROM hhc_web.member_recording_live_cover_reference WHERE target LIKE 'source:%' AND wanted AND NOT applied AND released_at IS NULL AND (checked_at IS NULL OR checked_at<$1::timestamptz-interval '1 minute') ORDER BY checked_at NULLS FIRST,created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, now).Scan(&ref.ID, &ref.Target, &ref.CoverID, &ref.Scope, &ref.RecordingID)
		if errors.Is(e, sql.ErrNoRows) {
			finishTx()
			return failures
		}
		if e != nil {
			finishTx()
			return errors.Join(failures, e)
		}
		if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET checked_at=$2 WHERE id=$1`, ref.ID, now); e == nil {
			e = tx.Commit()
		}
		finishTx()
		if e != nil {
			return errors.Join(failures, e)
		}
		attempt := retain(ctx, ref)
		if attempt == nil {
			_, e = r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET applied=true WHERE id=$1 AND wanted AND released_at IS NULL`, ref.ID)
			if e != nil {
				return errors.Join(failures, e)
			}
		}
		failures = errors.Join(failures, attempt)
	}
	return failures
}
