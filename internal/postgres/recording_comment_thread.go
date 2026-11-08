package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/google/uuid"
)

const maxThreadCommentNodes = 4096

func (r *RecordingRepository) ListThreadedRecordingComments(ctx context.Context, id, user, parent, cursor, order string, limit int, refresh bool, now time.Time) (recordings.CommentPage, error) {
	if parent == "" {
		if order == "top" {
			return r.listTopRecordingComments(ctx, id, user, cursor, limit, refresh, now, true)
		}
		return r.listRecordingComments(ctx, id, user, "", cursor, order, limit, now, true)
	}
	out := recordings.CommentPage{Items: []recordings.Comment{}}
	if _, err := uuid.Parse(parent); err != nil {
		return out, recordings.ErrInvalid
	}
	tx, finish, err := r.interactionTransaction(ctx, id, user, now)
	if err != nil {
		return out, err
	}
	defer finish()
	var root string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(c.parent_id,c.id)::text FROM hhc_web.member_recording_comment c
 JOIN hhc_web.member_recording_comment root ON root.id=COALESCE(c.parent_id,c.id) AND root.recording_id=c.recording_id
 WHERE c.id=$1 AND c.recording_id=$2 AND NOT root.hidden`, parent, id).Scan(&root)
	if errors.Is(err, sql.ErrNoRows) {
		return out, recordings.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if cursor != "" {
		if _, err = uuid.Parse(cursor); err != nil {
			return out, recordings.ErrInvalid
		}
		var exists bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording_comment
 WHERE id=$1 AND recording_id=$2 AND parent_id=$3 AND COALESCE(reply_to_id,parent_id)=$4)`, cursor, id, root, parent).Scan(&exists)
		if err != nil {
			return out, err
		}
		if !exists {
			return out, recordings.ErrCommentCursorExpired
		}
	}
	out.Items, err = r.threadedComments(ctx, tx, id, user, parent, []string{}, cursor, order, limit+1, true)
	if err != nil {
		return out, err
	}
	if len(out.Items) > limit {
		out.NextCursor = out.Items[limit-1].ID
		out.Items = out.Items[:limit]
	}
	out.Items = visibleThreadComments(out.Items)
	return out, nil
}

func (r *RecordingRepository) enrichThreadedComments(ctx context.Context, tx *sql.Tx, id, user string, items []recordings.Comment) ([]recordings.Comment, error) {
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	fresh, err := r.threadedComments(ctx, tx, id, user, "", ids, "", "oldest", len(items), false)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]recordings.Comment, len(fresh))
	for _, c := range fresh {
		byID[c.ID] = c
	}
	out := make([]recordings.Comment, 0, len(fresh))
	for _, c := range items {
		if current, ok := byID[c.ID]; ok {
			out = append(out, current)
		}
	}
	return visibleThreadComments(out), nil
}

