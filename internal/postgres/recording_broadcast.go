package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"strings"
	"time"
)

func broadcastDigest(v any) string {
	raw, _ := json.Marshal(v)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func scanBroadcast(row *sql.Row) (recordings.Broadcast, error) {
	var b recordings.Broadcast
	var raw []byte
	err := row.Scan(&raw, &b.View.Title, &b.View.Description, &b.SourceManaged)
	if errors.Is(err, sql.ErrNoRows) {
		return b, recordings.ErrNotFound
	}
	if err != nil {
		return b, err
	}
	title, description := b.View.Title, b.View.Description
	err = json.Unmarshal(raw, &b)
	b.View.Title, b.View.Description = title, description
	b.View.Cover = b.CoverSnapshot.Settings
	return b, err
}
func (r *RecordingRepository) GetBroadcast(ctx context.Context, id string) (recordings.Broadcast, error) {
	return readSubjectGetBroadcast(ctx, r.db, id)
}
func readSubjectGetBroadcast(ctx context.Context, q recordingRowReader, id string) (recordings.Broadcast, error) {
	return scanBroadcast(q.QueryRowContext(ctx, `SELECT b.payload_json,r.title_override,r.description,b.source_managed FROM hhc_web.member_recording_broadcast b JOIN hhc_web.member_recording r ON r.id=b.recording_id WHERE r.id::text=$1 AND r.deleted_at IS NULL`, id))
}
func saveBroadcast(ctx context.Context, tx *sql.Tx, b recordings.Broadcast) error {
	b.View.Title = ""
	b.View.Description = ""
	b.View.Cover = b.CoverSnapshot.Settings
	raw, err := json.Marshal(b)
	if err != nil {
		return err
	}
	// Small-table sequential plans must compare only the target's large journal.
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast SET payload_json=$2 WHERE recording_id=$1 AND CASE WHEN recording_id=$1 THEN payload_json IS DISTINCT FROM $2::jsonb ELSE false END`, b.View.RecordingID, raw)
	return err
}
func appendBroadcastAudit(ctx context.Context, tx *sql.Tx, b recordings.Broadcast, now time.Time) error {
	for key, op := range b.Operations {
		raw, e := json.Marshal(struct {
			Receipt recordings.BroadcastReceipt `json:"receipt"`
			Intent  json.RawMessage             `json:"intent"`
		}{op.Receipt, op.Intent})
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_broadcast_audit(id,recording_id,actor_id,request_id,operation_key,operation,digest,receipt_json,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(recording_id,operation_key) DO NOTHING`, platform.NewID(), b.View.RecordingID, op.ActorID, op.RequestID, key, op.Receipt.Operation, op.Digest, raw, now); e != nil {
			return e
		}
	}
	return nil
}
func snapshotBroadcastCover(ctx context.Context, tx *sql.Tx, b *recordings.Broadcast, selection *recordings.LiveCoverSelection, now time.Time) error {
	defaults, e := defaultLiveCover(ctx, tx, true)
	if e != nil {
		return e
	}
	defaults.Settings.InheritedRevision = defaults.Settings.Revision
	defaults.Settings.Revision = 1
	defaults.ReferenceID = platform.NewID()
	defaults.AutoReferenceID = platform.NewID()
	defaults.Scope = ""
	if defaults.Settings.Selection.Mode == "custom" {
		defaults.Scope = "defaults"
	}
	if selection != nil {
		defaults.Settings.Selection = *selection
		defaults.Scope = ""
		if selection.Mode == "custom" {
			defaults.Scope = "defaults"
		}
	}
	defaults.Settings.CoverID = ""
	defaults.Settings.ContentPath = ""
	defaults.Settings.CoverState = "waiting"
	if defaults.Settings.Selection.Mode == "custom" {
		defaults.Settings.CoverState = "pending"
		defaults.Settings.CoverID = defaults.Settings.Selection.UploadID
		if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_live_cover_reference(id,target,cover_id,scope,recording_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, defaults.ReferenceID, "broadcast:"+b.View.RecordingID, defaults.Settings.Selection.UploadID, defaults.Scope, "", now); e != nil {
			return e
		}
	}
	b.CoverSnapshot = defaults
	b.View.Cover = defaults.Settings
	return nil
}
func (r *RecordingRepository) CreateBroadcast(ctx context.Context, in recordings.BroadcastCreateInput, actor string, now time.Time) (recordings.Broadcast, error) {
	var b recordings.Broadcast
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor || r.auditOutbox == nil {
		return b, recordings.ErrAuditUnavailable
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return b, err
	}
	defer finishTx()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "broadcast-create:"+actor+":"+in.OperationKey); err != nil {
		return b, err
	}
	var id, digest string
	err = tx.QueryRowContext(ctx, `SELECT recording_id::text,digest FROM hhc_web.member_recording_broadcast_create WHERE actor_id::text=$1 AND operation_key=$2`, actor, in.OperationKey).Scan(&id, &digest)
	if err == nil {
		if digest != broadcastDigest(in) {
			return b, recordings.ErrBroadcastOperation
		}
		b, err = scanBroadcast(tx.QueryRowContext(ctx, `SELECT b.payload_json,r.title_override,r.description,b.source_managed FROM hhc_web.member_recording_broadcast b JOIN hhc_web.member_recording r ON r.id=b.recording_id WHERE r.id::text=$1 AND r.deleted_at IS NULL`, id))
		if err != nil {
			return b, err
		}
		return b, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return b, err
	}
	b, err = r.createBroadcastTx(ctx, tx, in, actor, now)
	if err != nil {
		return b, err
	}
	return b, tx.Commit()
}

