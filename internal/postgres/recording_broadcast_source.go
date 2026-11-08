package postgres

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"time"
)

type broadcastSourceState struct {
	recordings.BroadcastSource
	hash string
	id   string
}

func readBroadcastSource(ctx context.Context, tx *sql.Tx) (broadcastSourceState, error) {
	return readBroadcastSourceID(ctx, tx, "")
}
func readBroadcastSourceID(ctx context.Context, tx *sql.Tx, id string) (broadcastSourceState, error) {
	var s broadcastSourceState
	e := tx.QueryRowContext(ctx, `SELECT id::text,revision,key_hash,suffix,target_recording_id::text FROM hhc_web.member_recording_broadcast_source WHERE deleted_at IS NULL AND (($1='' AND singleton) OR id::text=$1) FOR UPDATE`, id).Scan(&s.id, &s.Revision, &s.hash, &s.Suffix, &s.TargetRecordingID)
	if e != nil {
		return s, e
	}
	e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture WHERE input_active)`).Scan(&s.InputBusy)
	s.Configured = s.hash != ""
	s.CanRotate = !s.InputBusy
	return s, e
}
func (r *RecordingRepository) sourceTransaction(ctx context.Context) (*sql.Tx, func(), error) {
	tx, finishTx, e := r.beginRecordingTransaction(ctx)
	if e != nil {
		return nil, nil, e
	}
	transferred := false
	defer func() {
		if !transferred {
			finishTx()
		}
	}()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('recording-capture-single-input',0))`); e != nil {
		finishTx()
		return nil, nil, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source s SET prepared=false,revision=revision+1 WHERE prepared AND (
 EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast b WHERE b.recording_id=s.target_recording_id AND (b.payload_json->>'Revoked'='true' OR b.payload_json->'View'->>'phase' IN ('cancelled','failed')) AND NOT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c WHERE c.recording_id=b.recording_id AND c.input_active))
 OR target_recording_id IS NULL OR EXISTS(SELECT 1 FROM hhc_web.member_recording r WHERE r.id=s.target_recording_id AND r.deleted_at IS NOT NULL)
 OR EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c JOIN hhc_web.member_recording_broadcast b ON b.recording_id=c.recording_id AND b.payload_json->>'CaptureIntentID'=c.id::text WHERE c.recording_id=s.target_recording_id AND NOT c.input_active))`); e != nil {
		finishTx()
		return nil, nil, e
	}
	transferred = true
	return tx, finishTx, nil
}

// Call under the shared input lock; removing a stale target never releases a capture.
func clearDeletedBroadcastSourceTarget(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source s SET target_recording_id=NULL,prepared=false,revision=revision+1 WHERE EXISTS(SELECT 1 FROM hhc_web.member_recording r WHERE r.id=s.target_recording_id AND r.deleted_at IS NOT NULL)`)
	return err
}
func (s broadcastSourceState) checkKey(hash string) error {
	if s.hash == "" || hash == "" || subtle.ConstantTimeCompare([]byte(s.hash), []byte(hash)) != 1 {
		return recordings.ErrBroadcastStreamKey
	}
	return nil
}
func (r *RecordingRepository) GetBroadcastSource(ctx context.Context) (recordings.BroadcastSource, error) {
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return recordings.BroadcastSource{}, e
	}
	defer finishTx()
	s, e := readBroadcastSource(ctx, tx)
	if e != nil {
		return s.BroadcastSource, e
	}
	return s.BroadcastSource, tx.Commit()
}

type sourceOperation struct {
	actor, kind, digest string
	revision            int64
	recording, intent   sql.NullString
	epoch               sql.NullInt64
}

