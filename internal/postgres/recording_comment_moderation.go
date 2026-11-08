package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"time"
)

func (r *RecordingRepository) ReportRecordingComment(ctx context.Context, id, user, comment, reason string, now time.Time) error {
	tx, finishTx, e := r.interactionTransaction(ctx, id, user, now)
	if e != nil {
		return e
	}
	defer finishTx()
	var allowed bool
	e = tx.QueryRowContext(ctx, `SELECT NOT c.hidden AND NOT c.deleted AND (c.parent_id IS NULL OR EXISTS(SELECT 1 FROM hhc_web.member_recording_comment p WHERE p.id=c.parent_id AND NOT p.hidden)) FROM hhc_web.member_recording_comment c WHERE c.id=$1 AND c.recording_id=$2 FOR SHARE`, comment, id).Scan(&allowed)
	if errors.Is(e, sql.ErrNoRows) || e == nil && !allowed {
		return recordings.ErrNotFound
	}
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_comment_report(comment_id,account_id,reason,created_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, comment, user, reason, now)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (r *RecordingRepository) ModerateRecordingComments(ctx context.Context, id, actor string, input recordings.ModerationInput, now time.Time) error {
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorID != actor || !auditclient.ValidActorRequest(actor, p.RequestID) {
		return recordings.ErrAuditUnavailable
	}
	tx, finishTx, e := r.beginRecordingTransaction(ctx)
	if e != nil {
		return e
	}
	defer finishTx()
	if e = lockWatermarkSubject(ctx, tx, actor); e != nil {
		return e
	}
	restricted, e := watermarkRestricted(ctx, tx, actor)
	if e != nil {
		return e
	}
	if restricted {
		return bulletinwatermark.ErrRestricted
	}
	var exists string
	if e = tx.QueryRowContext(ctx, `SELECT id::text FROM hhc_web.member_recording WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id).Scan(&exists); errors.Is(e, sql.ErrNoRows) {
		return recordings.ErrNotFound
	} else if e != nil {
		return e
	}
	if input.Operation == "settings" {
		if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_interaction_settings(recording_id) VALUES($1) ON CONFLICT DO NOTHING`, id); e != nil {
			return e
		}
		var revision int64
		var enabled bool
		if e = tx.QueryRowContext(ctx, `SELECT revision,comments_enabled FROM hhc_web.member_recording_interaction_settings WHERE recording_id=$1 FOR UPDATE`, id).Scan(&revision, &enabled); e != nil {
			return e
		}
		if enabled == *input.Enabled {
			return nil
		}
		if revision != input.ExpectedRevision {
			return recordings.ErrConflict
		}
		_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_interaction_settings SET comments_enabled=$2,revision=revision+1 WHERE recording_id=$1`, id, *input.Enabled)
	} else {
		var revision int64
		var hidden bool
		if e = tx.QueryRowContext(ctx, `SELECT revision,hidden FROM hhc_web.member_recording_comment WHERE id=$1 AND recording_id=$2 FOR UPDATE`, input.CommentID, id).Scan(&revision, &hidden); errors.Is(e, sql.ErrNoRows) {
			return recordings.ErrNotFound
		} else if e != nil {
			return e
		}
		if revision != input.ExpectedRevision {
			return recordings.ErrConflict
		}
		if input.Operation == "resolve" {
			_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_comment_report SET resolved_at=$2 WHERE comment_id=$1 AND resolved_at IS NULL`, input.CommentID, now)
		} else {
			_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_comment SET hidden=$2,revision=revision+1 WHERE id=$1`, input.CommentID, input.Operation == "hide")
		}
	}
	if e != nil {
		return e
	}
	payload, _ := json.Marshal(struct {
		CommentID string `json:"commentId,omitempty"`
		RequestID string `json:"requestId"`
	}{input.CommentID, p.RequestID})
	_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.cms_audit_event(id,action,resource_type,resource_id,actor,payload_json,created_at) VALUES($1,$2,'recording',$3,$4,$5,$6)`, platform.NewID(), "recording.comment."+input.Operation, id, actor, payload, now)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (r *RecordingRepository) ListRecordingModeration(ctx context.Context, id, cursor string, limit int) (recordings.ModerationPage, error) {
	out := recordings.ModerationPage{Items: []recordings.ModerationComment{}}
	var exists bool
	if e := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&exists); e != nil {
		return out, e
	}
	if !exists {
		return out, recordings.ErrNotFound
	}
	if e := r.db.QueryRowContext(ctx, `SELECT COALESCE((SELECT comments_enabled FROM hhc_web.member_recording_interaction_settings WHERE recording_id=$1),true),COALESCE((SELECT revision FROM hhc_web.member_recording_interaction_settings WHERE recording_id=$1),1)`, id).Scan(&out.CommentsEnabled, &out.Revision); e != nil {
		return out, e
	}
	rows, e := r.db.QueryContext(ctx, `SELECT c.id::text,COALESCE(c.parent_id::text,''),COALESCE(c.account_id::text,''),c.body,c.hidden,c.deleted,c.revision,c.created_at,c.edited_at,(SELECT count(*) FROM hhc_web.member_recording_comment_report q WHERE q.comment_id=c.id AND q.resolved_at IS NULL),COALESCE((SELECT jsonb_agg(q.reason ORDER BY q.created_at) FROM (SELECT reason,created_at FROM hhc_web.member_recording_comment_report WHERE comment_id=c.id AND resolved_at IS NULL ORDER BY created_at LIMIT 10) q),'[]') FROM hhc_web.member_recording_comment c WHERE c.recording_id=$1 AND ($2='' OR (c.created_at,c.id)<(SELECT created_at,id FROM hhc_web.member_recording_comment WHERE id=NULLIF($2,'')::uuid AND recording_id=$1)) ORDER BY c.created_at DESC,c.id DESC LIMIT $3`, id, cursor, limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var v recordings.ModerationComment
		var reasons []byte
		c := &v.Comment
		if e = rows.Scan(&c.ID, &c.ParentID, &c.AuthorID, &c.Text, &c.Hidden, &c.Deleted, &c.Revision, &c.CreatedAt, &c.EditedAt, &v.Reports, &reasons); e != nil {
			return out, e
		}
		if e = json.Unmarshal(reasons, &v.Reasons); e != nil {
			return out, e
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > limit {
		out.NextCursor = out.Items[limit-1].Comment.ID
		out.Items = out.Items[:limit]
	}
	return out, nil
}
