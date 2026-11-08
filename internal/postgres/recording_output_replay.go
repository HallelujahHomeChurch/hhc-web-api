package postgres

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (r *RecordingRepository) ReadBroadcastOutputReplay(ctx context.Context, hash string, in recordings.BroadcastOutputReplayInput) (recordings.BroadcastOutputReplaySnapshot, error) {
	var out recordings.BroadcastOutputReplaySnapshot
	// sourceTransaction also cleans stale prepared sources. Replay must not write
	// business state, but still takes the same global/source/record/capture order.
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return out, err
	}
	defer finishTx()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('recording-capture-single-input',0))`); err != nil {
		return out, err
	}
	source, err := readStudioSource(ctx, tx, in.SourceID, "")
	if errors.Is(err, recordings.ErrNotFound) {
		return out, recordings.ErrBroadcastStreamKey
	}
	if err != nil {
		return out, err
	}
	if hash == "" || source.hash == "" || subtle.ConstantTimeCompare([]byte(source.hash), []byte(hash)) != 1 {
		return out, recordings.ErrBroadcastStreamKey
	}
	var recording, owner, intent string
	var generation, epoch int64
	err = tx.QueryRowContext(ctx, `SELECT recording_id::text,actor_id::text,key_generation,COALESCE(capture_intent_id::text,''),COALESCE(epoch,0) FROM hhc_web.member_recording_broadcast_output WHERE source_id=$1 AND output_id=$2`, source.id, in.OutputSessionID).Scan(&recording, &owner, &generation, &intent, &epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return out, recordings.ErrBroadcastOutputReplayUnknown
	}
	if err != nil {
		return out, err
	}
	if recording != in.RecordingID || epoch != in.Epoch || intent == "" {
		return out, recordings.ErrBroadcastOutputReplayUnknown
	}
	b, value, capture, err := lockBroadcast(ctx, tx, recording)
	if errors.Is(err, recordings.ErrNotFound) {
		return out, recordings.ErrBroadcastOutputReplayUnknown
	}
	if err != nil {
		return out, err
	}
	if !b.SourceManaged || b.CaptureIntentID != intent || b.Epoch != epoch || capture == nil || capture.IntentID != intent || capture.ActorID != owner || capture.BroadcastEpoch != epoch || capture.Status.CaptureID != in.CaptureID || b.View.Binding == nil || b.View.Binding.CaptureID != in.CaptureID || b.View.Binding.Epoch != epoch || b.View.Binding.ActorID != owner {
		return out, recordings.ErrBroadcastOutputReplayUnknown
	}
	frames := 0
	if in.MediaProfile != nil {
		frames = 300
	}
	operation, exists := b.Operations[in.OutputSessionID+".bind"]
	if capture.SegmentFrames != frames || !exists || operation.ActorID != owner || operation.Receipt.OperationKey != in.OutputSessionID+".bind" || operation.Receipt.Operation != "bind" || operation.Receipt.State != "accepted" && operation.Receipt.State != "completed" {
		return out, recordings.ErrBroadcastOutputReplayUnknown
	}
	// K4 renders completed only after binding; it does not persist this receipt
	// state. The fully bound identity above is mandatory before deriving it.
	receipt := operation.Receipt
	receipt.State = "completed"
	if in.Receipt == nil || receipt != *in.Receipt {
		return out, recordings.ErrBroadcastOutputReplayUnknown
	}
	var deleted, inputActive bool
	var linkedSource, captureID sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT r.deleted_at IS NOT NULL,b.source_id::text,c.capture_id,c.input_active FROM hhc_web.member_recording r JOIN hhc_web.member_recording_broadcast b ON b.recording_id=r.id JOIN hhc_web.member_recording_capture c ON c.recording_id=r.id AND c.id=$2 WHERE r.id=$1`, recording, intent).Scan(&deleted, &linkedSource, &captureID, &inputActive); errors.Is(err, sql.ErrNoRows) {
		return out, recordings.ErrBroadcastOutputReplayUnknown
	} else if err != nil {
		return out, err
	}
	if !linkedSource.Valid || linkedSource.String != source.id || !captureID.Valid || captureID.String != in.CaptureID {
		return out, recordings.ErrBroadcastOutputReplayUnknown
	}
	out = recordings.BroadcastOutputReplaySnapshot{Owner: owner, Capture: *capture, Receipt: receipt, State: "terminal"}
	if inputActive {
		if deleted || value.MediaTerminal || b.Revoked || b.View.Phase == "cancelled" || b.View.Phase == "failed" || capture.Terminal() || generation != source.generation {
			return recordings.BroadcastOutputReplaySnapshot{}, recordings.ErrBroadcastOutputReplayUnknown
		}
		out.State = "active"
	}
	return out, tx.Commit()
}