func (r *RecordingRepository) threadedComments(ctx context.Context, tx *sql.Tx, id, user, parent string, ids []string, cursor, order string, limit int, descendants bool) ([]recordings.Comment, error) {
	direction, compare := "DESC", "<"
	if order == "oldest" {
		direction, compare = "ASC", ">"
	}
	// Only the returned seed page enters recursion. Root totals use stored parent_id.
	seed := `SELECT c.* FROM hhc_web.member_recording_comment c
 JOIN hhc_web.member_recording_comment root ON root.id=COALESCE(c.parent_id,c.id) AND root.recording_id=c.recording_id AND NOT root.hidden
 WHERE c.recording_id=$1 AND (($2='' AND c.id=ANY($3::uuid[])) OR ($2<>'' AND c.parent_id IS NOT NULL AND COALESCE(c.reply_to_id,c.parent_id)=NULLIF($2,'')::uuid))
 AND ($4='' OR (c.created_at,c.id) ` + compare + ` (SELECT created_at,id FROM hhc_web.member_recording_comment WHERE id=NULLIF($4,'')::uuid))
 ORDER BY c.created_at ` + direction + `,c.id ` + direction + ` LIMIT $5`
	branchSeed := `SELECT c.id AS seed_id,c.id,c.account_id,c.hidden,c.deleted,c.created_at,NULL::uuid AS first_child_id,COALESCE(c.parent_id,c.id) AS root_id FROM seeds c`
	recurse := ""
	direct := "COALESCE(s.children,0)"
	total := "COALESCE(s.descendants,0)"
	preview := `SELECT b.account_id FROM bounded b WHERE b.seed_id=c.id AND b.id=b.first_child_id AND NOT b.hidden AND NOT b.deleted AND b.account_id IS NOT NULL GROUP BY b.account_id ORDER BY min(b.created_at),b.account_id LIMIT 1`
	if !descendants {
		// Removed paths stop at their first visible node: only traversability is needed.
		branchSeed = `SELECT c.id AS seed_id,d.id,d.account_id,d.hidden,d.deleted,d.created_at,d.id AS first_child_id,COALESCE(c.parent_id,c.id) AS root_id
  FROM seeds c JOIN hhc_web.member_recording_comment d ON d.recording_id=$1 AND d.parent_id=COALESCE(c.parent_id,c.id) AND COALESCE(d.reply_to_id,d.parent_id)=c.id WHERE d.hidden OR d.deleted`
		recurse = " AND (b.hidden OR b.deleted)"
		direct = `(SELECT count(*) FROM hhc_web.member_recording_comment d WHERE d.recording_id=$1 AND d.parent_id=COALESCE(c.parent_id,c.id) AND COALESCE(d.reply_to_id,d.parent_id)=c.id AND NOT d.hidden AND NOT d.deleted)+COALESCE(s.children,0)`
		total = "NULL::bigint"
		preview = `SELECT d.account_id FROM hhc_web.member_recording_comment d WHERE d.recording_id=$1 AND d.parent_id=COALESCE(c.parent_id,c.id) AND COALESCE(d.reply_to_id,d.parent_id)=c.id AND NOT d.hidden AND NOT d.deleted AND d.account_id IS NOT NULL ORDER BY d.created_at,d.id LIMIT 1`
	}
	query := `WITH RECURSIVE seeds AS MATERIALIZED (` + seed + `),branch AS (` + branchSeed + `
 UNION ALL
 SELECT b.seed_id,d.id,d.account_id,d.hidden,d.deleted,d.created_at,CASE WHEN b.id=b.seed_id THEN d.id ELSE b.first_child_id END,b.root_id
 FROM branch b JOIN hhc_web.member_recording_comment d ON d.recording_id=$1 AND d.parent_id=b.root_id AND COALESCE(d.reply_to_id,d.parent_id)=b.id` + recurse + `
 ),bounded AS MATERIALIZED (SELECT * FROM branch LIMIT ($6::int+1)),summary AS (
 SELECT seed_id,count(*) FILTER(WHERE id<>seed_id AND NOT hidden AND NOT deleted) AS descendants,
 count(DISTINCT first_child_id) FILTER(WHERE NOT hidden AND NOT deleted) AS children
 FROM bounded GROUP BY seed_id)
 SELECT ` + commentColumns + `,COALESCE(c.reply_to_id::text,c.parent_id::text,''),` + direct + `,` + total + `,
 COALESCE((SELECT jsonb_agg(p.account_id::text) FROM (` + preview + `) p),'[]'::jsonb),
 (SELECT count(*)>$6 FROM bounded)
 FROM seeds c LEFT JOIN summary s ON s.seed_id=c.id ORDER BY c.created_at ` + direction + `,c.id ` + direction
	queryCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	rows, err := tx.QueryContext(queryCtx, query, id, parent, ids, cursor, limit, maxThreadCommentNodes)
	if err != nil {
		if errors.Is(queryCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, recordings.ErrCommentReadTimeout
		}
		return nil, fmt.Errorf("threaded comment read: %w", err)
	}
	items := []recordings.Comment{}
	overBudget := false
	for rows.Next() {
		var c recordings.Comment
		var direct int
		var total sql.NullInt64
		var exceeded bool
		var preview []byte
		if err = rows.Scan(&c.ID, &c.ParentID, &c.AuthorID, &c.Text, &c.Hidden, &c.Deleted, &c.Revision, &c.CreatedAt, &c.EditedAt, &c.ReplyCount, &c.ThreadParentID, &direct, &total, &preview, &exceeded); err != nil {
			rows.Close()
			return nil, err
		}
		c.DirectChildCount = &direct
		if total.Valid {
			n := int(total.Int64)
			c.DescendantReplyCount = &n
		}
		if c.ParentID == "" {
			n := c.ReplyCount
			c.DescendantReplyCount = &n
		}
		overBudget = overBudget || exceeded
		c.Mine = c.AuthorID == user
		var authors []string
		if err = json.Unmarshal(preview, &authors); err != nil {
			rows.Close()
			return nil, err
		}
		for _, author := range authors {
			c.ReplyPreviewAuthors = append(c.ReplyPreviewAuthors, recordings.ReplyPreviewAuthor{AuthorID: author, DisplayName: "會員"})
		}
		if c.Hidden || c.Deleted {
			c.Text, c.AuthorID, c.DisplayName = "", "", ""
			c.Mine = false
			c.ReplyPreviewAuthors = nil
		}
		items = append(items, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		if errors.Is(queryCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, recordings.ErrCommentReadTimeout
		}
		return nil, err
	}
	if overBudget {
		if descendants {
			seeds := make([]string, len(items))
			for i := range items {
				seeds[i] = items[i].ID
			}
			return r.threadedComments(ctx, tx, id, user, "", seeds, "", order, limit, false)
		}
		for i := range items {
			if items[i].ParentID != "" {
				return nil, recordings.ErrCommentTraversalLimit
			}
			if items[i].ReplyCount > 0 {
				items[i].DirectChildCount = nil
				items[i].HasChildren = true
			} else {
				zero := 0
				items[i].DirectChildCount = &zero
			}
		}
	}
	if err = decorateComments(ctx, tx, items, user); err != nil {
		return nil, err
	}
	return items, nil
}

// Empty removed rows still advance the raw-page cursor, but never enter member output.
func visibleThreadComments(items []recordings.Comment) []recordings.Comment {
	out := items[:0]
	for _, c := range items {
		if (!c.Hidden && !c.Deleted) || c.HasChildren || c.DirectChildCount != nil && *c.DirectChildCount > 0 {
			out = append(out, c)
		}
	}
	return out
}