func sourceReplay(ctx context.Context, tx *sql.Tx, key, actor, kind, digest string) (sourceOperation, bool, error) {
	var op sourceOperation
	e := tx.QueryRowContext(ctx, `SELECT actor_id::text,operation,digest,source_revision,recording_id::text,capture_intent_id::text,epoch FROM hhc_web.member_recording_broadcast_source_operation WHERE operation_key=$1`, key).Scan(&op.actor, &op.kind, &op.digest, &op.revision, &op.recording, &op.intent, &op.epoch)
	if errors.Is(e, sql.ErrNoRows) {
		return op, false, nil
	}
	if e != nil {
		return op, false, e
	}
	if op.actor != actor || op.kind != kind || op.digest != digest {
		return op, true, recordings.ErrBroadcastOperation
	}
	return op, true, nil
}
func writeSourceOperation(ctx context.Context, tx *sql.Tx, key, actor, kind, digest string, revision int64, recording, intent string, epoch int64, now time.Time) error {
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor {
		return recordings.ErrForbidden
	}
	_, e := tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_broadcast_source_operation(operation_key,actor_id,request_id,operation,digest,source_revision,recording_id,capture_intent_id,epoch,occurred_at) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,NULLIF($8,'')::uuid,NULLIF($9,0),$10)`, key, actor, p.RequestID, kind, digest, revision, recording, intent, epoch, now)
	return e
}
func (r *RecordingRepository) SetBroadcastSourceKey(ctx context.Context, actor string, in recordings.BroadcastSourceStreamKeyInput, hash, suffix string, now time.Time) (recordings.BroadcastSource, error) {
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return recordings.BroadcastSource{}, e
	}
	defer finishTx()
	s, e := readBroadcastSource(ctx, tx)
	if e != nil {
		return s.BroadcastSource, e
	}
	// The raw key is cleared by the service and never enters a durable digest input.
	in.StreamKey = ""
	digest := broadcastDigest(struct {
		Input recordings.BroadcastSourceStreamKeyInput
		Hash  string
	}{in, hash})
	op, replay, e := sourceReplay(ctx, tx, in.OperationKey, actor, "key", digest)
	if e != nil {
		return s.BroadcastSource, e
	}
	if replay {
		if op.revision != s.Revision || s.checkKey(hash) != nil {
			return s.BroadcastSource, recordings.ErrBroadcastOperation
		}
		return s.BroadcastSource, tx.Commit()
	}
	if in.ExpectedSourceRevision != s.Revision {
		return s.BroadcastSource, recordings.ErrPrecondition
	}
	if s.InputBusy {
		return s.BroadcastSource, recordings.ErrBroadcastSourceBusy
	}
	if s.Revision >= 9007199254740991 {
		return s.BroadcastSource, recordings.ErrConflict
	}
	s.Revision++
	s.Configured = true
	s.Suffix = &suffix
	if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET revision=$1,key_hash=$2,suffix=$3,key_ciphertext=NULL,key_encryption_id='',key_generation=key_generation+1 WHERE singleton`, s.Revision, hash, suffix); e != nil {
		return s.BroadcastSource, e
	}
	if e = writeSourceOperation(ctx, tx, in.OperationKey, actor, "key", digest, s.Revision, "", "", 0, now); e != nil {
		return s.BroadcastSource, e
	}
	return s.BroadcastSource, tx.Commit()
}
func sourceTargetEligible(b recordings.Broadcast, v recordings.Recording) bool {
	return b.CanChangePolicy() && b.StreamKeyHash == "" && !b.Revoked && !v.MediaTerminal && b.View.Boundaries.StartSequence == nil && (b.View.Phase == "draft" || b.View.Phase == "scheduled")
}
func sourceRecordingAvailable(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var available bool
	err := tx.QueryRowContext(ctx, `SELECT r.deleted_at IS NULL AND (b.source_id IS NULL OR EXISTS(
 SELECT 1 FROM hhc_web.member_recording_broadcast_source s WHERE s.id=b.source_id AND s.singleton))
 FROM hhc_web.member_recording r JOIN hhc_web.member_recording_broadcast b ON b.recording_id=r.id WHERE r.id=$1`, id).Scan(&available)
	return available, err
}