func (r *RecordingRepository) createBroadcastTx(ctx context.Context, tx *sql.Tx, in recordings.BroadcastCreateInput, actor string, now time.Time) (recordings.Broadcast, error) {
	id := platform.NewID()
	var err error
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor {
		return recordings.Broadcast{}, recordings.ErrForbidden
	}
	intent, _ := json.Marshal(in)
	b := recordings.Broadcast{LiveNotificationPending: true, View: recordings.BroadcastView{RecordingID: id, Revision: 1, Phase: "draft", Title: in.Title, Description: in.Description, ScheduledAt: in.ScheduledAt, Policy: in.Policy, Boundaries: recordings.BroadcastBoundaries{Revision: 1}, Health: recordings.BroadcastHealth{State: "waiting"}, Archive: recordings.BroadcastArchive{State: "none"}}, CreatedAt: now, MemberState: "blocked", Commands: map[string]recordings.BroadcastCommand{}, Operations: map[string]recordings.BroadcastOperation{in.OperationKey: {ActorID: actor, RequestID: p.RequestID, Digest: broadcastDigest(in), Intent: intent, Receipt: recordings.BroadcastReceipt{OperationKey: in.OperationKey, Operation: "create", State: "completed"}}}}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording(id,title_override,create_title,description,draft_expires_at,idempotency_key,created_by,updated_by,created_at,updated_at,is_broadcast) VALUES($1,$2,$2,$3,$4,$5,$6,$6,$7,$7,true)`, id, in.Title, in.Description, now.Add(7*24*time.Hour), "broadcast:"+id, actor, now); err != nil {
		return b, err
	}
	if _, err = assignRecordingPublicID(ctx, tx, id, newRecordingPublicID); err != nil {
		return b, err
	}
	if err = snapshotBroadcastCover(ctx, tx, &b, in.Cover, now); err != nil {
		return b, err
	}
	raw, _ := json.Marshal(b)
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_broadcast(recording_id,payload_json,created_at) VALUES($1,$2,$3)`, id, raw, now); err != nil {
		return b, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_broadcast_create(actor_id,operation_key,recording_id,digest)VALUES($1,$2,$3,$4)`, actor, in.OperationKey, id, broadcastDigest(in)); err != nil {
		return b, err
	}
	if err = appendBroadcastAudit(ctx, tx, b, now); err != nil {
		return b, err
	}
	if err = r.enqueueAudit(ctx, tx, "cms.recording.create", id, actor, now); err != nil {
		return b, err
	}
	return b, nil
}
func lockBroadcast(ctx context.Context, tx *sql.Tx, id string) (recordings.Broadcast, recordings.Recording, *recordings.Capture, error) {
	var b recordings.Broadcast
	r, e := scanRecording(tx.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM hhc_web.member_recording WHERE id::text=$1 AND (deleted_at IS NULL OR is_broadcast) FOR UPDATE`, id))
	if e != nil {
		return b, r, nil, e
	}
	b, e = scanBroadcast(tx.QueryRowContext(ctx, `SELECT b.payload_json,r.title_override,r.description,b.source_managed FROM hhc_web.member_recording_broadcast b JOIN hhc_web.member_recording r ON r.id=b.recording_id WHERE r.id::text=$1 FOR UPDATE OF b`, id))
	if e != nil {
		return b, r, nil, e
	}
	var c *recordings.Capture
	if b.CaptureIntentID != "" {
		value, e := scanCapture(tx.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.member_recording_capture WHERE id::text=$1 FOR UPDATE`, b.CaptureIntentID))
		if e != nil {
			return b, r, nil, e
		}
		value.Status.RecordingVersion = r.Version
		c = &value
	}
	return b, r, c, nil
}
func (r *RecordingRepository) UpdateBroadcast(ctx context.Context, id, actor string, now time.Time, fn func(context.Context, *recordings.Broadcast, *recordings.Recording, *recordings.Capture) error) (recordings.Broadcast, error) {
	stageCtx, finish := diagnostics.Start(ctx, diagnostics.BroadcastSourceTx)
	tx, finishTx, e := r.sourceTransaction(stageCtx)
	finish(e)
	if e != nil {
		return recordings.Broadcast{}, e
	}
	defer finishTx()
	stageCtx, finish = diagnostics.Start(ctx, diagnostics.BroadcastLock)
	b, value, c, e := lockBroadcast(stageCtx, tx, id)
	finish(e)
	if e != nil {
		return b, e
	}
	before := b.CoverSnapshot
	wasStarted := b.StartedAt != nil
	title, description := value.Title, value.Description
	oldPreviews := make(map[string]recordings.BroadcastPreviewAudit, len(b.Previews))
	for scope, preview := range b.Previews {
		oldPreviews[scope] = preview
	}
	oldOps := len(b.Operations)
	stageCtx, finish = diagnostics.Start(ctx, diagnostics.BroadcastCallback)
	e = fn(stageCtx, &b, &value, c)
	finish(e)
	if e != nil {
		return b, e
	}
	if title != value.Title || description != value.Description {
		if actor == "" {
			return b, recordings.ErrForbidden
		}
		if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording SET title_override=$2,description=$3,version=version+1,human_edit_generation=human_edit_generation+1,updated_at=$4,updated_by=$5 WHERE id::text=$1`, id, value.Title, value.Description, now, actor); e != nil {
			return b, e
		}
		value.Version++
		if c != nil {
			c.Generation++
			c.Status.RecordingVersion = value.Version
		}
		if e = r.enqueueAudit(ctx, tx, "cms.recording.title.update", id, actor, now); e != nil {
			return b, e
		}
	}
	if b.View.Phase == "end_pending" || b.View.Boundaries.EndSequenceExclusive != nil || b.EndedAt != nil {
		b.CoverSnapshot.AutoFrozen = true
		if c != nil && c.LiveCover != nil {
			c.LiveCover.AutoFrozen = true
		}
	}
	if before.ReferenceID != b.CoverSnapshot.ReferenceID {
		if before.ReferenceID != "" {
			if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET wanted=false WHERE id::text=$1`, before.ReferenceID); e != nil {
				return b, e
			}
		}
		if b.CoverSnapshot.Settings.Selection.Mode == "custom" {
			owner := id
			if b.CoverSnapshot.Scope == "defaults" {
				owner = ""
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_live_cover_reference(id,target,cover_id,scope,recording_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, b.CoverSnapshot.ReferenceID, "broadcast:"+id, b.CoverSnapshot.Settings.Selection.UploadID, b.CoverSnapshot.Scope, owner, now); e != nil {
				return b, e
			}
		}
		if c != nil {
			c.LiveCover = &b.CoverSnapshot
		}
	}
	if c != nil {
		if e = saveCapture(ctx, tx, *c); e != nil {
			return b, e
		}
	}
	if !wasStarted && b.StartedAt != nil {
		b.LiveNotificationPending = true
	}
	if b.LiveNotificationPending && c != nil && !value.Expired(now) && b.LiveNotificationReady(*c, now) {
		if e = enqueueRecordingNotification(ctx, tx, id, value.Title, c.ActorID, value.Version, now, true); e != nil {
			return b, e
		}
		b.LiveNotificationPending = false
	}
	if e = saveBroadcast(ctx, tx, b); e != nil {
		return b, e
	}
	if len(b.Operations) > oldOps {
		if e = appendBroadcastAudit(ctx, tx, b, now); e != nil {
			return b, e
		}
	}
	for scope, preview := range b.Previews {
		prior, exists := oldPreviews[scope]
		if exists && prior == preview {
			continue
		}
		operationKey := "preview:" + scope
		if exists {
			operationKey += ":renew:" + platform.NewID()
		}
		raw, err := json.Marshal(preview)
		if err != nil {
			return b, err
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_broadcast_audit(id,recording_id,actor_id,request_id,operation_key,operation,digest,receipt_json,occurred_at) VALUES($1,$2,$3,$4,$5,'preview',$6,$7,$8)`, platform.NewID(), id, preview.ActorID, preview.RequestID, operationKey, broadcastDigest(preview), raw, now); e != nil {
			return b, e
		}
	}
	b.View.Title, b.View.Description = value.Title, value.Description
	b.View.Cover = b.CoverSnapshot.Settings
	if actor != "" && (title != value.Title || description != value.Description || before.ReferenceID != b.CoverSnapshot.ReferenceID) {
		if e = rememberDirectSettings(ctx, tx, b, now); e != nil {
			return b, e
		}
	}
	_, finish = diagnostics.Start(ctx, diagnostics.BroadcastCommit)
	e = tx.Commit()
	finish(e)
	return b, e
}
func (r *RecordingRepository) PrepareBroadcastBinding(ctx context.Context, id, actor string, in recordings.BroadcastBindInput, now time.Time) (recordings.Broadcast, recordings.Capture, error) {
	return r.prepareBroadcastBinding(ctx, id, actor, in, "", now)
}
func (r *RecordingRepository) PrepareBroadcastStreamKeyBinding(ctx context.Context, id, actor string, in recordings.BroadcastBindInput, keyHash string, now time.Time) (recordings.Broadcast, recordings.Capture, error) {
	if keyHash == "" {
		return recordings.Broadcast{}, recordings.Capture{}, recordings.ErrBroadcastStreamKey
	}
	return r.prepareBroadcastBinding(ctx, id, actor, in, keyHash, now)
}
func (r *RecordingRepository) prepareBroadcastBinding(ctx context.Context, id, actor string, in recordings.BroadcastBindInput, keyHash string, now time.Time) (recordings.Broadcast, recordings.Capture, error) {
	var empty recordings.Capture
	tx, finishTx, e := r.beginRecordingTransaction(ctx)
	if e != nil {
		return recordings.Broadcast{}, empty, e
	}
	defer finishTx()
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('recording-capture-single-input',0))`); e != nil {
		return recordings.Broadcast{}, empty, e
	}
	b, v, prior, e := lockBroadcast(ctx, tx, id)
	if e != nil {
		return b, empty, e
	}
	if e = b.CheckStreamKey(keyHash); e != nil {
		return b, empty, e
	}
	b, c, e := r.reserveBroadcastBinding(ctx, tx, id, actor, in, now, b, v, prior)
	if e != nil {
		return b, empty, e
	}
	return b, c, tx.Commit()
}
func (r *RecordingRepository) reserveBroadcastBinding(ctx context.Context, tx *sql.Tx, id, actor string, in recordings.BroadcastBindInput, now time.Time, b recordings.Broadcast, v recordings.Recording, prior *recordings.Capture) (recordings.Broadcast, recordings.Capture, error) {
	var empty recordings.Capture
	var e error
	digest := broadcastDigest(in)
	if op, ok := b.Operations[in.OperationKey]; ok {
		if op.ActorID != actor || op.Digest != digest || op.Receipt.Operation != "bind" || prior == nil {
			return b, empty, recordings.ErrBroadcastOperation
		}
		return b, *prior, nil
	}
	if b.View.Revision != in.ExpectedRevision {
		return b, empty, recordings.ErrPrecondition
	}
	if b.View.Boundaries.StartSequence != nil || b.View.Phase == "cancelled" || b.View.Phase == "archived" || b.Revoked && (prior == nil || !prior.Terminal()) || b.Epoch >= 9007199254740991 || b.View.Policy.AutoStart && !b.View.Announced {
		return b, empty, recordings.ErrConflict
	}
	if prior != nil && !prior.Terminal() {
		return b, empty, recordings.ErrBroadcastBound
	}
	var occupied bool
	if e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture WHERE input_active) OR EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast_source WHERE prepared AND target_recording_id<>$1)`, id).Scan(&occupied); e != nil {
		return b, empty, e
	}
	if occupied {
		return b, empty, recordings.ErrBroadcastActiveLimit
	}
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorID != actor || p.ActorType != "user" {
		return b, empty, recordings.ErrForbidden
	}
	b.Epoch++
	b.Recovery = recordings.BroadcastRecovery{State: "unsupported"}
	b.PublicEnd = nil
	b.Revoked = false
	b.MemberState = "blocked"
	b.RangePending = false
	b.Projection = nil
	b.View.PendingCommand = nil
	b.View.Archive = recordings.BroadcastArchive{State: "none"}
	b.AutoPublicationActivated = false
	input := recordings.CaptureCreateInput{OperationKey: in.OperationKey, ExpectedVersion: v.Version, AutoPublish: false, LiveEnabled: false}
	raw, _ := json.Marshal(input)
	c := recordings.Capture{BroadcastEpoch: b.Epoch, LiveCover: &b.CoverSnapshot, IntentID: platform.NewID(), ActorID: actor, ActorType: p.ActorType, RequestID: p.RequestID, RecordingID: id, Create: input, Status: recordings.CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{RecordingID: id, State: "creating", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Objects: []assetclient.RecordingCaptureObject{}, Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, RecordingVersion: v.Version, LiveState: "starting", AutoPublish: "cancelled"}, Operations: map[string]recordings.CaptureOperation{in.OperationKey: {Operation: "create", Digest: broadcastDigest(input), Input: raw, Pending: true}}}
	if e = tx.QueryRowContext(ctx, `SELECT human_edit_generation FROM hhc_web.member_recording WHERE id::text=$1`, id).Scan(&c.Generation); e != nil {
		return b, empty, e
	}
	raw, e = json.Marshal(c)
	if e != nil {
		return b, empty, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_capture(id,recording_id,actor_id,operation_key,active,payload_json,broadcast_epoch) VALUES($1,$2,$3,$4,true,$5,$6)`, c.IntentID, id, actor, in.OperationKey, raw, b.Epoch); e != nil {
		return b, empty, recordingConflict(e)
	}
	b.CaptureIntentID = c.IntentID
	b.View.Binding = nil
	b.View.Phase = "preview"
	b.View.Revision++
	b.View.Boundaries = recordings.BroadcastBoundaries{Revision: 1}
	b.RangeApplied = 1
	intent, _ := json.Marshal(in)
	b.Operations[in.OperationKey] = recordings.BroadcastOperation{ActorID: actor, RequestID: p.RequestID, Digest: digest, Intent: intent, Receipt: recordings.BroadcastReceipt{OperationKey: in.OperationKey, Operation: "bind", State: "accepted"}}
	if e = saveBroadcast(ctx, tx, b); e != nil {
		return b, empty, e
	}
	if e = appendBroadcastAudit(ctx, tx, b, now); e != nil {
		return b, empty, e
	}
	return b, c, nil
}
func (r *RecordingRepository) ListBroadcasts(ctx context.Context, cursor string, limit int) ([]recordings.Broadcast, error) {
	rows, e := r.db.QueryContext(ctx, `SELECT b.payload_json,r.title_override,r.description,b.source_managed FROM hhc_web.member_recording_broadcast b JOIN hhc_web.member_recording r ON r.id=b.recording_id WHERE r.deleted_at IS NULL AND ($1='' OR r.id::text>$1) ORDER BY r.id LIMIT $2`, cursor, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []recordings.Broadcast{}
	for rows.Next() {
		var b recordings.Broadcast
		var raw []byte
		var title, desc string
		if e = rows.Scan(&raw, &title, &desc, &b.SourceManaged); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &b); e != nil {
			return nil, e
		}
		b.View.Title, b.View.Description = title, desc
		b.View.Cover = b.CoverSnapshot.Settings
		out = append(out, b)
	}
	return out, rows.Err()
}
func (r *RecordingRepository) ReconcileBroadcastCleanup(ctx context.Context, now time.Time) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM hhc_web.member_recording_comment_rank WHERE id IN (SELECT id FROM hhc_web.member_recording_comment_rank WHERE expires_at<=$1 ORDER BY expires_at LIMIT 500)`, now); err != nil {
		return err
	}

	tx, finishTx, e := r.sourceTransaction(ctx)
	if e != nil {
		return e
	}
	defer finishTx()
	// Keep cancelled URLs; retire only their dedicated references after 30 days.
	_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference f SET wanted=false FROM hhc_web.member_recording_broadcast b WHERE f.target='broadcast:'||b.recording_id::text AND f.wanted AND ((b.payload_json->>'CancelledAt')::timestamptz<=$1::timestamptz-interval '30 days' OR EXISTS(SELECT 1 FROM hhc_web.member_recording r WHERE r.id=b.recording_id AND (r.deleted_at IS NOT NULL OR r.media_terminal)))`, now)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording r SET deleted_at=$1 WHERE r.is_broadcast AND r.deleted_at IS NULL AND r.uploaded_at IS NULL AND r.created_at<$1::timestamptz-interval '7 days' AND EXISTS(SELECT 1 FROM hhc_web.member_recording_broadcast b WHERE b.recording_id=r.id AND b.payload_json->'View'->>'announced'='false' AND b.payload_json->>'CaptureIntentID'='' AND b.payload_json->'View'->>'phase'='draft' AND GREATEST(r.created_at,(b.payload_json->'View'->>'scheduledAt')::timestamptz)<$1::timestamptz-interval '7 days')`, now)
	if e != nil {
		return e
	}
	if e = clearDeletedBroadcastSourceTarget(ctx, tx); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *RecordingRepository) WithRecordingSubject(ctx context.Context, user string, read func(recordings.SubjectReader) error) error {
	return r.withRecordingSubject(ctx, user, func(tx *sql.Tx) error { return read(subjectReader{tx}) })
}

