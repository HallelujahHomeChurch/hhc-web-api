package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestThreadedRecordingCommentsHierarchyAndLegacyCompatibility(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	id, user := b.View.RecordingID, p.ActorID
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,announced}','true') WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	create := func(parent, target, text string, offset time.Duration) recordings.Comment {
		t.Helper()
		c, err := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), ParentID: parent, ReplyToID: target, Text: text}, now.Add(offset))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	root := create("", "", "Root", 0)
	first := create(root.ID, "", "Historical untargeted reply", time.Second)
	second := create(root.ID, root.ID, "Second reply", 2*time.Second)
	grandchild := create(root.ID, first.ID, "Reply to a reply", 3*time.Second)

	page, err := repo.ListThreadedRecordingComments(ctx, id, user, "", "", "top", 20, false, now)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("roots: %+v %v", page, err)
	}
	got := page.Items[0]
	if got.ReplyCount != 3 || got.DirectChildCount == nil || *got.DirectChildCount != 2 || got.DescendantReplyCount == nil || *got.DescendantReplyCount != 3 || len(got.ReplyPreviewAuthors) != 1 {
		t.Fatalf("root must count all levels, not only its direct children: %+v", got)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, root.ID, "", "oldest", 1, false, now)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != first.ID || page.NextCursor == "" {
		t.Fatalf("first branch page: %+v %v", page, err)
	}
	got = page.Items[0]
	if got.ParentID != root.ID || got.ThreadParentID != root.ID || got.ReplyCount != 0 || got.DescendantReplyCount == nil || *got.DescendantReplyCount != 1 {
		t.Fatalf("stored root and structural parent must stay distinct: %+v", got)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, root.ID, page.NextCursor, "oldest", 1, false, now)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != second.ID || page.NextCursor != "" {
		t.Fatalf("second branch page: %+v %v", page, err)
	}
	if _, err = repo.ListThreadedRecordingComments(ctx, id, user, first.ID, second.ID, "oldest", 20, false, now); !errors.Is(err, recordings.ErrCommentCursorExpired) {
		t.Fatalf("sibling cursor accepted: %v", err)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, first.ID, "", "oldest", 20, false, now)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != grandchild.ID || page.Items[0].ThreadParentID != first.ID || page.Items[0].ParentID != root.ID {
		t.Fatalf("nested page: %+v %v", page, err)
	}
	legacy, err := repo.ListRecordingComments(ctx, id, user, root.ID, "", "oldest", 20, now)
	if err != nil || len(legacy.Items) != 3 {
		t.Fatalf("legacy flat traversal changed: %+v %v", legacy, err)
	}
	for _, c := range legacy.Items {
		data, _ := json.Marshal(c)
		var fields map[string]any
		_ = json.Unmarshal(data, &fields)
		for _, key := range []string{"threadParentId", "directChildCount", "descendantReplyCount", "replyPreviewAuthors"} {
			if _, exists := fields[key]; exists {
				t.Fatalf("threaded field %s leaked to a legacy response: %s", key, data)
			}
		}
	}
	if _, err = repo.UpdateRecordingComment(ctx, id, user, first.ID, "", 1, true, now); err != nil {
		t.Fatal(err)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, root.ID, "", "oldest", 20, false, now)
	if err != nil || len(page.Items) != 2 || !page.Items[0].Deleted || page.Items[0].Text != "" || page.Items[0].AuthorID != "" || page.Items[0].ReplyTo != nil || *page.Items[0].DescendantReplyCount != 1 {
		t.Fatalf("anonymous intermediate placeholder lost: %+v %v", page, err)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, first.ID, "", "oldest", 20, false, now)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != grandchild.ID || page.Items[0].ReplyTo != nil {
		t.Fatalf("deleted target must retain its descendants without identity: %+v %v", page, err)
	}
	if _, err = repo.UpdateRecordingComment(ctx, id, user, root.ID, "", 1, true, now); err != nil {
		t.Fatal(err)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, "", "", "top", 20, false, now)
	if err != nil || len(page.Items) != 1 || !page.Items[0].Deleted || page.Items[0].ReplyCount != 2 || *page.Items[0].DescendantReplyCount != 2 || len(page.Items[0].ReplyPreviewAuthors) != 0 {
		t.Fatalf("deleted root must preserve visible descendants without its identity: %+v %v", page, err)
	}
}

