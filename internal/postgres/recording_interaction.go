package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/google/uuid"
)

// Share the DSR subject fence and publication row lock with media authorization.
func (r *RecordingRepository) interactionTransaction(ctx context.Context, id, user string, now time.Time) (*sql.Tx, func(), error) {
	tx, finishTx, current, err := r.recordingMemberWrite(ctx, id, user)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			finishTx()
		}
	}()

	visible := current.Visible(now)
	if current.Broadcast && !current.Hidden && !current.Expired(now) {
		var body []byte
		e := tx.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.member_recording_broadcast WHERE recording_id=$1 FOR SHARE`, id).Scan(&body)
		if e != nil {
			return nil, nil, e
		}
		var b recordings.Broadcast
		if e = json.Unmarshal(body, &b); e != nil {
			return nil, nil, e
		}
		visible = b.View.Announced && !b.Revoked && b.View.Phase != "cancelled" && b.View.Phase != "failed"
	}
	if !visible {
		return nil, nil, recordings.ErrNotFound
	}
	ok = true
	return tx, finishTx, nil
}
func (r *RecordingRepository) recordingMemberWrite(ctx context.Context, id, user string) (*sql.Tx, func(), recordings.Recording, error) {
	if _, e := uuid.Parse(id); e != nil {
		return nil, nil, recordings.Recording{}, recordings.ErrInvalid
	}
	if _, e := uuid.Parse(user); e != nil {
		return nil, nil, recordings.Recording{}, recordings.ErrInvalid
	}
	tx, finishTx, e := r.beginRecordingTransaction(ctx)
	if e != nil {
		return nil, nil, recordings.Recording{}, e
	}
	ok := false
	defer func() {
		if !ok {
			finishTx()
		}
	}()
	if e = shareWatermarkSubject(ctx, tx, user); e != nil {
		return nil, nil, recordings.Recording{}, e
	}
	// Preserve user-wide interaction serialization without blocking protected reads.
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,20261023))`, "recording-interaction:"+watermarkSubjectHash(user)); e != nil {
		return nil, nil, recordings.Recording{}, e
	}
	blocked, e := watermarkRestricted(ctx, tx, user)
	if e != nil {
		return nil, nil, recordings.Recording{}, e
	}
	if blocked {
		return nil, nil, recordings.Recording{}, bulletinwatermark.ErrRestricted
	}
	current, e := scanRecording(tx.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM hhc_web.member_recording WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id))
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil, recordings.Recording{}, recordings.ErrNotFound
	}
	if e != nil {
		return nil, nil, recordings.Recording{}, e
	}
	ok = true
	return tx, finishTx, current, nil
}

