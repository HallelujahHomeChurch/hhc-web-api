package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (r *RecordingRepository) Delete(ctx context.Context, id string, expected int64, actor string, now time.Time, canPublish bool) error {
	// Serialize target cleanup with Console preparation and OBS binding.
	tx, finishTx, err := r.sourceTransaction(ctx)
	if err != nil {
		return err
	}
	defer finishTx()
	var version int64
	var status string
	var deleted sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT version,status,deleted_at FROM hhc_web.member_recording WHERE id::text=$1 FOR UPDATE`, id).Scan(&version, &status, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return recordings.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status == "published" && !canPublish {
		return recordings.ErrForbidden
	}
	if deleted.Valid {
		if version == expected+1 {
			return nil
		}
		return recordings.ErrPrecondition
	}
	if version != expected {
		return recordings.ErrPrecondition
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET deleted_at=$2,human_edit_generation=human_edit_generation+1,version=version+1,updated_by=$3,updated_at=$2 WHERE id::text=$1`, id, now, actor); err != nil {
		return err
	}
	if err = closeCapturePublicationTx(ctx, tx, id, false, now); err != nil {
		return err
	}
	if err = clearDeletedBroadcastSourceTarget(ctx, tx); err != nil {
		return err
	}
	if err = r.enqueueAudit(ctx, tx, "cms.recording.delete", id, actor, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *RecordingRepository) ListPendingDeletions(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id::text FROM hhc_web.member_recording
 WHERE deleted_at IS NOT NULL AND deletion_delivered_at IS NULL
 ORDER BY deletion_checked_at NULLS FIRST,id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *RecordingRepository) MarkDeletionAttempt(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording SET deletion_checked_at=clock_timestamp() WHERE id::text=$1 AND deleted_at IS NOT NULL AND deletion_delivered_at IS NULL`, id)
	return err
}

func (r *RecordingRepository) MarkDeletionDelivered(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording SET deletion_delivered_at=clock_timestamp() WHERE id::text=$1 AND deleted_at IS NOT NULL`, id)
	return err
}
