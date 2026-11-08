package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"time"
)

type studioSource struct {
	id, hash, actor      string
	generation, revision int64
	target, direct       sql.NullString
	prepared             bool
}

func readStudioSource(ctx context.Context, tx *sql.Tx, id, hash string) (studioSource, error) {
	var s studioSource
	err := tx.QueryRowContext(ctx, `SELECT id::text,key_hash,COALESCE(direct_actor_id::text,''),key_generation,revision,target_recording_id::text,direct_recording_id::text,prepared FROM hhc_web.member_recording_broadcast_source WHERE deleted_at IS NULL AND (($1<>'' AND id::text=$1) OR ($1='' AND key_hash=$2 AND key_hash<>'')) FOR UPDATE`, id, hash).Scan(&s.id, &s.hash, &s.actor, &s.generation, &s.revision, &s.target, &s.direct, &s.prepared)
	if errors.Is(err, sql.ErrNoRows) {
		if id == "" {
			err = recordings.ErrBroadcastStreamKey
		} else {
			err = recordings.ErrNotFound
		}
	}
	return s, err
}
func (r *RecordingRepository) EnsureDirectBroadcast(ctx context.Context, source, actor string, in recordings.BroadcastMutationInput, now time.Time) (recordings.Broadcast, error) {
	tx, finishTx, err := r.sourceTransaction(ctx)
	if err != nil {
		return recordings.Broadcast{}, err
	}
	defer finishTx()
	s, err := readStudioSource(ctx, tx, source, "")
	if err != nil {
		return recordings.Broadcast{}, err
	}
	digest := broadcastDigest(in)
	var priorActor, priorSource, priorDigest, id string
	err = tx.QueryRowContext(ctx, `SELECT actor_id::text,source_id::text,digest,recording_id::text FROM hhc_web.member_recording_studio_operation WHERE operation_key=$1`, in.OperationKey).Scan(&priorActor, &priorSource, &priorDigest, &id)
	if err == nil {
		if priorActor != actor || priorSource != source || priorDigest != digest {
			return recordings.Broadcast{}, recordings.ErrBroadcastOperation
		}
		b, _, _, e := lockBroadcast(ctx, tx, id)
		if e != nil {
			return b, e
		}
		return b, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return recordings.Broadcast{}, err
	}
	if s.revision != in.ExpectedRevision {
		return recordings.Broadcast{}, recordings.ErrPrecondition
	}
	if s.hash == "" {
		return recordings.Broadcast{}, recordings.ErrBroadcastSourceNotPrepared
	}
	b, err := r.ensureDirectTx(ctx, tx, &s, actor, now)
	if err != nil {
		return b, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET direct_actor_id=$2,revision=revision+1 WHERE id=$1`, source, actor)
	if err != nil {
		return b, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_studio_operation(operation_key,source_id,actor_id,digest,recording_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, in.OperationKey, source, actor, digest, b.View.RecordingID, now)
	if err != nil {
		return b, err
	}
	return b, tx.Commit()
}
func (r *RecordingRepository) ensureDirectTx(ctx context.Context, tx *sql.Tx, s *studioSource, actor string, now time.Time) (recordings.Broadcast, error) {
	var previous *recordings.Broadcast
	var settings directSettings
	var snapshot []byte
	if e := tx.QueryRowContext(ctx, `SELECT direct_settings FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, s.id).Scan(&snapshot); e != nil {
		return recordings.Broadcast{}, e
	}
	if e := json.Unmarshal(snapshot, &settings); e != nil {
		return recordings.Broadcast{}, e
	}
	if s.direct.Valid {
		b, v, c, e := lockBroadcast(ctx, tx, s.direct.String)
		if e != nil && !errors.Is(e, recordings.ErrNotFound) {
			return b, e
		}
		if e == nil {
			var deleted bool
			if e = tx.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM hhc_web.member_recording WHERE id=$1`, s.direct.String).Scan(&deleted); e != nil {
				return b, e
			}
			if !deleted {
				previous = &b
			}
			if !deleted && !b.Revoked && b.View.Phase != "cancelled" && b.View.Phase != "failed" && b.View.Phase != "archived" && (b.CanChangePolicy() || c != nil && b.View.Boundaries.EndSequenceExclusive == nil && !c.Terminal()) && !v.MediaTerminal {
				return b, nil
			}
		}
	}
	in := recordings.BroadcastCreateInput{OperationKey: platform.NewID(), Title: "直播", Policy: recordings.BroadcastPolicy{AutoStart: true, AutoEnd: true, AutoPublish: true}, Cover: &recordings.LiveCoverSelection{Mode: "auto"}}
	if settings.Title != "" {
		in.Title = settings.Title
		in.Description = settings.Description
		sel := settings.Cover.Settings.Selection
		in.Cover = &sel
	} else if previous != nil {
		in.Title = previous.View.Title
		in.Description = previous.View.Description
		sel := previous.CoverSnapshot.Settings.Selection
		in.Cover = &sel
	}
	b, e := r.createBroadcastTx(ctx, tx, in, actor, now)
	if e != nil {
		return b, e
	}
	b.SourceManaged = true
	b.View.Announced = true
	if in.Cover.Mode == "custom" {
		// Preserve an independent reference to the original asset scope.
		b.CoverSnapshot.Scope = settings.Cover.Scope
		if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET scope=$2,recording_id=$3 WHERE id=$1`, b.CoverSnapshot.ReferenceID, b.CoverSnapshot.Scope, ""); e != nil {
			return b, e
		}
	}
	if e = saveBroadcast(ctx, tx, b); e != nil {
		return b, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast SET source_id=$2,source_managed=true,studio_mode='direct' WHERE recording_id=$1`, b.View.RecordingID, s.id); e != nil {
		return b, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET direct_recording_id=$2 WHERE id=$1`, s.id, b.View.RecordingID); e != nil {
		return b, e
	}
	s.direct = sql.NullString{String: b.View.RecordingID, Valid: true}
	if e = rememberDirectSettings(ctx, tx, b, now); e != nil {
		return b, e
	}
	return b, nil
}

