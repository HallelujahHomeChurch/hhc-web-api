package postgres

import (
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"testing"
	"time"
)

func TestCommentVisibleCountAndReplyTarget(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	id, user := b.View.RecordingID, p.ActorID
	if _, e := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,announced}','true') WHERE recording_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	root, e := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), Text: "Root"}, now)
	if e != nil {
		t.Fatal(e)
	}
	reply, e := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), ParentID: root.ID, Text: "Reply"}, now)
	if e != nil {
		t.Fatal(e)
	}
	var input recordings.CommentInput
	body, _ := json.Marshal(map[string]string{"clientMutationId": platform.NewID(), "parentId": root.ID, "replyToId": reply.ID, "text": "Targeted reply"})
	if e = json.Unmarshal(body, &input); e != nil {
		t.Fatal(e)
	}
	targeted, e := repo.CreateRecordingComment(ctx, id, user, input, now)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(targeted)
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if target, ok := got["replyTo"].(map[string]any); !ok || target["id"] != reply.ID {
		t.Fatalf("reply target absent: %s", data)
	}
	summary, e := repo.RecordingInteractions(ctx, id, user, now)
	if e != nil {
		t.Fatal(e)
	}
	data, _ = json.Marshal(summary)
	_ = json.Unmarshal(data, &got)
	if got["commentCount"] != float64(3) {
		t.Fatalf("count: %s", data)
	}
	if _, e = repo.UpdateRecordingComment(ctx, id, user, reply.ID, "", 1, true, now); e != nil {
		t.Fatal(e)
	}
	page, e := repo.ListRecordingComments(ctx, id, user, root.ID, "", "oldest", 20, now)
	if e != nil {
		t.Fatal(e)
	}
	for _, c := range page.Items {
		if c.ID == targeted.ID {
			data, _ = json.Marshal(c)
			_ = json.Unmarshal(data, &got)
			if c.ReplyTo != nil {
				t.Fatalf("deleted target leaked: %s", data)
			}
		}
	}
	if _, e = repo.CreateRecordingComment(ctx, id, user, input, now); e != nil {
		t.Fatalf("idempotent retry must survive target deletion: %v", e)
	}
	input.ClientMutationID = platform.NewID()
	if _, e = repo.CreateRecordingComment(ctx, id, user, input, now); !errors.Is(e, recordings.ErrInvalid) {
		t.Fatalf("deleted target: %v", e)
	}
	if _, e = repo.UpdateRecordingComment(ctx, id, user, root.ID, "", 1, true, now); e != nil {
		t.Fatal(e)
	}
	input.ClientMutationID = platform.NewID()
	input.ReplyToID = targeted.ID
	if _, e = repo.CreateRecordingComment(ctx, id, user, input, now); e != nil {
		t.Fatalf("reply to visible reply under deleted root: %v", e)
	}
	input.ClientMutationID = platform.NewID()
	input.ReplyToID = root.ID
	if _, e = repo.CreateRecordingComment(ctx, id, user, input, now); !errors.Is(e, recordings.ErrInvalid) {
		t.Fatalf("deleted root target: %v", e)
	}
	input.ClientMutationID = platform.NewID()
	input.ReplyToID = ""
	if _, e = repo.CreateRecordingComment(ctx, id, user, input, now); !errors.Is(e, recordings.ErrInvalid) {
		t.Fatalf("untargeted reply to deleted root: %v", e)
	}
	if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording_comment SET hidden=true WHERE id=$1`, root.ID); e != nil {
		t.Fatal(e)
	}
	summary, e = repo.RecordingInteractions(ctx, id, user, now)
	if e != nil || summary.CommentCount != 0 {
		t.Fatalf("hidden thread: %+v %v", summary, e)
	}
}

func TestCommentReactionAndStableRanking(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	id, user := b.View.RecordingID, p.ActorID
	if _, e := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,announced}','true') WHERE recording_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	comments := []recordings.Comment{}
	for i := 0; i < 3; i++ {
		c, e := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), Text: "Comment"}, now.Add(time.Duration(i)*time.Second))
		if e != nil {
			t.Fatal(e)
		}
		comments = append(comments, c)
	}
	like := "like"
	dislike := "dislike"
	for i := 0; i < 2; i++ {
		r, e := repo.SetCommentReaction(ctx, id, user, comments[0].ID, &like, now)
		if e != nil || r.Likes != 1 {
			t.Fatalf("desired state: %+v %v", r, e)
		}
	}
	first, e := repo.ListTopRecordingComments(ctx, id, user, "", 1, false, now)
	if e != nil || len(first.Items) != 1 || first.Items[0].ID != comments[0].ID {
		t.Fatalf("top first: %+v %v", first, e)
	}
	if _, e = repo.SetCommentReaction(ctx, id, user, comments[0].ID, &dislike, now); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.SetCommentReaction(ctx, id, user, comments[1].ID, &like, now); e != nil {
		t.Fatal(e)
	}
	second, e := repo.ListTopRecordingComments(ctx, id, user, first.NextCursor, 20, false, now)
	if e != nil || len(second.Items) != 2 || second.Items[0].ID != comments[2].ID || second.Items[1].ID != comments[1].ID {
		t.Fatalf("unstable traversal: %+v %v", second, e)
	}
	if _, e = repo.ListTopRecordingComments(ctx, id, platform.NewID(), first.NextCursor, 20, false, now); !errors.Is(e, recordings.ErrInvalid) {
		t.Fatalf("foreign cursor: %v", e)
	}
	if _, e = repo.ListTopRecordingComments(ctx, id, user, first.NextCursor, 20, false, now.Add(16*time.Minute)); !errors.Is(e, recordings.ErrCommentCursorExpired) {
		t.Fatalf("expired: %v", e)
	}
	if _, e = repo.SetCommentReaction(ctx, id, user, comments[1].ID, nil, now); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording_interaction_settings SET comments_enabled=false WHERE recording_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.SetCommentReaction(ctx, id, user, comments[1].ID, &like, now); !errors.Is(e, recordings.ErrCommentsClosed) {
		t.Fatalf("closed reaction: %v", e)
	}
	cleanup := New(repo.db)
	if _, remaining, e := cleanup.CleanupWatermarkAccount(ctx, user, "comments:"+platform.NewID()); e != nil || remaining != 0 {
		t.Fatalf("cleanup %d %v", remaining, e)
	}
	for _, table := range []string{"member_recording_comment_reaction", "member_recording_comment_rank"} {
		var count int
		if e := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.`+table+` WHERE account_id=$1`, user).Scan(&count); e != nil || count != 0 {
			t.Fatalf("private rows retained %s %d %v", table, count, e)
		}
	}
}
