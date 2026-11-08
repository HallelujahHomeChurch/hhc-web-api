package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	postgresBulletinAuditActor   = "018f47d2-e5d1-7f3f-8f18-6c8e32621f71"
	postgresBulletinAuditRequest = "request-bulletin-owner-1"
)

func TestBulletinMutationsCommitOneCanonicalAuditEvent(t *testing.T) {
	db := openBulletinAuditDB(t)
	now := time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC)
	ctx := bulletinAuditContext()
	tests := []struct {
		name, action string
		mutate       func(*testing.T, *Repository) string
	}{
		{"create", "cms.bulletin.create", func(t *testing.T, r *Repository) string {
			issue, err := r.CreateIssue(ctx, 1901, "2026-09-20", postgresBulletinAuditActor, "audit-create", now)
			if err != nil {
				t.Fatal(err)
			}
			return issue.ID
		}},
		{"issue update", "cms.bulletin.update", func(t *testing.T, r *Repository) string {
			issue := seedBulletinAuditIssue(t, db, now, false)
			if _, err := r.UpdateIssue(ctx, issue.ID, issue.Version, bulletins.UpdateIssueInput{IssueNumber: 1902, IssueDate: "2026-09-27"}, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return issue.ID
		}},
		{"version attach", "cms.bulletin.version.update", func(t *testing.T, r *Repository) string {
			issue := seedBulletinAuditIssue(t, db, now, false)
			updated, err := r.PutVersion(ctx, issue.ID, issue.Version, bulletinVersionInput(), postgresBulletinAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			return updated.Versions[0].ID
		}},
		{"version update", "cms.bulletin.version.update", func(t *testing.T, r *Repository) string {
			issue := seedBulletinAuditIssue(t, db, now, true)
			if _, err := r.UpdateVersion(ctx, issue.ID, "general", "en", issue.Version, "Updated", "", postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return issue.Versions[0].ID
		}},
		{"version delete", "cms.bulletin.version.delete", func(t *testing.T, r *Repository) string {
			issue := seedBulletinAuditIssue(t, db, now, true)
			if _, err := r.DeleteVersion(ctx, issue.ID, "general", "en", issue.Version, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return issue.Versions[0].ID
		}},
		{"publish", "cms.bulletin.publish", func(t *testing.T, r *Repository) string {
			issue := seedBulletinAuditIssue(t, db, now, true)
			if _, err := r.StartPublish(ctx, issue.ID, "general", "en", issue.Version, true, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return issue.ID
		}},
		{"notification submit and replay", "cms.bulletin.notification.submit", func(t *testing.T, r *Repository) string {
			issue := seedBulletinAuditIssue(t, db, now, true)
			preparePublishedBulletinAuditIssue(t, db, issue.ID)
			key := "10000000-0000-4000-8000-000000000001"
			first, err := r.SubmitBulletinNotification(ctx, issue.ID, "general", postgresBulletinAuditActor, key, now)
			if err != nil {
				t.Fatal(err)
			}
			replay, err := r.SubmitBulletinNotification(ctx, issue.ID, "general", postgresBulletinAuditActor, key, now)
			if err != nil || replay != first {
				t.Fatalf("replay=%#v first=%#v err=%v", replay, first, err)
			}
			return issue.ID
		}},
		{"unpublish", "cms.bulletin.unpublish", func(t *testing.T, r *Repository) string {
			issue := seedBulletinAuditIssue(t, db, now, true)
			preparePublishedBulletinAuditIssue(t, db, issue.ID)
			if _, err := r.Unpublish(ctx, issue.ID, "general", "en", issue.Version, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return issue.ID
		}},
		{"restore", "cms.bulletin.restore", func(t *testing.T, r *Repository) string {
			issue := seedBulletinAuditIssue(t, db, now, true)
			if _, err := r.RestoreIssueRevision(ctx, issue.ID, issue.Version, issue.Version, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return issue.ID
		}},
		{"delete", "cms.bulletin.delete", func(t *testing.T, r *Repository) string {
			issue := seedBulletinAuditIssue(t, db, now, false)
			if err := r.DeleteIssue(ctx, issue.ID, issue.Version, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			return issue.ID
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearBulletinAuditDB(t, db)
			repository := New(db).WithAudit(NewAuditOutbox(db))
			resourceID := test.mutate(t, repository)
			events := readBulletinAuditEvents(t, db)
			if len(events) != 1 || events[0].Action != test.action || events[0].ResourceID != resourceID || events[0].ActorID != postgresBulletinAuditActor || events[0].RequestID != postgresBulletinAuditRequest || len(events[0].Metadata) != 0 {
				t.Fatalf("events=%#v want action=%q resource=%q", events, test.action, resourceID)
			}
		})
	}
}

func TestBulletinMutationAuditFailureRollsBackBusinessState(t *testing.T) {
	db := openBulletinAuditDB(t)
	clearBulletinAuditDB(t, db)
	now := time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC)
	issue := seedBulletinAuditIssue(t, db, now, false)
	if _, err := db.Exec(`CREATE OR REPLACE FUNCTION hhc_web.reject_bulletin_audit_test() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.destination='audit-log' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; DROP TRIGGER IF EXISTS reject_bulletin_audit_test ON hhc_web.outbox_event; CREATE TRIGGER reject_bulletin_audit_test BEFORE INSERT ON hhc_web.outbox_event FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_bulletin_audit_test()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TRIGGER IF EXISTS reject_bulletin_audit_test ON hhc_web.outbox_event; DROP FUNCTION IF EXISTS hhc_web.reject_bulletin_audit_test()`)
	})
	repository := New(db).WithAudit(NewAuditOutbox(db))
	_, err := repository.UpdateIssue(bulletinAuditContext(), issue.ID, issue.Version, bulletins.UpdateIssueInput{IssueNumber: 2000, IssueDate: "2026-10-04"}, postgresBulletinAuditActor, now)
	if !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatalf("err=%v", err)
	}
	var number, version int64
	var date string
	if err := db.QueryRow(`SELECT issue_number,issue_date::text,version FROM hhc_web.bulletin_issue WHERE id=$1`, issue.ID).Scan(&number, &date, &version); err != nil {
		t.Fatal(err)
	}
	if number != int64(*issue.IssueNumber) || date != issue.IssueDate || version != issue.Version {
		t.Fatalf("business state committed: number=%d date=%s version=%d", number, date, version)
	}
}

func TestBulletinMutationRequiresTrustedAuditProvenance(t *testing.T) {
	db := openBulletinAuditDB(t)
	clearBulletinAuditDB(t, db)
	repository := New(db).WithAudit(NewAuditOutbox(db))
	_, err := repository.CreateIssue(context.Background(), 1901, "2026-09-20", postgresBulletinAuditActor, "missing-provenance", time.Now().UTC())
	if !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatalf("err=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.bulletin_issue`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("issues=%d err=%v", count, err)
	}
}

func openBulletinAuditDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := migrations.Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func bulletinAuditContext() context.Context {
	return auditclient.WithProvenance(context.Background(), postgresBulletinAuditActor, postgresBulletinAuditRequest)
}

func clearBulletinAuditDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`TRUNCATE hhc_web.cms_audit_event,hhc_web.outbox_event,hhc_web.publication_workflow,hhc_web.public_projection,hhc_web.bulletin_revision,hhc_web.bulletin_version,hhc_web.bulletin_issue CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func seedBulletinAuditIssue(t *testing.T, db *sql.DB, now time.Time, withVersion bool) bulletins.Issue {
	t.Helper()
	repository := New(db)
	issue, err := repository.CreateIssue(context.Background(), 1900, "2026-09-20", postgresBulletinAuditActor, "seed-bulletin", now)
	if err != nil {
		t.Fatal(err)
	}
	if withVersion {
		issue, err = repository.PutVersion(context.Background(), issue.ID, issue.Version, bulletinVersionInput(), postgresBulletinAuditActor, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	return issue
}

func bulletinVersionInput() bulletins.PutVersionInput {
	return bulletins.PutVersionInput{Locale: "en", Title: "Weekly", PDFAssetID: "asset-en", PDFFileName: "weekly.pdf"}
}

func preparePublishedBulletinAuditIssue(t *testing.T, db *sql.DB, issueID string) {
	t.Helper()
	for _, statement := range []string{
		`UPDATE hhc_web.bulletin_issue SET status='published' WHERE id=$1`,
		`UPDATE hhc_web.bulletin_version SET status='published',public_grant_id='grant-1' WHERE issue_id=$1`,
	} {
		if _, err := db.Exec(statement, issueID); err != nil {
			t.Fatal(err)
		}
	}
}

func readBulletinAuditEvents(t *testing.T, db *sql.DB) []auditclient.Event {
	t.Helper()
	rows, err := db.Query(`SELECT payload_json FROM hhc_web.outbox_event WHERE destination='audit-log' ORDER BY created_at,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var events []auditclient.Event
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatal(err)
		}
		event, err := auditclient.Decode(payload)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}