func TestThreadedRecordingCommentsModerationAndEmptyRemovedBranches(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	id, user := b.View.RecordingID, p.ActorID
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,announced}','true') WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	create := func(parent, target string) recordings.Comment {
		t.Helper()
		c, err := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), ParentID: parent, ReplyToID: target, Text: "Private identity must not survive removal"}, now)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	root := create("", "")
	parent := create(root.ID, root.ID)
	child := create(root.ID, parent.ID)
	if err := repo.ModerateRecordingComments(ctx, id, user, recordings.ModerationInput{Operation: "hide", CommentID: parent.ID, ExpectedRevision: 1}, now); err != nil {
		t.Fatal(err)
	}
	page, err := repo.ListThreadedRecordingComments(ctx, id, user, root.ID, "", "oldest", 20, false, now)
	if err != nil || len(page.Items) != 1 || !page.Items[0].Hidden || page.Items[0].Text != "" || page.Items[0].AuthorID != "" || *page.Items[0].DescendantReplyCount != 1 {
		t.Fatalf("hidden intermediate must be an anonymous traversable placeholder: %+v %v", page, err)
	}
	if _, err = repo.UpdateRecordingComment(ctx, id, user, child.ID, "", 1, true, now); err != nil {
		t.Fatal(err)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, root.ID, "", "oldest", 20, false, now)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("removed branches without visible descendants must disappear: %+v %v", page, err)
	}
	if err = repo.ModerateRecordingComments(ctx, id, user, recordings.ModerationInput{Operation: "restore", CommentID: parent.ID, ExpectedRevision: 2}, now); err != nil {
		t.Fatal(err)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, root.ID, "", "oldest", 20, false, now)
	if err != nil || len(page.Items) != 1 || page.Items[0].Hidden || page.Items[0].Text == "" {
		t.Fatalf("restore must retain stored content and position: %+v %v", page, err)
	}
	if err = repo.ModerateRecordingComments(ctx, id, user, recordings.ModerationInput{Operation: "hide", CommentID: root.ID, ExpectedRevision: 1}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ListThreadedRecordingComments(ctx, id, user, parent.ID, "", "oldest", 20, false, now); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatalf("known descendant bypassed hidden root: %v", err)
	}
}

