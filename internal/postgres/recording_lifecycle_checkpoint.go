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
	"github.com/jackc/pgx/v5/pgconn"
)

func (r *RecordingRepository) ClaimLifecycleCycle(ctx context.Context, _ time.Time) (recordings.LifecycleClaim, error) {
	var claim recordings.LifecycleClaim
	tx, finish, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return claim, err
	}
	defer finish()
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_lifecycle_checkpoint(id) VALUES(true) ON CONFLICT DO NOTHING`); err != nil {
		return claim, err
	}
	var raw []byte
	token := platform.NewID()
	err = tx.QueryRowContext(ctx, `UPDATE hhc_web.member_recording_lifecycle_checkpoint SET lease_token=$1,lease_until=clock_timestamp()+interval '15 seconds' WHERE id AND (lease_until IS NULL OR lease_until<=clock_timestamp()) RETURNING checkpoint`, token).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return claim, nil
	}
	if err != nil {
		return claim, err
	}
	if err = json.Unmarshal(raw, &claim.State); err != nil {
		return claim, err
	}
	if len(claim.State.Repairs) > recordings.LifecycleRepairLimit || len(raw) > 524288 {
		return claim, recordings.ErrLifecycleRepairCapacity
	}
	if err = tx.Commit(); err != nil {
		return recordings.LifecycleClaim{}, err
	}
	claim.Token = token
	return claim, nil
}
func (r *RecordingRepository) ReleaseLifecycleCycle(ctx context.Context, token string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_lifecycle_checkpoint SET lease_token=NULL,lease_until=NULL WHERE id AND lease_token=$1`, token)
	return err
}
func (r *RecordingRepository) LifecycleBindingPage(ctx context.Context, cursor string, limit int) ([]assetclient.RecordingLifecycleBinding, error) {
	if limit < 1 || limit > 100 {
		return nil, recordings.ErrInvalid
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id::text,package_id FROM hhc_web.member_recording WHERE deleted_at IS NULL AND package_id IS NOT NULL AND NOT media_terminal AND (NULLIF($1,'')::uuid IS NULL OR id>NULLIF($1,'')::uuid) ORDER BY id LIMIT $2`, cursor, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []assetclient.RecordingLifecycleBinding{}
	for rows.Next() {
		var b assetclient.RecordingLifecycleBinding
		if err = rows.Scan(&b.RecordingID, &b.PackageID); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
func (r *RecordingRepository) CommitLifecycleCycle(ctx context.Context, token string, state recordings.LifecycleCheckpoint, v assetclient.RecordingLifecycleSnapshot) error {
	if state.Repairs == nil {
		state.Repairs = []recordings.LifecycleRepair{}
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(raw) > 524288 || len(state.Repairs) > recordings.LifecycleRepairLimit {
		return recordings.ErrLifecycleRepairCapacity
	}
	tx, finish, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return err
	}
	defer finish()
	var locked bool
	if err = tx.QueryRowContext(ctx, `SELECT true FROM hhc_web.member_recording_lifecycle_checkpoint WHERE id AND lease_token=$1 AND lease_until>clock_timestamp() FOR UPDATE`, token).Scan(&locked); errors.Is(err, sql.ErrNoRows) {
		return recordings.ErrConflict
	} else if err != nil {
		return err
	}
	if err = applyLifecycleTransaction(ctx, tx, v); err != nil {
		var sqlError *pgconn.PgError
		if errors.Is(err, recordings.ErrConflict) || (errors.As(err, &sqlError) && (strings.HasPrefix(sqlError.Code, "22") || strings.HasPrefix(sqlError.Code, "23"))) {
			return &recordings.LifecycleApplyFailure{Err: err}
		}
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_lifecycle_checkpoint SET checkpoint=$2,updated_at=clock_timestamp() WHERE id AND lease_token=$1 AND lease_until>clock_timestamp()`, token, raw)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return recordings.ErrConflict
	}
	return tx.Commit()
}

func (r *RecordingRepository) LifecycleBindingCurrent(ctx context.Context, binding assetclient.RecordingLifecycleBinding) (bool, error) {
	var current bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording WHERE id=$1 AND package_id=$2 AND deleted_at IS NULL AND NOT media_terminal)`, binding.RecordingID, binding.PackageID).Scan(&current)
	return current, err
}
