package postgres

import (
	"context"
	"database/sql"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (r *RecordingRepository) LifecycleBindings(ctx context.Context) ([]assetclient.RecordingLifecycleBinding, error) {
	// ponytail: one bounded snapshot covers 1000 non-terminal packages; page this
	// owner contract if the active catalogue grows beyond that operational limit.
	rows, err := r.db.QueryContext(ctx, `SELECT id::text,package_id FROM hhc_web.member_recording WHERE deleted_at IS NULL AND package_id IS NOT NULL AND NOT media_terminal ORDER BY id LIMIT 1001`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []assetclient.RecordingLifecycleBinding{}
	for rows.Next() {
		var b assetclient.RecordingLifecycleBinding
		if err := rows.Scan(&b.RecordingID, &b.PackageID); err != nil {
			return nil, err
		}
		items = append(items, b)
	}
	if len(items) > 1000 {
		return nil, recordings.ErrConflict
	}
	return items, rows.Err()
}

func (r *RecordingRepository) ApplyLifecycle(ctx context.Context, v assetclient.RecordingLifecycleSnapshot) error {
	// A repeated primary-state projection must not consume scarce write admission.
	changed, err := r.lifecycleNeedsWrite(ctx, v)
	if err != nil || !changed {
		return err
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return err
	}
	defer finishTx()
	if err := applyLifecycleTransaction(ctx, tx, v); err != nil {
		return err
	}

	return tx.Commit()
}

func applyLifecycleTransaction(ctx context.Context, tx *sql.Tx, v assetclient.RecordingLifecycleSnapshot) error {
	for _, p := range v.Items {
		terminal := p.State == "expired" || p.State == "failed"
		if p.ReadyAt == nil {
			if terminal {
				if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET media_terminal=true WHERE `+lifecycleTerminalChange, p.RecordingID, p.PackageID); err != nil {
					return err
				}
			}
			continue
		}
		if p.UploadedAt == nil || p.MediaExpiresAt == nil || p.RetentionRevision < 1 {
			return recordings.ErrConflict
		}
		// Never overwrite a newer revision or a terminal deletion fence. Immutable
		// upload time may replace the old ready-time projection only once.
		_, err := tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET uploaded_at=$3,ready_at=$4,expires_at=$5,retention_revision=$6,media_terminal=$7
 WHERE `+lifecycleReadyChange, p.RecordingID, p.PackageID, p.UploadedAt, p.ReadyAt, p.MediaExpiresAt, p.RetentionRevision, terminal)
		if err != nil {
			return err
		}
	}
	return nil
}

const lifecycleTerminalChange = `id::text=$1 AND package_id=$2 AND deleted_at IS NULL AND NOT media_terminal`
const lifecycleReadyChange = `id::text=$1 AND package_id=$2 AND deleted_at IS NULL AND NOT media_terminal AND retention_revision<=$6 AND (retention_revision=0 OR uploaded_at=$3)
 AND (uploaded_at,ready_at,expires_at,retention_revision,media_terminal) IS DISTINCT FROM ($3::timestamptz,$4::timestamptz,$5::timestamptz,$6::bigint,$7::boolean)`

func (r *RecordingRepository) lifecycleNeedsWrite(ctx context.Context, v assetclient.RecordingLifecycleSnapshot) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	for _, p := range v.Items {
		terminal := p.State == "expired" || p.State == "failed"
		where := lifecycleTerminalChange
		args := []any{p.RecordingID, p.PackageID}
		if p.ReadyAt == nil {
			if !terminal {
				continue
			}
		} else {
			if p.UploadedAt == nil || p.MediaExpiresAt == nil || p.RetentionRevision < 1 {
				return false, recordings.ErrConflict
			}
			where = lifecycleReadyChange
			args = append(args, p.UploadedAt, p.ReadyAt, p.MediaExpiresAt, p.RetentionRevision, terminal)
		}
		var changed bool
		if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording WHERE `+where+`)`, args...).Scan(&changed); err != nil {
			return false, err
		}
		if changed {
			return true, nil
		}
	}
	return false, ctx.Err()
}
