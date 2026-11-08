package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestRecordingInteractionsVisibilityIdempotencyAndReplyBoundary(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	user := p.ActorID
	id := b.View.RecordingID
	if _, e := repo.SetRecordingLike(ctx, id, user, true, now); !errors.Is(e, recordings.ErrNotFound) {
		t.Fatalf("draft visible: %v", e)
	}
	if _, e := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(payload_json,'{View,announced}','true') WHERE recording_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := repo.SetRecordingLike(ctx, id, user, true, now); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	summary, e := repo.RecordingInteractions(ctx, id, user, now)
	if e != nil || summary.Likes != 1 || !summary.Liked {
		t.Fatalf("like: %+v %v", summary, e)
	}
	input := recordings.CommentInput{ClientMutationID: platform.NewID(), Text: "Hello"}
	first, e := repo.CreateRecordingComment(ctx, id, user, input, now)
	if e != nil {
		t.Fatal(e)
	}
	again, e := repo.CreateRecordingComment(ctx, id, user, input, now)
	if e != nil || again.ID != first.ID {
		t.Fatalf("retry %+v %v", again, e)
	}
	input.Text = "Changed"
	if _, e = repo.CreateRecordingComment(ctx, id, user, input, now); !errors.Is(e, recordings.ErrConflict) {
		t.Fatalf("changed retry %v", e)
	}
	reply, e := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), ParentID: first.ID, Text: "Reply"}, now)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), ParentID: reply.ID, Text: "Nested"}, now); !errors.Is(e, recordings.ErrInvalid) {
		t.Fatalf("nested: %v", e)
	}
	if _, e = repo.UpdateRecordingComment(ctx, id, platform.NewID(), first.ID, "Stolen", 1, false, now); !errors.Is(e, recordings.ErrCommentForbidden) {
		t.Fatalf("owner: %v", e)
	}
	deleted, e := repo.UpdateRecordingComment(ctx, id, user, first.ID, "", 1, true, now)
	if e != nil || !deleted.Deleted || deleted.Text != "" || deleted.AuthorID != "" {
		t.Fatalf("delete %+v %v", deleted, e)
	}
	page, e := repo.ListRecordingComments(ctx, id, user, first.ID, "", "oldest", 20, now)
	if e != nil || len(page.Items) != 1 || page.Items[0].ID != reply.ID {
		t.Fatalf("reply lost %+v %v", page, e)
	}
	if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording_comment SET hidden=true WHERE id=$1`, first.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.ListRecordingComments(ctx, id, user, first.ID, "", "oldest", 20, now); !errors.Is(e, recordings.ErrNotFound) {
		t.Fatalf("hidden parent replies leaked %v", e)
	}
	if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording_interaction_settings SET comments_enabled=false WHERE recording_id=$1`, id); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), Text: "Closed"}, now); !errors.Is(e, recordings.ErrCommentsClosed) {
		t.Fatalf("closed: %v", e)
	}

	if e = repo.ReportRecordingComment(ctx, id, user, reply.ID, "Inappropriate", now); !errors.Is(e, recordings.ErrNotFound) {
		t.Fatalf("report hidden parent's reply: %v", e)
	}
	if e = repo.ModerateRecordingComments(ctx, id, user, recordings.ModerationInput{Operation: "restore", CommentID: first.ID, ExpectedRevision: 2}, now); e != nil {
		t.Fatal(e)
	}
	if e = repo.ReportRecordingComment(ctx, id, user, reply.ID, "Inappropriate", now); e != nil {
		t.Fatal(e)
	}
	moderation, e := repo.ListRecordingModeration(ctx, id, "", 20)
	if e != nil || len(moderation.Items) != 2 {
		t.Fatalf("moderation %+v %v", moderation, e)
	}
	if e = repo.ModerateRecordingComments(ctx, id, user, recordings.ModerationInput{Operation: "resolve", CommentID: reply.ID, ExpectedRevision: 1}, now); e != nil {
		t.Fatal(e)
	}
	if e = seedWatermarkRestriction(ctx, repo.db, user); e != nil {
		t.Fatal(e)
	}
	if e = repo.ModerateRecordingComments(ctx, id, user, recordings.ModerationInput{Operation: "resolve", CommentID: reply.ID, ExpectedRevision: 1}, now); !errors.Is(e, bulletinwatermark.ErrRestricted) {
		t.Fatalf("moderation bypassed subject restriction: %v", e)
	}
	if _, e = repo.SetRecordingLike(ctx, id, user, false, now); !errors.Is(e, bulletinwatermark.ErrRestricted) {
		t.Fatalf("subject fence: %v", e)
	}
	if _, remaining, e := New(repo.db).CleanupWatermarkAccount(ctx, user, platform.NewID()); e != nil || remaining != 0 {
		t.Fatalf("erase %d %v", remaining, e)
	}
	var count int
	if e = repo.db.QueryRow(`SELECT `+recordingInteractionPrivateCountSQL, user).Scan(&count); e != nil || count != 0 {
		t.Fatalf("identity remains: %d %v", count, e)
	}
	var body, author string
	if e = repo.db.QueryRow(`SELECT body,COALESCE(account_id::text,'') FROM hhc_web.member_recording_comment WHERE id=$1`, reply.ID).Scan(&body, &author); e != nil || body != "" || author != "" {
		t.Fatalf("erased reply leaked: %v", e)
	}

}

