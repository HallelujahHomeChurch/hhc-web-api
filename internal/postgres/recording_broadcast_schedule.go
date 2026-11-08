package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"time"
)

func (r *RecordingRepository) GetStudioBroadcast(ctx context.Context, id string) (recordings.StudioBroadcast, error) {
	var out recordings.StudioBroadcast
	b, e := r.GetBroadcast(ctx, id)
	if e != nil {
		return out, e
	}
	out.Broadcast = b.View
	if b.Projection != nil {
		out.DurationSeconds = b.Projection.DurationSeconds
	}
	e = r.db.QueryRowContext(ctx, `SELECT source_id::text,studio_mode FROM hhc_web.member_recording_broadcast WHERE recording_id=$1`, id).Scan(&out.SourceID, &out.Mode)
	if e != nil {
		return out, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT phase,signal,observed_at FROM hhc_web.member_recording_broadcast_signal WHERE recording_id=$1 ORDER BY id DESC LIMIT 200`, id)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var observation recordings.StudioSignalObservation
		if e = rows.Scan(&observation.Phase, &observation.Signal, &observation.ObservedAt); e != nil {
			return out, e
		}
		out.SignalHistory = append(out.SignalHistory, observation)
	}
	return out, rows.Err()
}
func (r *RecordingRepository) CreateStudioSchedule(ctx context.Context, actor string, in recordings.StudioScheduleInput, now time.Time) (recordings.StudioBroadcast, error) {
	var out recordings.StudioBroadcast
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return out, e
	}
	defer finishTx()
	source, e := readStudioSource(ctx, tx, in.SourceID, "")
	if e != nil {
		return out, e
	}
	digest := broadcastDigest(in)
	var priorActor, priorDigest, id string
	e = tx.QueryRowContext(ctx, `SELECT actor_id::text,digest,recording_id::text FROM hhc_web.member_recording_studio_operation WHERE operation_key=$1`, in.OperationKey).Scan(&priorActor, &priorDigest, &id)
	if e == nil {
		if priorActor != actor || priorDigest != digest {
			return out, recordings.ErrBroadcastOperation
		}
		b, _, _, err := lockBroadcast(ctx, tx, id)
		if err != nil {
			return out, err
		}
		out = recordings.StudioBroadcast{Broadcast: b.View, SourceID: &source.id, Mode: "scheduled"}
		return out, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	cover := in.Cover
	if cover == nil {
		cover = &recordings.LiveCoverSelection{Mode: "auto"}
	}
	create := recordings.BroadcastCreateInput{OperationKey: in.OperationKey, Title: in.Title, Description: in.Description, ScheduledAt: in.ScheduledAt, Policy: recordings.BroadcastPolicy{AutoEnd: true, AutoPublish: true}, Cover: cover}
	b, e := r.createBroadcastTx(ctx, tx, create, actor, now)
	if e != nil {
		return out, e
	}
	b.SourceManaged = true
	if e = saveBroadcast(ctx, tx, b); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast SET source_id=$2,source_managed=true,studio_mode='scheduled' WHERE recording_id=$1`, b.View.RecordingID, in.SourceID); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_studio_operation(operation_key,source_id,actor_id,digest,recording_id,created_at)VALUES($1,$2,$3,$4,$5,$6)`, in.OperationKey, in.SourceID, actor, digest, b.View.RecordingID, now); e != nil {
		return out, e
	}
	out = recordings.StudioBroadcast{Broadcast: b.View, SourceID: &source.id, Mode: "scheduled"}
	return out, tx.Commit()
}

func (r *RecordingRepository) AssignStudioSource(ctx context.Context, id, actor string, in recordings.StudioSourceAssignment, now time.Time) (recordings.StudioBroadcast, error) {
	var out recordings.StudioBroadcast
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return out, e
	}
	defer finishTx()
	source, e := readStudioSource(ctx, tx, in.SourceID, "")
	if e != nil {
		return out, e
	}
	digest := broadcastDigest(struct {
		ID    string
		Input recordings.StudioSourceAssignment
	}{id, in})
	var priorActor, priorDigest string
	e = tx.QueryRowContext(ctx, `SELECT actor_id::text,digest FROM hhc_web.member_recording_studio_operation WHERE operation_key=$1`, in.OperationKey).Scan(&priorActor, &priorDigest)
	replay := e == nil
	if replay && (priorActor != actor || priorDigest != digest) {
		return out, recordings.ErrBroadcastOperation
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return out, e
	}
	b, v, c, e := lockBroadcast(ctx, tx, id)
	if e != nil {
		return out, e
	}
	var mode, oldSource string
	if e = tx.QueryRowContext(ctx, `SELECT studio_mode,COALESCE(source_id::text,'') FROM hhc_web.member_recording_broadcast WHERE recording_id=$1`, id).Scan(&mode, &oldSource); e != nil {
		return out, e
	}
	out = recordings.StudioBroadcast{Broadcast: b.View, SourceID: &source.id, Mode: mode}
	if replay {
		if oldSource != source.id {
			return out, recordings.ErrBroadcastOperation
		}
		return out, tx.Commit()
	}
	if b.View.Revision != in.ExpectedRevision || source.revision != in.ExpectedSourceRevision {
		return out, recordings.ErrPrecondition
	}
	if mode != "scheduled" || c != nil || !sourceTargetEligible(b, v) {
		return out, recordings.ErrBroadcastSourceNotPrepared
	}
	var busy, wasPrepared bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture WHERE input_active),EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast_source WHERE target_recording_id=$1 AND prepared)`, id).Scan(&busy, &wasPrepared); e != nil {
		return out, e
	}
	if busy || source.prepared && (!source.target.Valid || source.target.String != id) {
		return out, recordings.ErrBroadcastSourceBusy
	}
	if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET target_recording_id=NULL,prepared=false,revision=revision+1 WHERE target_recording_id=$1 AND id<>$2`, id, source.id); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast SET source_id=$2 WHERE recording_id=$1`, id, source.id); e != nil {
		return out, e
	}
	if wasPrepared {
		if source.hash == "" {
			return out, recordings.ErrBroadcastSourceNotPrepared
		}
		if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET target_recording_id=$2,prepared=true,revision=revision+1 WHERE id=$1`, source.id, id); e != nil {
			return out, e
		}
		// Preserve the authorized preparation delegate on the moved reservation.
		if e = writeSourceOperation(ctx, tx, in.OperationKey, actor, "target", digest, source.revision+1, id, "", 0, now); e != nil {
			return out, e
		}
	}
	b.View.Revision++
	if e = saveBroadcast(ctx, tx, b); e != nil {
		return out, e
	}
	if e = r.enqueueAudit(ctx, tx, "cms.recording.title.update", id, actor, now); e != nil {
		return out, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_studio_operation(operation_key,source_id,actor_id,digest,recording_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, in.OperationKey, source.id, actor, digest, id, now); e != nil {
		return out, e
	}
	out.Broadcast = b.View
	return out, tx.Commit()
}
