package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (r *RecordingRepository) BroadcastIngestCredentials(ctx context.Context, recordingID string) (string, string, error) {
	var hash, secret string
	e := r.db.QueryRowContext(ctx, `SELECT s.key_hash,s.ingest_signing_secret
 FROM hhc_web.member_recording_broadcast b
 JOIN hhc_web.member_recording_broadcast_source s ON s.id=b.source_id OR (b.source_id IS NULL AND s.singleton)
 WHERE b.recording_id::text=$1 AND b.source_managed`, recordingID).Scan(&hash, &secret)
	if errors.Is(e, sql.ErrNoRows) {
		return "", "", recordings.ErrBroadcastIngestSession
	}
	return hash, secret, e
}
func (r *RecordingRepository) BroadcastIngestOwner(ctx context.Context, hash, id, key string) (string, error) {
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return "", e
	}
	defer finishTx()
	source, e := readBroadcastSource(ctx, tx)
	if e != nil {
		return "", e
	}
	if e = source.checkKey(hash); e != nil {
		return "", e
	}
	var actor string
	var recording sql.NullString
	// Reply-loss retries retain the original Console delegate even after retargeting.
	e = tx.QueryRowContext(ctx, `SELECT actor_id::text,recording_id::text FROM hhc_web.member_recording_broadcast_source_operation WHERE operation_key=$1 AND operation='bind'`, key).Scan(&actor, &recording)
	if e == nil {
		if !recording.Valid || recording.String != id {
			return "", recordings.ErrBroadcastOperation
		}
		return actor, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}
	if source.TargetRecordingID == nil || *source.TargetRecordingID != id {
		return "", recordings.ErrBroadcastSourceNotPrepared
	}
	e = tx.QueryRowContext(ctx, `SELECT actor_id::text FROM hhc_web.member_recording_broadcast_source_operation WHERE operation='target' AND recording_id=$1 AND source_revision<=$2 ORDER BY source_revision DESC LIMIT 1`, id, source.Revision).Scan(&actor)
	if errors.Is(e, sql.ErrNoRows) {
		return "", recordings.ErrBroadcastSourceNotPrepared
	}
	if e != nil {
		return "", e
	}
	return actor, tx.Commit()
}

// Authorization must not read the multi-megabyte object/receipt journal per request.
func (r *RecordingRepository) BroadcastIngestCapture(ctx context.Context, capture, id string) (recordings.Capture, error) {
	return scanCapture(r.db.QueryRowContext(ctx, `SELECT jsonb_build_object(
  'IntentID',id::text,'ActorID',actor_id::text,'ActorType',payload_json->>'ActorType',
  'BroadcastEpoch',broadcast_epoch,
  'Status',jsonb_build_object('expiresAt',payload_json#>'{Status,expiresAt}','state',payload_json#>'{Status,state}'))
  FROM hhc_web.member_recording_capture WHERE capture_id=$1 AND recording_id::text=$2`, capture, id))
}