func TestRecordingModerationWaitsForSubjectFence(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	tx, e := repo.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	if e = lockWatermarkSubject(ctx, tx, p.ActorID); e != nil {
		t.Fatal(e)
	}
	blocked, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	disabled := false
	e = repo.ModerateRecordingComments(blocked, b.View.RecordingID, p.ActorID, recordings.ModerationInput{Operation: "settings", ExpectedRevision: 1, Enabled: &disabled}, now)
	if e == nil || blocked.Err() != context.DeadlineExceeded {
		t.Fatalf("moderation did not wait for subject fence: %v", e)
	}
	if e = tx.Rollback(); e != nil {
		t.Fatal(e)
	}
	if e = repo.ModerateRecordingComments(ctx, b.View.RecordingID, p.ActorID, recordings.ModerationInput{Operation: "settings", ExpectedRevision: 1, Enabled: &disabled}, now); e != nil {
		t.Fatal(e)
	}
}

func TestRecordingAccountCleanupPreservesOtherSubjectsAndAnonymousMetrics(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	id, user, other := b.View.RecordingID, p.ActorID, platform.NewID()
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=jsonb_set(jsonb_set(payload_json,'{View,announced}','true'),'{View,phase}','"live"') WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	parent, err := repo.CreateRecordingComment(ctx, id, user, recordings.CommentInput{ClientMutationID: platform.NewID(), Text: "Owner comment"}, now)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := repo.CreateRecordingComment(ctx, id, other, recordings.CommentInput{ClientMutationID: platform.NewID(), ParentID: parent.ID, Text: "Other reply"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []string{user, other} {
		if _, err = repo.SetRecordingLike(ctx, id, account, true, now); err != nil {
			t.Fatal(err)
		}
		comment := reply.ID
		if account == other {
			comment = parent.ID
		}
		if err = repo.ReportRecordingComment(ctx, id, account, comment, "Report", now); err != nil {
			t.Fatal(err)
		}
		session := platform.NewID()
		for seq := int64(1); seq <= 2; seq++ {
			if err = repo.RecordPlaybackHeartbeat(ctx, id, account, recordings.PlaybackHeartbeat{PlaybackSessionID: session, Sequence: seq, State: "playing", Position: float64(seq * 15), DeltaPlayingMS: (seq - 1) * 15000}, 0, now.Add(time.Duration(seq-1)*15*time.Second)); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, err := repo.GetPlaybackMetrics(ctx, id, other, true, now.Add(20*time.Second))
	if err != nil || before.TotalViews != 2 || before.UniqueViewers != 2 || before.WatchSeconds != 30 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	key := "ordinary-video-cleanup:" + platform.NewID()
	cleanup := New(repo.db)
	count, remaining, err := cleanup.CleanupWatermarkAccount(ctx, user, key)
	if err != nil || count != 6 || remaining != 0 {
		t.Fatalf("cleanup count=%d remaining=%d err=%v", count, remaining, err)
	}
	replayCount, replayRemaining, err := cleanup.CleanupWatermarkAccount(ctx, user, key)
	if err != nil || replayCount != count || replayRemaining != 0 {
		t.Fatalf("cleanup replay count=%d remaining=%d err=%v", replayCount, replayRemaining, err)
	}
	for _, table := range []string{"member_recording_like", "member_recording_comment", "member_recording_comment_report", "member_recording_playback_session", "member_recording_playback_visit", "member_recording_viewer"} {
		for _, account := range []string{user, other} {
			want := 1
			if account == user {
				want = 0
			}
			var actual int
			if err = repo.db.QueryRow(`SELECT count(*) FROM hhc_web.`+table+` WHERE account_id=$1`, account).Scan(&actual); err != nil || actual != want {
				t.Fatalf("%s account=%s count=%d want=%d err=%v", table, account, actual, want, err)
			}
		}
	}
	var body, author string
	var deleted bool
	if err = repo.db.QueryRow(`SELECT body,COALESCE(account_id::text,''),deleted FROM hhc_web.member_recording_comment WHERE id=$1`, parent.ID).Scan(&body, &author, &deleted); err != nil || body != "" || author != "" || !deleted {
		t.Fatalf("parent tombstone body=%q author=%q deleted=%t err=%v", body, author, deleted, err)
	}
	var parentID string
	if err = repo.db.QueryRow(`SELECT body,parent_id::text FROM hhc_web.member_recording_comment WHERE id=$1`, reply.ID).Scan(&body, &parentID); err != nil || body != "Other reply" || parentID != parent.ID {
		t.Fatalf("reply lost body=%q parent=%s err=%v", body, parentID, err)
	}
	after, err := repo.GetPlaybackMetrics(ctx, id, other, true, now.Add(20*time.Second))
	if err != nil || after.TotalViews != before.TotalViews || after.LiveViews != before.LiveViews || after.WatchSeconds != before.WatchSeconds || after.UniqueViewers != 1 {
		t.Fatalf("anonymous totals changed before=%+v after=%+v err=%v", before, after, err)
	}
	if _, err = repo.SetRecordingLike(ctx, id, user, true, now); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatalf("erased user recreated like: %v", err)
	}
	if err = repo.RecordPlaybackHeartbeat(ctx, id, user, recordings.PlaybackHeartbeat{PlaybackSessionID: platform.NewID(), Sequence: 1, State: "playing"}, 0, now); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatalf("erased user recreated metrics: %v", err)
	}
}
