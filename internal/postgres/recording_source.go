package postgres

import (
	"context"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (r *RecordingRepository) AddSource(ctx context.Context, id, source string, now time.Time) error {
	if !packageIDPattern.MatchString(source) {
		return recordings.ErrInvalid
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return err
	}
	defer finishTx()
	var locked string
	if err = tx.QueryRowContext(ctx, `SELECT id::text FROM hhc_web.member_recording WHERE id::text=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET source_id=$2 WHERE id::text=$1 AND deleted_at IS NULL AND NOT is_broadcast AND NOT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c WHERE c.recording_id=member_recording.id) AND primary_asset_version_id IS NULL AND package_id IS NULL AND (source_id IS NULL OR source_id=$2) AND COALESCE(expires_at,draft_expires_at)>$3`, id, source, now)
	if err != nil {
		return recordingConflict(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return recordings.ErrConflict
	}
	return tx.Commit()
}

func (r *RecordingRepository) RecordSourceReady(ctx context.Context, id, source, pkg string, at time.Time) (recordings.Recording, error) {
	if !packageIDPattern.MatchString(source) || !packageIDPattern.MatchString(pkg) || at.IsZero() {
		return recordings.Recording{}, recordings.ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording SET package_id=$3,uploaded_at=$4,expires_at=$4::timestamptz+interval '720 hours' WHERE id::text=$1 AND deleted_at IS NULL AND source_id=$2 AND NOT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c WHERE c.recording_id=member_recording.id) AND primary_asset_version_id IS NULL AND (package_id IS NULL OR package_id=$3) AND (uploaded_at IS NULL OR uploaded_at=$4)`, id, source, pkg, at.UTC())
	if err != nil {
		return recordings.Recording{}, recordingConflict(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return recordings.Recording{}, recordings.ErrConflict
	}
	return r.Get(ctx, id)
}