func TestThreadedCommentsBoundBroadPagesAndLargeDescendantCounts(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	id, user := b.View.RecordingID, p.ActorID
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,announced}','true') WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	root, err := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), Text: "Root"}, now)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), ParentID: root.ID, ReplyToID: root.ID, Text: "Branch"}, now)
	if err != nil {
		t.Fatal(err)
	}
	count := maxThreadCommentNodes + 1000
	_, err = repo.db.Exec(`INSERT INTO hhc_web.member_recording_comment(id,recording_id,parent_id,reply_to_id,body,created_at)
 SELECT md5($1||g)::uuid,$2,$3,$4,'Synthetic reply',$5::timestamptz+g*interval '1 millisecond' FROM generate_series(1,$6)g`, platform.NewID(), id, root.ID, branch.ID, now, count)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repo.ListThreadedRecordingComments(ctx, id, user, "", "", "newest", 20, false, now)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("root fast path: %+v %v", page, err)
	}
	if got := page.Items[0]; got.DescendantReplyCount == nil || *got.DescendantReplyCount != count+1 || got.DirectChildCount == nil || *got.DirectChildCount != 1 {
		t.Fatalf("root totals: %+v", got)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, root.ID, "", "oldest", 20, false, now)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("large branch: %+v %v", page, err)
	}
	if got := page.Items[0]; got.DescendantReplyCount != nil || got.DirectChildCount == nil || *got.DirectChildCount != count {
		t.Fatalf("large count must be omitted, never truncated: %+v", got)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, branch.ID, "", "oldest", 20, false, now)
	if err != nil || len(page.Items) != 20 || page.NextCursor == "" {
		t.Fatalf("bounded sibling page: len=%d cursor=%s %v", len(page.Items), page.NextCursor, err)
	}
	for _, c := range page.Items {
		if c.DescendantReplyCount == nil || *c.DescendantReplyCount != 0 {
			t.Fatalf("unreturned siblings entered recursion: %+v", c)
		}
	}
	if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_comment SET deleted=true WHERE recording_id=$1 AND parent_id IS NOT NULL`, id); err != nil {
		t.Fatal(err)
	}
	_, err = repo.db.Exec(`INSERT INTO hhc_web.member_recording_comment(id,recording_id,parent_id,reply_to_id,body,created_at)
 SELECT $1,$2,$3,id,'Visible descendant',$4 FROM hhc_web.member_recording_comment WHERE recording_id=$2 AND reply_to_id=$5 LIMIT 1`, platform.NewID(), id, root.ID, now, branch.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err = repo.ListThreadedRecordingComments(ctx, id, user, "", "", "newest", 20, false, now)
	if err != nil || len(page.Items) != 1 || page.Items[0].DirectChildCount != nil || !page.Items[0].HasChildren || page.Items[0].DescendantReplyCount == nil || *page.Items[0].DescendantReplyCount != 1 {
		t.Fatalf("large removed paths need exact root total and traversability fallback: %+v %v", page, err)
	}
	if _, err = repo.ListThreadedRecordingComments(ctx, id, user, root.ID, "", "oldest", 20, false, now); !errors.Is(err, recordings.ErrCommentTraversalLimit) {
		t.Fatalf("unresolved nonroot removed paths must fail explicitly: %v", err)
	}
}

func TestThreadedCommentSQLDeadlineReleasesConnection(t *testing.T) {
	repo, b, ctx, _ := broadcastFixture(t)
	blocker, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback()
	if _, err = blocker.ExecContext(ctx, `LOCK TABLE hhc_web.member_recording_comment IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	tx, err := repo.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	started := time.Now()
	_, err = repo.threadedComments(ctx, tx, b.View.RecordingID, "", "", []string{platform.NewID()}, "", "oldest", 20, false)
	if !errors.Is(err, recordings.ErrCommentReadTimeout) || time.Since(started) > 2*time.Second {
		t.Fatalf("added SQL read deadline was not bounded: %v after %s", err, time.Since(started))
	}
	_ = tx.Rollback()
	_ = blocker.Rollback()
	check, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err = repo.db.PingContext(check); err != nil {
		t.Fatalf("connection unusable after canceled read: %v", err)
	}
}

func TestThreadedCommentsRemovedPagesAdvanceWithoutExposingEmptyNodes(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	id, user := b.View.RecordingID, p.ActorID
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,announced}','true') WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	root, err := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), Text: "Root"}, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.db.Exec(`INSERT INTO hhc_web.member_recording_comment(id,recording_id,parent_id,reply_to_id,body,deleted,created_at) SELECT md5($1||g)::uuid,$2,$3,$3,'',g<=25,$4::timestamptz+g*interval '1 millisecond' FROM generate_series(1,26)g`, platform.NewID(), id, root.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repo.ListThreadedRecordingComments(ctx, id, user, root.ID, "", "oldest", 20, false, now)
	if err != nil || len(first.Items) != 0 || first.NextCursor == "" {
		t.Fatalf("empty raw page did not advance: %+v %v", first, err)
	}
	next, err := repo.ListThreadedRecordingComments(ctx, id, user, root.ID, first.NextCursor, "oldest", 20, false, now)
	if err != nil || len(next.Items) != 1 || next.Items[0].Deleted || next.NextCursor != "" {
		t.Fatalf("next visible reply lost: %+v %v", next, err)
	}
}
