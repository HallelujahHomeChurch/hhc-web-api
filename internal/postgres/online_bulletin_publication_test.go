package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestOnlinePublishCannotBypassReviewGate(t *testing.T) {
	for _, scenario := range []struct {
		name, code      string
		accept, publish bool
	}{
		{"clean", "", true, true},
		{"warning-unaccepted", "unknown_font_role", false, false},
		{"warning-reviewed", "unknown_font_role", true, true},
		{"unassigned-text", "unsupported_source_structure", true, false},
		{"future-error", "future_error", true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := context.Background(), time.Now().UTC()
			issues := []onlinebulletins.ReviewIssue{}
			if scenario.code != "" {
				issues = append(issues, onlinebulletins.ReviewIssue{Code: scenario.code, ComponentID: "body", Blocking: true})
			}
			repo, source := seedOnlineLayoutIssues(t, db, now, issues)
			t.Cleanup(func() {
				for _, query := range []string{`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, `DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, `DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`} {
					if _, err := db.Exec(query, source.DocumentID); err != nil {
						t.Error(err)
					}
				}
				if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
					t.Error(err)
				}
			})
			if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now); err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measuredLayout(t, *job), now); err != nil {
				t.Fatal(err)
			}
			state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil {
				t.Fatal(err)
			}
			accepted := []onlinebulletins.ReviewIssue{}
			if scenario.accept {
				accepted = state.Local.ReviewIssues
			}
			confirmed, err := repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: state.Version, Revision: state.Local.Revision, PageCount: len(state.Local.Document.Pages), LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, AcceptedIssues: accepted}, "admin", now)
			if (err == nil) != scenario.publish {
				t.Fatal("review gate", err)
			}
			version := state.Version
			if err == nil {
				version = confirmed.Version
			}
			if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,$2,1 FROM hhc_web.bulletin_version WHERE issue_id=$1`, source.Canonical.IssueID, now); err != nil {
				t.Fatal(err)
			}
			_, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: 1, OnlineVersion: version, Revision: state.Local.Revision}, "admin", now)
			if (err == nil) != scenario.publish {
				t.Fatal("publication bypassed review", err)
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, source.DocumentID).Scan(&count); err != nil || (count == 1) != scenario.publish {
				t.Fatal("publication state", count, err)
			}
		})
	}
}

func TestOnlinePublicationGatesAndPublishedCheckpoint(t *testing.T) {
	db := accessDB(t)
	ctx, now := bulletinAuditContext(), time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	repo = repo.WithAudit(NewAuditOutbox(db))
	t.Cleanup(func() {
		for _, query := range []string{
			`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`,
			`DELETE FROM hhc_web.online_bulletin_mapping WHERE document_id=$1`,
			`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`,
			`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`,
			`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1`,
		} {
			if _, err := db.Exec(query, source.DocumentID); err != nil {
				t.Error(err)
			}
		}
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
			t.Error(err)
		}
	})
	input := onlinebulletins.PublishInput{CanonicalVersion: 1, OnlineVersion: 2, Revision: 1}
	if _, err := repo.PublishOnlineBulletin(context.Background(), source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("audit missing", err)
	}
	if _, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("unmeasured published", err)
	}
	if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now); err != nil {
		t.Fatal(err)
	}
	measure := func() onlinebulletins.AdminState {
		job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
		if err != nil || job == nil {
			t.Fatal(err)
		}
		if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measuredLayout(t, *job), now); err != nil {
			t.Fatal(err)
		}
		state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	state := measure()
	input.OnlineVersion, input.Revision = state.Version, state.Local.Revision
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,$2,1 FROM hhc_web.bulletin_version WHERE issue_id=$1`, source.Canonical.IssueID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("unconfirmed published", err)
	}
	confirm := func(state onlinebulletins.AdminState) {
		result, err := repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: state.Version, Revision: state.Local.Revision, LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, PageCount: len(state.Local.Document.Pages), AcceptedIssues: state.Local.ReviewIssues}, postgresBulletinAuditActor, now)
		if err != nil {
			t.Fatal(err)
		}
		input.OnlineVersion, input.Revision = result.Version, result.Revision
	}
	confirm(state)
	for _, column := range []string{"status", "pdf_asset_id", "title"} {
		var old string
		if err := db.QueryRow(`SELECT `+column+` FROM hhc_web.bulletin_version WHERE issue_id=$1`, source.Canonical.IssueID).Scan(&old); err != nil {
			t.Fatal(err)
		}
		value := "changed"
		if column == "status" {
			value = "unpublished"
		}
		if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET `+column+`=$2 WHERE issue_id=$1`, source.Canonical.IssueID, value); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrConflict) {
			t.Fatalf("%s gate=%v", column, err)
		}
		if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET `+column+`=$2 WHERE issue_id=$1`, source.Canonical.IssueID, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`ALTER TABLE hhc_web.outbox_event ADD CONSTRAINT test_online_publish_audit_failure CHECK(payload_json->>'resourceId'<>'` + source.DocumentID + `' OR payload_json->>'action'<>'cms.bulletin.online.publish') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`ALTER TABLE hhc_web.outbox_event DROP CONSTRAINT IF EXISTS test_online_publish_audit_failure`); err != nil {
			t.Error(err)
		}
	})
	if _, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); err == nil {
		t.Fatal("failed audit committed publication")
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.PublishedRevision != nil || state.Version != input.OnlineVersion || state.PDFStatus != "published" {
		t.Fatal("partial publication transaction", err)
	}
	var publications int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, source.DocumentID).Scan(&publications); err != nil || publications != 0 {
		t.Fatal("orphan publication", err)
	}
	if _, err = db.Exec(`ALTER TABLE hhc_web.outbox_event DROP CONSTRAINT test_online_publish_audit_failure`); err != nil {
		t.Fatal(err)
	}
	result, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now)
	if err != nil || result.Version != input.OnlineVersion+1 {
		t.Fatal("publish", err)
	}
	first := result.Revision
	if _, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale publish", err)
	}
	for i := 0; i < 33; i++ {
		state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
		if err != nil {
			t.Fatal(err)
		}
		components := state.Local.Document.Components
		components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = fmt.Sprintf("修正%d。", i)
		if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: state.Version, Components: components}, postgresBulletinAuditActor, now); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_mapping WHERE document_id=$1`, source.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatal("draft checkpoints", err)
	}
	state = measure()
	confirm(state)
	result, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_mapping WHERE document_id=$1 AND from_revision=$2 AND to_revision=$3 AND status='unchanged'`, source.DocumentID, first, result.Revision).Scan(&count); err != nil || count != 13 {
		t.Fatal("incomplete direct checkpoint", count, err)
	}
	if err = db.QueryRow(`SELECT count(DISTINCT (from_revision,to_revision)) FROM hhc_web.online_bulletin_mapping WHERE document_id=$1`, source.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatal("draft mapping chain", err)
	}
	input.OnlineVersion = result.Version
	unpublished, err := repo.UnpublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input.CanonicalVersion, input.OnlineVersion, postgresBulletinAuditActor, now)
	if err != nil || unpublished.Version != result.Version+1 {
		t.Fatal("unpublish", err)
	}
	var pdf string
	var published sql.NullInt64
	if err = db.QueryRow(`SELECT v.status,d.published_revision FROM hhc_web.bulletin_version v JOIN hhc_web.online_bulletin_document d ON d.issue_id=v.issue_id AND d.series=v.series AND d.content_locale=v.locale WHERE d.id=$1`, source.DocumentID).Scan(&pdf, &published); err != nil || pdf != "published" || published.Valid {
		t.Fatal("independent states", err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND destination<>'audit-log'`, source.Canonical.IssueID).Scan(&count); err != nil || count != 0 {
		t.Fatal("weekly notification changed", err)
	}
	input.OnlineVersion = unpublished.Version
	republished, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now)
	if err != nil || republished.Revision != result.Revision {
		t.Fatal("republish lost immutable revision", err)
	}
	input.OnlineVersion = republished.Version
	repeated, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now)
	if err != nil || repeated.Version != republished.Version {
		t.Fatal("duplicate publish mutated", err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, source.DocumentID).Scan(&count); err != nil || count != 2 {
		t.Fatal("republish overwrote history", count, err)
	}
}