func (r *RecordingRepository) withRecordingSubject(ctx context.Context, user string, read func(*sql.Tx) error) error {
	tx, finishTx, e := r.beginSubjectReadTransaction(ctx)
	if e != nil {
		return e
	}
	defer finishTx()
	if e = shareWatermarkSubject(ctx, tx, user); e != nil {
		return e
	}
	blocked, e := watermarkRestricted(ctx, tx, user)
	if e != nil {
		return e
	}
	if blocked {
		return bulletinwatermark.ErrRestricted
	}
	if e = read(tx); e != nil {
		return e
	}
	return tx.Commit()
}
func closeBroadcastPublicationTx(ctx context.Context, tx *sql.Tx, id string, reversible bool) error {
	b, e := scanBroadcast(tx.QueryRowContext(ctx, `SELECT b.payload_json,r.title_override,r.description,b.source_managed FROM hhc_web.member_recording_broadcast b JOIN hhc_web.member_recording r ON r.id=b.recording_id WHERE r.id::text=$1 FOR UPDATE OF b`, id))
	if errors.Is(e, recordings.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	// Only retained, completed VOD withdrawal is reversible; deletion and live closure stay terminal.
	var retained bool
	if reversible && !b.Revoked && b.Projection != nil && b.Projection.State == "ready" && b.View.Boundaries.StartSequence != nil && b.View.Boundaries.EndSequenceExclusive != nil {
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording r WHERE r.id::text=$1 AND r.deleted_at IS NULL AND NOT r.media_terminal AND r.package_id IS NOT NULL AND r.uploaded_at IS NOT NULL AND r.expires_at>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM hhc_web.member_recording_capture c WHERE c.id::text=$2 AND c.payload_json->'Status'->>'state' IN ('failed','expired','aborted')))`, id, b.CaptureIntentID).Scan(&retained)
		if e != nil {
			return e
		}
	}
	if retained {
		b.PublicationWithdrawn = true
		b.View.Archive = recordings.BroadcastArchive{State: "ready"}
	} else {
		b.RevokePublic()
	}
	b.MemberState = "blocked"
	b.RangePending = b.CaptureIntentID != ""
	b.View.Boundaries.Revision++
	b.View.Revision++
	if b.View.Boundaries.StartSequence == nil {
		b.View.Phase = "cancelled"
		now := time.Now().UTC()
		b.CancelledAt = &now
	} else {
		b.View.Phase = "processing"
	}
	return saveBroadcast(ctx, tx, b)
}
func (r *RecordingRepository) applyBroadcastCoverReference(ctx context.Context, ref recordings.LiveCoverReference, retained bool) error {
	id := strings.TrimPrefix(ref.Target, "broadcast:")
	tx, finishTx, e := r.beginRecordingTransaction(ctx)
	if e != nil {
		return e
	}
	defer finishTx()
	b, _, c, e := lockBroadcast(ctx, tx, id)
	if e != nil {
		return e
	}
	if b.Revoked || b.CoverSnapshot.ReferenceID != ref.ID || b.CoverSnapshot.Settings.Selection.Mode != "custom" || b.CoverSnapshot.Settings.Selection.UploadID != ref.CoverID {
		return recordings.ErrConflict
	}
	var wanted, applied bool
	var released sql.NullTime
	var target, scope, owner, cover string
	e = tx.QueryRowContext(ctx, `SELECT target,scope,recording_id,cover_id,wanted,applied,released_at FROM hhc_web.member_recording_live_cover_reference WHERE id=$1 FOR UPDATE`, ref.ID).Scan(&target, &scope, &owner, &cover, &wanted, &applied, &released)
	if e != nil {
		return e
	}
	if target != ref.Target || scope != ref.Scope || owner != ref.RecordingID || cover != ref.CoverID || !wanted || released.Valid {
		return recordings.ErrConflict
	}
	if !applied {
		if !retained {
			return errCoverRetainRequired
		}
		if _, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_live_cover_reference SET applied=true WHERE id=$1`, ref.ID); e != nil {
			return e
		}
	}
	b.CoverSnapshot.Settings.CoverState = "ready"
	b.CoverSnapshot.Settings.CoverID = ref.CoverID
	b.View.Cover = b.CoverSnapshot.Settings
	if c != nil {
		c.LiveCover = &b.CoverSnapshot
		if e = saveCapture(ctx, tx, *c); e != nil {
			return e
		}
	}
	if e = saveBroadcast(ctx, tx, b); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *RecordingRepository) ListBroadcastReconciliation(ctx context.Context, now time.Time, limit int) ([]recordings.Broadcast, error) {
	// Settled history must not displace active work; authority revocation and expiry still reconcile.
	rows, e := r.db.QueryContext(ctx, `WITH picked AS(
 SELECT b.recording_id FROM hhc_web.member_recording_broadcast b
 JOIN hhc_web.member_recording r ON r.id=b.recording_id
 WHERE b.payload_json->>'RangePending'='true'
 OR (b.payload_json->>'CaptureIntentID'<>'' AND (
   (b.payload_json->>'Revoked'='false' AND COALESCE(b.payload_json->>'PublicationWithdrawn','false')<>'true' AND b.payload_json->'View'->>'phase' NOT IN ('archived','cancelled','failed'))
   OR (b.payload_json->>'MemberState'<>'blocked' AND (r.deleted_at IS NOT NULL OR r.media_terminal OR r.expires_at<=$2 OR (r.status='draft' AND b.payload_json->'View'->>'phase'='archived')))))
 OR (b.payload_json->>'Revoked'='false' AND b.payload_json->'CoverSnapshot'->'Settings'->'selection'->>'mode'='custom' AND b.payload_json->'CoverSnapshot'->'Settings'->>'coverState'<>'ready')
 ORDER BY b.checked_at NULLS FIRST,b.recording_id LIMIT $1 FOR UPDATE OF b SKIP LOCKED) UPDATE hhc_web.member_recording_broadcast b SET checked_at=$2 FROM picked,hhc_web.member_recording r WHERE b.recording_id=picked.recording_id AND r.id=b.recording_id RETURNING b.payload_json,r.title_override,r.description,b.source_managed`, limit, now)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []recordings.Broadcast{}
	for rows.Next() {
		var b recordings.Broadcast
		var raw []byte
		var title, desc string
		if e = rows.Scan(&raw, &title, &desc, &b.SourceManaged); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &b); e != nil {
			return nil, e
		}
		b.View.Title, b.View.Description = title, desc
		out = append(out, b)
	}
	return out, rows.Err()
}

// Publication and its Worker authority intent share the existing publication
// transaction. A producer outage leaves member playback blocked until retry.
func publishBroadcastAuthorityTx(ctx context.Context, tx *sql.Tx, id string) error {
	b, e := scanBroadcast(tx.QueryRowContext(ctx, `SELECT b.payload_json,r.title_override,r.description,b.source_managed FROM hhc_web.member_recording_broadcast b JOIN hhc_web.member_recording r ON r.id=b.recording_id WHERE r.id::text=$1 FOR UPDATE OF b`, id))
	if errors.Is(e, recordings.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	if b.Revoked || b.Projection == nil || b.Projection.State != "ready" || b.View.Boundaries.EndSequenceExclusive == nil {
		return recordings.ErrConflict
	}
	b.PublicationWithdrawn = false
	b.MemberState = "vod"
	b.RangePending = true
	b.View.Boundaries.Revision++
	b.View.Revision++
	b.View.Phase = "archived"
	b.View.Archive.State = "published"
	b.View.Archive.Reason = nil
	return saveBroadcast(ctx, tx, b)
}