func (r *RecordingRepository) BroadcastOutputOwner(ctx context.Context, hash, output string) (string, error) {
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return "", e
	}
	defer finishTx()
	s, e := readStudioSource(ctx, tx, "", hash)
	if e != nil {
		return "", e
	}
	var owner string
	e = tx.QueryRowContext(ctx, `SELECT actor_id::text FROM hhc_web.member_recording_broadcast_output WHERE source_id=$1 AND output_id=$2`, s.id, output).Scan(&owner)
	if e == nil {
		return owner, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return "", e
	}
	if s.target.Valid {
		b, v, c, err := lockBroadcast(ctx, tx, s.target.String)
		if err != nil && !errors.Is(err, recordings.ErrNotFound) {
			return "", err
		}
		if err == nil && c == nil && sourceTargetEligible(b, v) {
			var deleted bool
			if err = tx.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM hhc_web.member_recording WHERE id=$1`, s.target.String).Scan(&deleted); err != nil {
				return "", err
			}
			if !deleted {
				err = tx.QueryRowContext(ctx, `SELECT actor_id::text FROM hhc_web.member_recording_broadcast_source_operation WHERE operation='target' AND recording_id=$1 ORDER BY occurred_at DESC LIMIT 1`, s.target.String).Scan(&owner)
				if err == nil {
					return owner, tx.Commit()
				}
				if !errors.Is(err, sql.ErrNoRows) {
					return "", err
				}
			}
		}
	}
	if s.actor == "" {
		return "", recordings.ErrBroadcastSourceNotPrepared
	}
	return s.actor, tx.Commit()
}
func (r *RecordingRepository) PrepareBroadcastOutput(ctx context.Context, hash, output, actor string, allowNew bool, now time.Time, options ...recordings.BroadcastOutputOptions) (recordings.BroadcastOutputReservation, error) {
	var option recordings.BroadcastOutputOptions
	if len(options) > 0 {
		option = options[0]
	}
	if option.RecoveryVersion != "" && option.RecoveryVersion != recordings.BroadcastRecoveryVersion {
		return recordings.BroadcastOutputReservation{}, recordings.ErrInvalid
	}
	frames := 0
	if option.MediaProfile != "" {
		if option.MediaProfile != "obs-hls-10s-v1" {
			return recordings.BroadcastOutputReservation{}, recordings.ErrBroadcastProtocol
		}
		frames = 300
	}
	var out recordings.BroadcastOutputReservation
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return out, e
	}
	defer finishTx()
	s, e := readStudioSource(ctx, tx, "", hash)
	if e != nil {
		return out, e
	}
	out.SourceID = s.id
	var id, owner, intent string
	var generation, epoch int64
	e = tx.QueryRowContext(ctx, `SELECT recording_id::text,actor_id::text,key_generation,COALESCE(capture_intent_id::text,''),COALESCE(epoch,0) FROM hhc_web.member_recording_broadcast_output WHERE source_id=$1 AND output_id=$2`, s.id, output).Scan(&id, &owner, &generation, &intent, &epoch)
	if e == nil {
		if actor != owner {
			return out, recordings.ErrForbidden
		}
		if generation != s.generation {
			return out, recordings.ErrBroadcastOutputTerminal
		}
		b, _, c, err := lockBroadcast(ctx, tx, id)
		if errors.Is(err, recordings.ErrNotFound) {
			return out, recordings.ErrBroadcastOutputTerminal
		}
		if err != nil {
			return out, err
		}
		if b.CaptureIntentID != intent || b.Epoch != epoch {
			return out, recordings.ErrBroadcastOutputTerminal
		}
		var available bool
		err = tx.QueryRowContext(ctx, `SELECT r.deleted_at IS NULL AND c.input_active FROM hhc_web.member_recording r JOIN hhc_web.member_recording_capture c ON c.recording_id=r.id WHERE r.id=$1 AND c.id=$2`, id, b.CaptureIntentID).Scan(&available)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (!available || b.Revoked || c == nil) {
			return out, recordings.ErrBroadcastOutputTerminal
		}
		if err != nil {
			return out, err
		}
		out.Broadcast = b
		if c.SegmentFrames != frames || c.RequestedRecoveryVersion != option.RecoveryVersion {
			return out, recordings.ErrConflict
		}
		out.Capture = *c
		return out, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	if !allowNew {
		return out, assetclient.ErrUnavailable
	}
	var busy bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture WHERE input_active) OR EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast_source WHERE prepared AND id<>$1)`, s.id).Scan(&busy); e != nil {
		return out, e
	}
	if busy {
		var own bool
		if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c JOIN hhc_web.member_recording_broadcast b ON b.recording_id=c.recording_id WHERE c.input_active AND b.source_id=$1)`, s.id).Scan(&own); e != nil {
			return out, e
		}
		if own {
			return out, recordings.ErrBroadcastSourceBusy
		}
		return out, recordings.ErrBroadcastOutputCapacity
	}
	var b recordings.Broadcast
	if s.target.Valid {
		target, v, c, err := lockBroadcast(ctx, tx, s.target.String)
		if err != nil && !errors.Is(err, recordings.ErrNotFound) {
			return out, err
		}
		if err == nil && c == nil && sourceTargetEligible(target, v) {
			var deleted bool
			if e = tx.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM hhc_web.member_recording WHERE id=$1`, s.target.String).Scan(&deleted); e != nil {
				return out, e
			}
			if !deleted {
				var delegate string
				e = tx.QueryRowContext(ctx, `SELECT actor_id::text FROM hhc_web.member_recording_broadcast_source_operation WHERE operation='target' AND recording_id=$1 ORDER BY occurred_at DESC LIMIT 1`, s.target.String).Scan(&delegate)
				if e != nil {
					return out, e
				}
				if delegate != actor {
					return out, recordings.ErrForbidden
				}
				b = target
			}
		}
	}
	if b.View.RecordingID == "" {
		if s.actor == "" {
			return out, recordings.ErrBroadcastSourceNotPrepared
		}
		if s.actor != actor {
			return out, recordings.ErrForbidden
		}
		b, e = r.ensureDirectTx(ctx, tx, &s, actor, now)
		if e != nil {
			return out, e
		}
	}
	if b.CaptureIntentID != "" {
		return out, recordings.ErrBroadcastSourceBusy
	}
	id = b.View.RecordingID
	b, v, c, e := lockBroadcast(ctx, tx, id)
	if e != nil {
		return out, e
	}
	in := recordings.BroadcastBindInput{ProtocolVersion: recordings.BroadcastProtocolVersion, BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: output + ".bind", ExpectedRevision: b.View.Revision}}
	b, capture, e := r.reserveBroadcastBinding(ctx, tx, id, actor, in, now, b, v, c)
	if e != nil {
		return out, e
	}
	capture.SegmentFrames = frames
	capture.RequestedRecoveryVersion = option.RecoveryVersion
	if s.direct.Valid && s.direct.String == b.View.RecordingID && b.SourceManaged && b.View.ScheduledAt == nil && b.View.Policy.AutoStart && b.View.Policy.AutoEnd {
		capture.RecoveryVersion = option.RecoveryVersion
	}
	if e = saveCapture(ctx, tx, capture); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_broadcast_output(source_id,output_id,key_generation,recording_id,actor_id,created_at,capture_intent_id,epoch) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, s.id, output, s.generation, id, actor, now, capture.IntentID, b.Epoch); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET target_recording_id=$2,revision=revision+1 WHERE id=$1`, s.id, id); e != nil {
		return out, e
	}
	out.Broadcast = b
	out.Capture = capture
	return out, tx.Commit()
}
