package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestOnlineConfirmationIsBoundToMeasuredRevisionAndCanonicalMetadata(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	ctx := context.Background()
	input := onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: 2, Revision: 1, PageCount: 4, LayoutValidationHash: strings.Repeat("a", 64), AcceptedIssues: []onlinebulletins.ReviewIssue{}}
	if _, err := repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("unmeasured revision confirmed", err)
	}
	if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	measured := measuredLayout(t, *job)
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measured, now); err != nil {
		t.Fatal(err)
	}
	input.OnlineVersion, input.Revision, input.PageCount, input.LayoutValidationHash = 4, 2, len(measured.Document.Pages), measured.LayoutValidationHash
	repo = repo.WithAudit(NewAuditOutbox(db))
	if _, err = repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("unaudited confirmation", err)
	}
	ctx = bulletinAuditContext()
	for _, change := range []func(*onlinebulletins.ConfirmInput){
		func(i *onlinebulletins.ConfirmInput) { i.CanonicalVersion++ },
		func(i *onlinebulletins.ConfirmInput) { i.Revision-- },
		func(i *onlinebulletins.ConfirmInput) { i.PageCount++ },
		func(i *onlinebulletins.ConfirmInput) { i.LayoutValidationHash = strings.Repeat("b", 64) },
	} {
		bad := input
		change(&bad)
		if _, err = repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", bad, postgresBulletinAuditActor, now); err == nil {
			t.Fatal("stale or unreviewed proof accepted")
		}
	}
	result, err := repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now)
	if err != nil || result.Version != 5 || result.Revision != 2 {
		t.Fatal("confirm", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`, source.DocumentID)
		_, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1`, source.DocumentID)
	})
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1 AND payload_json->>'action'='cms.bulletin.online.review'`, source.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatal("confirmation missing audit", err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_confirmation SET created_by='changed' WHERE document_id=$1`, source.DocumentID); err == nil {
		t.Fatal("confirmation evidence mutable")
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || !state.Confirmed || state.PublishedRevision != nil || state.PDFStatus != "draft" {
		t.Fatal("confirmation published content or missing state", err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET title='changed' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Confirmed {
		t.Fatal("metadata edit retained confirmation", err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET title=$2 WHERE issue_id=$1`, source.Canonical.IssueID, source.Canonical.Metadata.Title); err != nil {
		t.Fatal(err)
	}
	components := state.Local.Document.Components
	components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "再次修正。"
	if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: 5, Components: components}, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Confirmed || state.Local.Revision != 3 || state.Version != 6 {
		t.Fatal("new Local reused old confirmation", err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`, source.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatal("historical confirmation lost", err)
	}
}