func interactionSummary(ctx context.Context, tx *sql.Tx, id, user string) (recordings.InteractionSummary, error) {
	var v recordings.InteractionSummary
	e := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_like WHERE recording_id=$1 AND account_id=$2),(SELECT count(*) FROM hhc_web.member_recording_like WHERE recording_id=$1),COALESCE((SELECT comments_enabled FROM hhc_web.member_recording_interaction_settings WHERE recording_id=$1),true),(SELECT count(*) FROM hhc_web.member_recording_comment c WHERE c.recording_id=$1 AND NOT c.hidden AND NOT c.deleted AND (c.parent_id IS NULL OR EXISTS(SELECT 1 FROM hhc_web.member_recording_comment p WHERE p.id=c.parent_id AND NOT p.hidden)))`, id, user).Scan(&v.Liked, &v.Likes, &v.CommentsEnabled, &v.CommentCount)
	return v, e
}
func (r *RecordingRepository) RecordingInteractions(ctx context.Context, id, user string, now time.Time) (recordings.InteractionSummary, error) {
	tx, finishTx, e := r.interactionTransaction(ctx, id, user, now)
	if e != nil {
		return recordings.InteractionSummary{}, e
	}
	defer finishTx()
	return interactionSummary(ctx, tx, id, user)
}
func (r *RecordingRepository) SetRecordingLike(ctx context.Context, id, user string, liked bool, now time.Time) (recordings.InteractionSummary, error) {
	tx, finishTx, e := r.interactionTransaction(ctx, id, user, now)
	if e != nil {
		return recordings.InteractionSummary{}, e
	}
	defer finishTx()
	if liked {
		_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_like(recording_id,account_id,created_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, user, now)
	} else {
		_, e = tx.ExecContext(ctx, `DELETE FROM hhc_web.member_recording_like WHERE recording_id=$1 AND account_id=$2`, id, user)
	}
	if e != nil {
		return recordings.InteractionSummary{}, e
	}
	v, e := interactionSummary(ctx, tx, id, user)
	if e != nil {
		return v, e
	}
	return v, tx.Commit()
}

const commentColumns = `c.id::text,COALESCE(c.parent_id::text,''),COALESCE(c.account_id::text,''),c.body,c.hidden,c.deleted,c.revision,c.created_at,c.edited_at,(SELECT count(*) FROM hhc_web.member_recording_comment child WHERE child.parent_id=c.id AND NOT child.hidden AND NOT child.deleted)`

func scanComment(row interface{ Scan(...any) error }, user string) (recordings.Comment, error) {
	var c recordings.Comment
	e := row.Scan(&c.ID, &c.ParentID, &c.AuthorID, &c.Text, &c.Hidden, &c.Deleted, &c.Revision, &c.CreatedAt, &c.EditedAt, &c.ReplyCount)
	c.Mine = c.AuthorID == user
	if c.Hidden || c.Deleted {
		c.Text = ""
		c.AuthorID = ""
	}
	return c, e
}
func (r *RecordingRepository) CreateRecordingComment(ctx context.Context, id, user string, input recordings.CommentInput, now time.Time) (recordings.Comment, error) {
	if e := input.Validate(); e != nil {
		return recordings.Comment{}, e
	}
	tx, finishTx, e := r.interactionTransaction(ctx, id, user, now)
	if e != nil {
		return recordings.Comment{}, e
	}
	defer finishTx()
	data, _ := json.Marshal(input)
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	var previous, previousHash string
	e = tx.QueryRowContext(ctx, `SELECT id::text,request_hash FROM hhc_web.member_recording_comment WHERE recording_id=$1 AND account_id=$2 AND mutation_id=$3`, id, user, input.ClientMutationID).Scan(&previous, &previousHash)
	if e == nil {
		if previousHash != hash {
			return recordings.Comment{}, recordings.ErrConflict
		}
		return readComment(ctx, tx, previous, user)
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return recordings.Comment{}, e
	}
	// Lock the setting row so moderation cannot close comments during an insert.
	if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_interaction_settings(recording_id) VALUES($1) ON CONFLICT DO NOTHING`, id); e != nil {
		return recordings.Comment{}, e
	}
	var enabled bool
	if e = tx.QueryRowContext(ctx, `SELECT comments_enabled FROM hhc_web.member_recording_interaction_settings WHERE recording_id=$1 FOR SHARE`, id).Scan(&enabled); e != nil {
		return recordings.Comment{}, e
	}
	if !enabled {
		return recordings.Comment{}, recordings.ErrCommentsClosed
	}
	if input.ParentID != "" {
		var parent bool
		e = tx.QueryRowContext(ctx, `SELECT parent_id IS NULL AND NOT hidden AND (NOT deleted OR ($3<>'' AND $3<>id::text)) FROM hhc_web.member_recording_comment WHERE id=$1 AND recording_id=$2 FOR SHARE`, input.ParentID, id, input.ReplyToID).Scan(&parent)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !parent {
			return recordings.Comment{}, recordings.ErrInvalid
		}
		if e != nil {
			return recordings.Comment{}, e
		}
	}
	if input.ReplyToID != "" {
		var allowed bool
		e = tx.QueryRowContext(ctx, `SELECT NOT hidden AND NOT deleted AND (id=$2 OR parent_id=$2) FROM hhc_web.member_recording_comment WHERE id=$1 AND recording_id=$3 FOR SHARE`, input.ReplyToID, input.ParentID, id).Scan(&allowed)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !allowed {
			return recordings.Comment{}, recordings.ErrInvalid
		}
		if e != nil {
			return recordings.Comment{}, e
		}
	}
	commentID := platform.NewID()
	_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_comment(id,recording_id,parent_id,account_id,mutation_id,request_hash,body,created_at,reply_to_id) VALUES($1,$2,NULLIF($3,'')::uuid,$4,$5,$6,$7,$8,NULLIF($9,'')::uuid)`, commentID, id, input.ParentID, user, input.ClientMutationID, hash, input.Text, now, input.ReplyToID)
	if e != nil {
		return recordings.Comment{}, e
	}
	v, e := readComment(ctx, tx, commentID, user)
	if e != nil {
		return v, e
	}
	return v, tx.Commit()
}
func (r *RecordingRepository) ListRecordingComments(ctx context.Context, id, user, parent, cursor, order string, limit int, now time.Time) (recordings.CommentPage, error) {
	return r.listRecordingComments(ctx, id, user, parent, cursor, order, limit, now, false)
}

func (r *RecordingRepository) listRecordingComments(ctx context.Context, id, user, parent, cursor, order string, limit int, now time.Time, threaded bool) (recordings.CommentPage, error) {
	out := recordings.CommentPage{Items: []recordings.Comment{}}
	tx, finishTx, e := r.interactionTransaction(ctx, id, user, now)
	if e != nil {
		return out, e
	}
	defer finishTx()
	if parent != "" {
		var allowed bool
		e = tx.QueryRowContext(ctx, `SELECT parent_id IS NULL AND NOT hidden FROM hhc_web.member_recording_comment WHERE id=$1 AND recording_id=$2`, parent, id).Scan(&allowed)
		if errors.Is(e, sql.ErrNoRows) || e == nil && !allowed {
			return out, recordings.ErrNotFound
		}
		if e != nil {
			return out, e
		}
	}
	direction, compare := "DESC", "<"
	if order == "oldest" {
		direction, compare = "ASC", ">"
	}
	query := `SELECT ` + commentColumns + ` FROM hhc_web.member_recording_comment c WHERE c.recording_id=$1 AND c.parent_id IS NOT DISTINCT FROM NULLIF($2,'')::uuid AND NOT c.hidden AND ($3='' OR (c.created_at,c.id) ` + compare + ` (SELECT created_at,id FROM hhc_web.member_recording_comment WHERE id=NULLIF($3,'')::uuid AND recording_id=$1 AND parent_id IS NOT DISTINCT FROM NULLIF($2,'')::uuid)) ORDER BY c.created_at ` + direction + `,c.id ` + direction + ` LIMIT $4`
	rows, e := tx.QueryContext(ctx, query, id, parent, cursor, limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		c, e := scanComment(rows, user)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, c)
	}
	if e = rows.Err(); e != nil {
		return out, e
	}
	if len(out.Items) > limit {
		out.NextCursor = out.Items[limit-1].ID
		out.Items = out.Items[:limit]
	}
	if e = rows.Close(); e != nil {
		return out, e
	}
	if !threaded {
		if e = decorateComments(ctx, tx, out.Items, user); e != nil {
			return out, e
		}
	}
	if threaded {
		out.Items, e = r.enrichThreadedComments(ctx, tx, id, user, out.Items)
	}
	return out, e
}
func (r *RecordingRepository) UpdateRecordingComment(ctx context.Context, id, user, comment, text string, revision int64, remove bool, now time.Time) (recordings.Comment, error) {
	tx, finishTx, e := r.interactionTransaction(ctx, id, user, now)
	if e != nil {
		return recordings.Comment{}, e
	}
	defer finishTx()
	c, e := scanComment(tx.QueryRowContext(ctx, `SELECT `+commentColumns+` FROM hhc_web.member_recording_comment c WHERE c.id=$1 AND c.recording_id=$2 FOR UPDATE`, comment, id), user)
	if errors.Is(e, sql.ErrNoRows) {
		return c, recordings.ErrNotFound
	}
	if e != nil {
		return c, e
	}
	if !c.Mine {
		return c, recordings.ErrCommentForbidden
	}
	if c.Deleted && remove {
		return c, nil
	}
	if c.Revision != revision {
		return c, recordings.ErrConflict
	}
	if c.Hidden || c.Deleted {
		return c, recordings.ErrCommentForbidden
	}
	if remove {
		text = ""
	}
	_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_comment SET body=$2,deleted=$3,revision=revision+1,edited_at=$4 WHERE id=$1`, comment, text, remove, now)
	if e != nil {
		return c, e
	}
	c, e = readComment(ctx, tx, comment, user)
	if e != nil {
		return c, e
	}
	return c, tx.Commit()
}

const recordingInteractionPrivateCountSQL = `(SELECT count(*) FROM hhc_web.member_recording_progress WHERE account_id=$1::text::uuid)+(SELECT count(*) FROM hhc_web.member_recording_comment_reaction WHERE account_id=$1::text::uuid)+(SELECT count(*) FROM hhc_web.member_recording_comment_rank WHERE account_id=$1::text::uuid)+(SELECT count(*) FROM hhc_web.member_recording_playback_session WHERE account_id=$1::text::uuid)+(SELECT count(*) FROM hhc_web.member_recording_playback_visit WHERE account_id=$1::text::uuid)+(SELECT count(*) FROM hhc_web.member_recording_viewer WHERE account_id=$1::text::uuid)+(SELECT count(*) FROM hhc_web.cms_audit_event WHERE actor=$1::text AND action LIKE 'recording.comment.%')+(SELECT count(*) FROM hhc_web.member_recording_like WHERE account_id=$1::text::uuid)+(SELECT count(*) FROM hhc_web.member_recording_comment WHERE account_id=$1::text::uuid)+(SELECT count(*) FROM hhc_web.member_recording_comment_report WHERE account_id=$1::text::uuid)`
