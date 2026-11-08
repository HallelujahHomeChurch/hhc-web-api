package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"time"
)

const studioSourceColumns = `s.id::text,s.name,s.revision,s.key_hash<>'',s.suffix,s.target_recording_id::text,s.direct_actor_id IS NOT NULL,
 EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c JOIN hhc_web.member_recording_broadcast b ON b.recording_id=c.recording_id WHERE b.source_id=s.id AND c.input_active)`

func scanStudioSource(row interface{ Scan(...any) error }) (recordings.StudioSource, error) {
	var s recordings.StudioSource
	e := row.Scan(&s.ID, &s.Name, &s.Revision, &s.Configured, &s.Suffix, &s.TargetRecordingID, &s.DirectEnabled, &s.InputBusy)
	if errors.Is(e, sql.ErrNoRows) {
		e = recordings.ErrNotFound
	}
	s.CanRotate = !s.InputBusy
	return s, e
}
func (r *RecordingRepository) ListStudioSources(ctx context.Context) ([]recordings.StudioSource, error) {
	rows, e := r.db.QueryContext(ctx, `SELECT `+studioSourceColumns+` FROM hhc_web.member_recording_broadcast_source s WHERE deleted_at IS NULL ORDER BY created_at,id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []recordings.StudioSource{}
	for rows.Next() {
		s, e := scanStudioSource(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
func (r *RecordingRepository) MutateStudioSource(ctx context.Context, id, actor, kind string, in recordings.StudioSourceInput, hash, suffix string, now time.Time) (recordings.StudioSource, error) {
	var empty recordings.StudioSource
	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return empty, e
	}
	defer finishTx()
	sealed := in.SealedKey
	in.SealedKey = recordings.SealedStreamKey{}
	in.StreamKey = ""
	digest := broadcastDigest(struct {
		ID, Kind, Hash string
		Input          recordings.StudioSourceInput
	}{id, kind, hash, in})
	var oldID, oldActor, oldDigest string
	e = tx.QueryRowContext(ctx, `SELECT source_id::text,actor_id::text,digest FROM hhc_web.member_recording_studio_operation WHERE operation_key=$1`, in.OperationKey).Scan(&oldID, &oldActor, &oldDigest)
	if e == nil {
		if oldActor != actor || oldDigest != digest {
			return empty, recordings.ErrBroadcastOperation
		}
		s, e := scanStudioSource(tx.QueryRowContext(ctx, `SELECT `+studioSourceColumns+` FROM hhc_web.member_recording_broadcast_source s WHERE id=$1`, oldID))
		if e != nil {
			return empty, e
		}
		return s, tx.Commit()
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return empty, e
	}
	if kind == "create" {
		id = platform.NewID()
		_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_broadcast_source(id,singleton,name,key_hash,suffix,created_at,key_ciphertext,key_encryption_id) VALUES($1,NULL,$2,$3,$4,$5,$6,$7)`, id, in.Name, hash, suffix, now, sealed.Ciphertext, sealed.KeyID)
	} else {
		current, err := scanStudioSource(tx.QueryRowContext(ctx, `SELECT `+studioSourceColumns+` FROM hhc_web.member_recording_broadcast_source s WHERE id=$1 AND deleted_at IS NULL FOR UPDATE OF s`, id))
		if err != nil {
			return empty, err
		}
		if current.Revision != in.ExpectedRevision {
			return empty, recordings.ErrPrecondition
		}
		if current.InputBusy && (kind == "key" || kind == "delete") {
			return empty, recordings.ErrBroadcastSourceBusy
		}
		switch kind {
		case "rename":
			_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET name=$2,revision=revision+1 WHERE id=$1`, id, in.Name)
		case "key":
			_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET key_hash=$2,suffix=$3,key_ciphertext=$4,key_encryption_id=$5,key_generation=key_generation+1,revision=revision+1 WHERE id=$1`, id, hash, suffix, sealed.Ciphertext, sealed.KeyID)
		case "delete":
			_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast_source SET key_hash='',suffix=NULL,key_ciphertext=NULL,key_encryption_id='',deleted_at=$2,prepared=false,direct_actor_id=NULL,target_recording_id=NULL,revision=revision+1 WHERE id=$1`, id, now)
		default:
			return empty, recordings.ErrInvalid
		}
	}
	if e != nil {
		return empty, recordingConflict(e)
	}
	if kind == "delete" {
		if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET wanted=false WHERE target=$1`, "source:"+id); e != nil {
			return empty, e
		}
	}
	if e = r.enqueueAudit(ctx, tx, "cms.site_settings.update", "singleton", actor, now); e != nil {
		return empty, e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_studio_operation(operation_key,source_id,actor_id,digest,created_at) VALUES($1,$2,$3,$4,$5)`, in.OperationKey, id, actor, digest, now)
	if e != nil {
		return empty, e
	}
	s, e := scanStudioSource(tx.QueryRowContext(ctx, `SELECT `+studioSourceColumns+` FROM hhc_web.member_recording_broadcast_source s WHERE id=$1`, id))
	if e != nil {
		return empty, e
	}
	return s, tx.Commit()
}
func (r *RecordingRepository) SetStudioSourceTarget(ctx context.Context, id, actor string, in recordings.BroadcastSourceTargetInput, now time.Time) (recordings.BroadcastSource, error) {
	return r.setBroadcastSourceTarget(ctx, id, actor, in, now)
}
