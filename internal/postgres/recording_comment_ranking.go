package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/google/uuid"
)

func decorateComments(ctx context.Context, tx *sql.Tx, comments []recordings.Comment, user string) error {
	if len(comments) == 0 {
		return nil
	}
	ids := make([]string, len(comments))
	byID := map[string]*recordings.Comment{}
	for i := range comments {
		ids[i] = comments[i].ID
		byID[ids[i]] = &comments[i]
	}
	rows, e := tx.QueryContext(ctx, `SELECT c.id::text,
 (SELECT count(*) FROM hhc_web.member_recording_comment_reaction r WHERE r.comment_id=c.id AND r.reaction='like'),
 (SELECT reaction FROM hhc_web.member_recording_comment_reaction r WHERE r.comment_id=c.id AND r.account_id=$2),
 COALESCE(t.id::text,''),COALESCE(t.account_id::text,'')
 FROM hhc_web.member_recording_comment c LEFT JOIN hhc_web.member_recording_comment t ON t.id=c.reply_to_id AND NOT t.hidden AND NOT t.deleted
 WHERE c.id=ANY($1::uuid[])`, ids, user)
	if e != nil {
		return e
	}
	defer rows.Close()
	for rows.Next() {
		var id, target, author string
		var count int64
		var reaction sql.NullString
		if e = rows.Scan(&id, &count, &reaction, &target, &author); e != nil {
			return e
		}
		c := byID[id]
		if c.Deleted || c.Hidden {
			continue
		}
		c.Likes = count
		if reaction.Valid {
			v := reaction.String
			c.Reaction = &v
		}
		if target != "" {
			c.ReplyTo = &recordings.ReplyTarget{ID: target, AuthorID: author, DisplayName: "會員"}
		}
	}
	return rows.Err()
}
func readComment(ctx context.Context, tx *sql.Tx, id, user string) (recordings.Comment, error) {
	c, e := scanComment(tx.QueryRowContext(ctx, `SELECT `+commentColumns+` FROM hhc_web.member_recording_comment c WHERE c.id=$1`, id), user)
	if e != nil {
		return c, e
	}
	items := []recordings.Comment{c}
	e = decorateComments(ctx, tx, items, user)
	return items[0], e
}
func (r *RecordingRepository) SetCommentReaction(ctx context.Context, id, user, comment string, reaction *string, now time.Time) (recordings.CommentReaction, error) {
	var out recordings.CommentReaction
	if _, e := uuid.Parse(comment); e != nil {
		return out, recordings.ErrInvalid
	}
	if reaction != nil && *reaction != "like" && *reaction != "dislike" {
		return out, recordings.ErrInvalid
	}
	tx, finishTx, e := r.interactionTransaction(ctx, id, user, now)
	if e != nil {
		return out, e
	}
	defer finishTx()
	// Match creation's settings lock so closing comments fences writes.
	if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_interaction_settings(recording_id) VALUES($1) ON CONFLICT DO NOTHING`, id); e != nil {
		return out, e
	}
	var enabled bool
	if e = tx.QueryRowContext(ctx, `SELECT comments_enabled FROM hhc_web.member_recording_interaction_settings WHERE recording_id=$1 FOR SHARE`, id).Scan(&enabled); e != nil {
		return out, e
	}
	if !enabled {
		return out, recordings.ErrCommentsClosed
	}
	var parent sql.NullString
	var hidden, deleted bool
	e = tx.QueryRowContext(ctx, `SELECT parent_id::text,hidden,deleted FROM hhc_web.member_recording_comment WHERE id=$1 AND recording_id=$2 FOR SHARE`, comment, id).Scan(&parent, &hidden, &deleted)
	if errors.Is(e, sql.ErrNoRows) || e == nil && (hidden || deleted) {
		return out, recordings.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	if parent.Valid {
		if e = tx.QueryRowContext(ctx, `SELECT hidden FROM hhc_web.member_recording_comment WHERE id=$1 FOR SHARE`, parent.String).Scan(&hidden); e != nil {
			return out, e
		}
		if hidden {
			return out, recordings.ErrNotFound
		}
	}
	if reaction == nil {
		_, e = tx.ExecContext(ctx, `DELETE FROM hhc_web.member_recording_comment_reaction WHERE comment_id=$1 AND account_id=$2`, comment, user)
	} else {
		_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_comment_reaction(comment_id,account_id,reaction,updated_at) VALUES($1,$2,$3,$4) ON CONFLICT(comment_id,account_id) DO UPDATE SET reaction=EXCLUDED.reaction,updated_at=EXCLUDED.updated_at`, comment, user, *reaction, now)
	}
	if e != nil {
		return out, e
	}
	c, e := readComment(ctx, tx, comment, user)
	if e != nil {
		return out, e
	}
	return c.CommentReaction, tx.Commit()
}

