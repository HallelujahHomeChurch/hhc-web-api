package postgres

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/inboxcontent"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"testing"
	"time"
)

func TestInboxVisibilityRepositoryUsesExactCurrentPublishedResources(t *testing.T) {
	db := accessDB(t)
	clearBulletinAuditDB(t, db)
	now := time.Now().UTC()
	issue := seedBulletinAuditIssue(t, db, now, true)
	repo := New(db)
	ctx := context.Background()
	refs := []inboxcontent.Resource{{Type: "bulletin", ID: issue.ID, Series: "general"}, {Type: "bulletin", ID: issue.ID, Series: "children"}, {Type: "bulletin", ID: "safe-non-uuid", Series: "general"}}
	got, err := repo.InboxBulletins(ctx, refs)
	if err != nil || len(got) != 0 {
		t.Fatal("draft disclosed", got, err)
	}
	preparePublishedBulletinAuditIssue(t, db, issue.ID)
	if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,now(),version FROM hhc_web.bulletin_version WHERE issue_id=$1`, issue.ID); err != nil {
		t.Fatal(err)
	}
	got, err = repo.InboxBulletins(ctx, refs)
	if err != nil || len(got) != 1 || got[0].Series != "general" || got[0].Locale != "en" || got[0].IssueID != issue.ID || got[0].ProtectedAssetID == "" || got[0].Online {
		t.Fatal("projection", got, err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, issue.ID); err != nil {
		t.Fatal(err)
	}
	got, err = repo.InboxBulletins(ctx, refs)
	if err != nil || len(got) != 0 {
		t.Fatal("withdrawn disclosed", got, err)
	}
	actor := platform.NewID()
	allowed, err := repo.InboxSubjectAllowed(ctx, actor)
	if err != nil || !allowed {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES($1,true)`, watermarkSubjectHash(actor)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1`, watermarkSubjectHash(actor))
	})
	allowed, err = repo.InboxSubjectAllowed(ctx, actor)
	if err != nil || allowed {
		t.Fatal("cleanup fence ignored", err)
	}
	recordingRepo, v, _, _ := broadcastFixture(t)
	snapshots, err := recordingRepo.InboxRecordings(ctx, []string{v.View.RecordingID, "safe-non-uuid"})
	if err != nil || len(snapshots) != 1 || snapshots[0].Broadcast == nil || snapshots[0].Recording.ID != v.View.RecordingID {
		t.Fatal("batched current recording", snapshots, err)
	}
}
