package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (r *RecordingRepository) ReadStreamKey(ctx context.Context, id string) (string, recordings.SealedStreamKey, error) {
	var hash string
	var sealed recordings.SealedStreamKey
	e := r.db.QueryRowContext(ctx, `SELECT key_hash,key_encryption_id,key_ciphertext FROM hhc_web.member_recording_broadcast_source WHERE id=$1 AND deleted_at IS NULL`, id).Scan(&hash, &sealed.KeyID, &sealed.Ciphertext)
	if errors.Is(e, sql.ErrNoRows) {
		e = recordings.ErrNotFound
	}
	return hash, sealed, e
}
func (r *RecordingRepository) RetainStreamKey(ctx context.Context, hash string, sealed recordings.SealedStreamKey) error {
	// An authenticated old key may race a reset; never replace the new key or its envelope.
	_, e := r.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET key_encryption_id=$2,key_ciphertext=$3 WHERE key_hash=$1 AND deleted_at IS NULL AND key_ciphertext IS NULL`, hash, sealed.KeyID, sealed.Ciphertext)
	return e
}