func (r *RecordingRepository) ListTopRecordingComments(ctx context.Context, id, user, cursor string, limit int, refresh bool, now time.Time) (recordings.CommentPage, error) {
	return r.listTopRecordingComments(ctx, id, user, cursor, limit, refresh, now, false)
}

func (r *RecordingRepository) listTopRecordingComments(ctx context.Context, id, user, cursor string, limit int, refresh bool, now time.Time, threaded bool) (recordings.CommentPage, error) {
	out := recordings.CommentPage{Items: []recordings.Comment{}}
	if limit < 1 || limit > 50 || len(cursor) > 128 || refresh && cursor != "" {
		return out, recordings.ErrInvalid
	}
	tx, finishTx, e := r.interactionTransaction(ctx, id, user, now)
	if e != nil {
		return out, e
	}
	defer finishTx()
	snapshot := ""
	offset := int64(0)
	if cursor != "" {
		parts := strings.Split(cursor, ":")
		if len(parts) != 2 {
			return out, recordings.ErrInvalid
		}
		if _, e = uuid.Parse(parts[0]); e != nil {
			return out, recordings.ErrInvalid
		}
		snapshot = parts[0]
		offset, e = strconv.ParseInt(parts[1], 10, 64)
		if e != nil || offset < 0 {
			return out, recordings.ErrInvalid
		}
		var expires time.Time
		e = tx.QueryRowContext(ctx, `SELECT expires_at FROM hhc_web.member_recording_comment_rank WHERE id=$1 AND recording_id=$2 AND account_id=$3 FOR SHARE`, snapshot, id, user).Scan(&expires)
		if errors.Is(e, sql.ErrNoRows) {
			return out, recordings.ErrInvalid
		}
		if e != nil {
			return out, e
		}
		if !expires.After(now) {
			return out, recordings.ErrCommentCursorExpired
		}
	} else {
		// Serialize first-page requests across tabs without holding a global lock.
		if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, id+":"+user+":comments"); e != nil {
			return out, e
		}
		var expires time.Time
		e = tx.QueryRowContext(ctx, `SELECT id::text,expires_at FROM hhc_web.member_recording_comment_rank WHERE recording_id=$1 AND account_id=$2`, id, user).Scan(&snapshot, &expires)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return out, e
		}
		if snapshot == "" || refresh || !expires.After(now) {
			if _, e = tx.ExecContext(ctx, `DELETE FROM hhc_web.member_recording_comment_rank WHERE recording_id=$1 AND account_id=$2`, id, user); e != nil {
				return out, e
			}
			snapshot = platform.NewID()
			if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_comment_rank(id,recording_id,account_id,expires_at) VALUES($1,$2,$3,$4)`, snapshot, id, user, now.Add(15*time.Minute)); e != nil {
				return out, e
			}
			_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_comment_rank_item(snapshot_id,ordinal,comment_id)
 SELECT $1,row_number() OVER(ORDER BY (SELECT count(*) FROM hhc_web.member_recording_comment_reaction r WHERE r.comment_id=c.id AND r.reaction='like') DESC,c.created_at DESC,c.id DESC),c.id
 FROM hhc_web.member_recording_comment c WHERE c.recording_id=$2 AND c.parent_id IS NULL AND NOT c.hidden`, snapshot, id)
			if e != nil {
				return out, e
			}
		}
	}
	rows, e := tx.QueryContext(ctx, `SELECT `+commentColumns+`,i.ordinal FROM hhc_web.member_recording_comment_rank_item i JOIN hhc_web.member_recording_comment c ON c.id=i.comment_id WHERE i.snapshot_id=$1 AND i.ordinal>$2 AND NOT c.hidden ORDER BY i.ordinal LIMIT $3`, snapshot, offset, limit+1)
	if e != nil {
		return out, e
	}
	ordinals := []int64{}
	for rows.Next() {
		var c recordings.Comment
		var ordinal int64
		if e = rows.Scan(&c.ID, &c.ParentID, &c.AuthorID, &c.Text, &c.Hidden, &c.Deleted, &c.Revision, &c.CreatedAt, &c.EditedAt, &c.ReplyCount, &ordinal); e != nil {
			rows.Close()
			return out, e
		}
		c.Mine = c.AuthorID == user
		if c.Deleted {
			c.Text = ""
			c.AuthorID = ""
		}
		out.Items = append(out.Items, c)
		ordinals = append(ordinals, ordinal)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(out.Items) > limit {
		out.NextCursor = snapshot + ":" + strconv.FormatInt(ordinals[limit-1], 10)
		out.Items = out.Items[:limit]
	}
	if !threaded {
		if e = decorateComments(ctx, tx, out.Items, user); e != nil {
			return out, e
		}
	}
	if threaded {
		out.Items, e = r.enrichThreadedComments(ctx, tx, id, user, out.Items)
		if e != nil {
			return out, e
		}
	}
	return out, tx.Commit()
}