func (r *RecordingRepository) SetBroadcastSourceTarget(ctx context.Context, actor string, in recordings.BroadcastSourceTargetInput, now time.Time) (recordings.BroadcastSource, error) {
	return r.setBroadcastSourceTarget(ctx, "", actor, in, now)
}
func (r *RecordingRepository) setBroadcastSourceTarget(ctx context.Context, source, actor string, in recordings.BroadcastSourceTargetInput, now time.Time) (recordings.BroadcastSource, error) {
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return recordings.BroadcastSource{}, e
	}
	defer finishTx()
	s, e := readBroadcastSourceID(ctx, tx, source)
	if e != nil {
		return s.BroadcastSource, e
	}
	digest := broadcastDigest(in)
	if source != "" {
		digest = broadcastDigest(struct {
			Source string
			Input  recordings.BroadcastSourceTargetInput
		}{source, in})
	}
	op, replay, e := sourceReplay(ctx, tx, in.OperationKey, actor, "target", digest)
	if e != nil {
		return s.BroadcastSource, e
	}
	if replay {
		if op.revision != s.Revision || s.TargetRecordingID == nil || *s.TargetRecordingID != in.RecordingID {
			return s.BroadcastSource, recordings.ErrBroadcastOperation
		}
		return s.BroadcastSource, tx.Commit()
	}
	if in.ExpectedSourceRevision != s.Revision {
		return s.BroadcastSource, recordings.ErrPrecondition
	}
	var reserved bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast_source WHERE prepared AND id<>$1)`, s.id).Scan(&reserved); e != nil {
		return s.BroadcastSource, e
	}
	if s.InputBusy || reserved {
		return s.BroadcastSource, recordings.ErrBroadcastSourceBusy
	}
	b, v, _, e := lockBroadcast(ctx, tx, in.RecordingID)
	if e != nil {
		return s.BroadcastSource, e
	}
	if in.ExpectedRevision != b.View.Revision {
		return s.BroadcastSource, recordings.ErrPrecondition
	}
	var available bool
	err := tx.QueryRowContext(ctx, `SELECT r.deleted_at IS NULL AND (b.source_id IS NULL OR b.source_id=$2) FROM hhc_web.member_recording r JOIN hhc_web.member_recording_broadcast b ON b.recording_id=r.id WHERE r.id=$1`, in.RecordingID, s.id).Scan(&available)
	if err != nil {
		return s.BroadcastSource, err
	}
	if !available || !sourceTargetEligible(b, v) {
		return s.BroadcastSource, recordings.ErrBroadcastSourceNotPrepared
	}
	if s.Revision >= 9007199254740991 {
		return s.BroadcastSource, recordings.ErrConflict
	}
	if !b.SourceManaged {
		if b.View.Revision >= 9007199254740991 {
			return s.BroadcastSource, recordings.ErrConflict
		}
		b.SourceManaged = true
		b.View.Revision++
		if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast SET source_managed=true,source_id=$2 WHERE recording_id=$1`, in.RecordingID, s.id); e != nil {
			return s.BroadcastSource, e
		}
		if e = saveBroadcast(ctx, tx, b); e != nil {
			return s.BroadcastSource, e
		}
	}
	s.Revision++
	s.TargetRecordingID = &in.RecordingID
	if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET revision=$1,target_recording_id=$2,prepared=$4 WHERE id=$3`, s.Revision, in.RecordingID, s.id, source != ""); e != nil {
		return s.BroadcastSource, e
	}
	if e = writeSourceOperation(ctx, tx, in.OperationKey, actor, "target", digest, s.Revision, in.RecordingID, "", 0, now); e != nil {
		return s.BroadcastSource, e
	}
	return s.BroadcastSource, tx.Commit()
}
func (r *RecordingRepository) ResolveBroadcastSource(ctx context.Context, hash string) (recordings.BroadcastSourceResolved, error) {
	var out recordings.BroadcastSourceResolved
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return out, e
	}
	defer finishTx()
	s, e := readBroadcastSource(ctx, tx)
	if e != nil {
		return out, e
	}
	if e = s.checkKey(hash); e != nil {
		return out, e
	}
	if s.TargetRecordingID == nil {
		return out, recordings.ErrBroadcastSourceNotPrepared
	}
	b, v, _, e := lockBroadcast(ctx, tx, *s.TargetRecordingID)
	if errors.Is(e, recordings.ErrNotFound) {
		return out, recordings.ErrBroadcastSourceNotPrepared
	}
	if e != nil {
		return out, e
	}
	available, err := sourceRecordingAvailable(ctx, tx, b.View.RecordingID)
	if err != nil {
		return out, err
	}
	if !available || !b.SourceManaged || !sourceTargetEligible(b, v) || b.View.Policy.AutoStart && !b.View.Announced {
		return out, recordings.ErrBroadcastSourceNotPrepared
	}
	out = recordings.BroadcastSourceResolved{SourceRevision: s.Revision, RecordingID: b.View.RecordingID, BroadcastRevision: b.View.Revision}
	return out, tx.Commit()
}
func (r *RecordingRepository) PrepareBroadcastSourceBinding(ctx context.Context, actor string, in recordings.BroadcastSourceBindInput, hash string, canPublish bool, now time.Time) (recordings.Broadcast, recordings.Capture, error) {
	var b recordings.Broadcast
	var empty recordings.Capture
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return b, empty, e
	}
	defer finishTx()
	s, e := readBroadcastSource(ctx, tx)
	if e != nil {
		return b, empty, e
	}
	if e = s.checkKey(hash); e != nil {
		return b, empty, e
	}
	in.StreamKey = ""
	digest := broadcastDigest(struct {
		Input recordings.BroadcastSourceBindInput
		Hash  string
	}{in, hash})
	op, replay, e := sourceReplay(ctx, tx, in.OperationKey, actor, "bind", digest)
	if e != nil {
		return b, empty, e
	}
	b, v, prior, e := lockBroadcast(ctx, tx, in.RecordingID)
	if e != nil {
		return b, empty, e
	}
	available, err := sourceRecordingAvailable(ctx, tx, in.RecordingID)
	if err != nil {
		return b, empty, err
	}
	if !available || b.Revoked {
		return b, empty, recordings.ErrBroadcastOperation
	}
	if (b.View.Policy.AutoStart || b.View.Policy.AutoPublish) && !canPublish {
		return b, empty, recordings.ErrForbidden
	}
	if replay {
		if !op.recording.Valid || op.recording.String != in.RecordingID || prior == nil || !op.intent.Valid || prior.IntentID != op.intent.String || !op.epoch.Valid || b.Epoch != op.epoch.Int64 || prior.Terminal() || !prior.Active() {
			return b, empty, recordings.ErrBroadcastOperation
		}
		return b, *prior, tx.Commit()
	}
	if s.Revision != in.ExpectedSourceRevision {
		return b, empty, recordings.ErrPrecondition
	}
	if s.TargetRecordingID == nil || *s.TargetRecordingID != in.RecordingID || !b.SourceManaged {
		return b, empty, recordings.ErrBroadcastSourceNotPrepared
	}
	if b.View.Revision != in.ExpectedRevision {
		return b, empty, recordings.ErrPrecondition
	}
	if !sourceTargetEligible(b, v) {
		return b, empty, recordings.ErrBroadcastSourceNotPrepared
	}
	if s.InputBusy {
		return b, empty, recordings.ErrBroadcastSourceBusy
	}
	// A B1 operation namespace collision must never become a K2 ledger adoption.
	if _, exists := b.Operations[in.OperationKey]; exists {
		return b, empty, recordings.ErrBroadcastOperation
	}
	b, c, e := r.reserveBroadcastBinding(ctx, tx, in.RecordingID, actor, in.BroadcastBindInput, now, b, v, prior)
	if e != nil {
		return b, empty, e
	}
	if e = writeSourceOperation(ctx, tx, in.OperationKey, actor, "bind", digest, s.Revision, in.RecordingID, c.IntentID, b.Epoch, now); e != nil {
		return b, empty, e
	}
	return b, c, tx.Commit()
}
