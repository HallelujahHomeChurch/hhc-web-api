package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestOnlineRestoreCreatesNewLocalWithoutChangingHistory(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	ctx := context.Background()
	if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_publication(document_id,revision,canonical_metadata,created_by) VALUES($1,1,'{"title":"published title","subtitle":"subtitle","issueNumber":987654,"date":"2099-12-30"}','test')`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=1 WHERE id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, source.DocumentID)
		_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, source.DocumentID)
	})
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	components := state.Local.Document.Components
	components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "新版文字。"
	saved, err := repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: 2, Components: components}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	repo = repo.WithAudit(NewAuditOutbox(db))
	input := onlinebulletins.RestoreInput{CanonicalVersion: 1, OnlineVersion: saved.Version, Revision: 1}
	if _, err = repo.RestoreOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("unaudited restore", err)
	}
	ctx = bulletinAuditContext()
	bad := input
	bad.Revision = 999
	if _, err = repo.RestoreOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", bad, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatal("nonexistent revision", err)
	}
	restored, err := repo.RestoreOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now)
	if err != nil || restored.Revision != 3 || restored.Version != 4 || restored.LayoutJobID == saved.LayoutJobID {
		t.Fatal("restore", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1`, source.DocumentID)
	})
	if _, err = repo.RestoreOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale restore", err)
	}
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local.Revision != 3 || *state.BaseRevision != 1 || state.PublishedRevision == nil || *state.PublishedRevision != 1 || state.PublishedMetadata.Title != "published title" || state.Confirmed || state.Local.Document.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "文" {
		t.Fatal("incorrect restored Local", err)
	}
	var text, status string
	if err = db.QueryRow(`SELECT document_json#>>'{components,1,bodySection,blocks,0,sentences,0,spans,0,text}' FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=2`, source.DocumentID).Scan(&text); err != nil || text != "新版文字。" {
		t.Fatal("rewrote historical correction", err)
	}
	if err = db.QueryRow(`SELECT status FROM hhc_web.online_bulletin_layout_job WHERE id=$1`, saved.LayoutJobID).Scan(&status); err != nil || status != "superseded" {
		t.Fatal("old layout lease not fenced", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1 AND payload_json->>'action'='cms.bulletin.online.restore'`, source.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing restore audit", err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND destination<>'audit-log'`, source.Canonical.IssueID).Scan(&count); err != nil || count != 0 {
		t.Fatal("restore triggered PDF or notification workflow", err)
	}
}
