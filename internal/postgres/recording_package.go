package postgres

import (
	"context"
	"regexp"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

var packageIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Attachment is immutable. A replay may recover a response but cannot replace
// the original upload or attach HLS to a legacy recording during cutover.
func (r *RecordingRepository) AddPackage(ctx context.Context, id, packageID string, now time.Time) error {
	if !packageIDPattern.MatchString(packageID) {
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
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET package_id=$2
 WHERE id::text=$1 AND deleted_at IS NULL AND NOT is_broadcast AND NOT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c WHERE c.recording_id=member_recording.id) AND primary_asset_version_id IS NULL AND source_id IS NULL AND (package_id IS NULL OR package_id=$2)
 AND COALESCE(expires_at,draft_expires_at)>$3`, id, packageID, now)
	if err != nil {
		return recordingConflict(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return recordings.ErrConflict
	}
	return tx.Commit()
}

func (r *RecordingRepository) RecordPackageReady(ctx context.Context, id, packageID string, at time.Time) (recordings.Recording, error) {
	if !packageIDPattern.MatchString(packageID) || at.IsZero() {
		return recordings.Recording{}, recordings.ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording SET uploaded_at=$3,expires_at=$3::timestamptz+interval '720 hours'
 WHERE id::text=$1 AND deleted_at IS NULL AND package_id=$2 AND (uploaded_at IS NULL OR uploaded_at=$3)`, id, packageID, at.UTC())
	if err != nil {
		return recordings.Recording{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return recordings.Recording{}, recordings.ErrConflict
	}
	return r.Get(ctx, id)
}
