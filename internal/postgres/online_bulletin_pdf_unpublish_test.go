package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

func TestPDFUnpublishKeepsOnlineUnlessExplicitAndCommitsTogether(t *testing.T) {
	for _, scenario := range []string{"default", "both", "stale-online", "audit-failure"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := bulletinAuditContext(), time.Now().UTC()
			repo, source := seedOnlineLayout(t, db, now)
			repo = repo.WithAudit(NewAuditOutbox(db))
			metadata, _ := json.Marshal(source.Canonical.Metadata)
			if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_publication(document_id,revision,canonical_metadata,created_by) VALUES($1,1,$2,'test')`, source.DocumentID, metadata); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=1,draft_status='published' WHERE id=$1`, source.DocumentID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,$2,1 FROM hhc_web.bulletin_version WHERE issue_id=$1`, source.Canonical.IssueID, now); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, query := range []string{
					`DELETE FROM hhc_web.outbox_event WHERE aggregate_id=$1::uuid OR payload_json->>'resourceId'=$1::text OR payload_json->>'resourceId'=$2::text`,
					`DELETE FROM hhc_web.publication_workflow WHERE resource_id=$1 AND $2::text<>''`,
					`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1 AND $2::text<>''`,
					`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE issue_id=$1 AND id=$2`,
					`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$2 AND $1::text<>''`,
				} {
					if _, err := db.Exec(query, source.Canonical.IssueID, source.DocumentID); err != nil {
						t.Error(err)
					}
				}
			})
			if scenario == "audit-failure" {
				if _, err := db.Exec(`ALTER TABLE hhc_web.outbox_event ADD CONSTRAINT test_online_unpublish_audit_failure CHECK(payload_json->>'resourceId'<>'` + source.DocumentID + `' OR payload_json->>'action'<>'cms.bulletin.online.unpublish') NOT VALID`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if _, err := db.Exec(`ALTER TABLE hhc_web.outbox_event DROP CONSTRAINT test_online_unpublish_audit_failure`); err != nil {
						t.Error(err)
					}
				})
			}
			var err error
			if scenario == "default" {
				_, err = repo.Unpublish(ctx, source.Canonical.IssueID, "general", "zh-Hant", 1, postgresBulletinAuditActor, now)
			} else {
				expected := int64(2)
				if scenario == "stale-online" {
					expected = 1
				}
				_, err = repo.UnpublishWithOnline(ctx, source.Canonical.IssueID, "general", "zh-Hant", 1, expected, postgresBulletinAuditActor, now)
			}
			failed := scenario == "stale-online" || scenario == "audit-failure"
			if failed != (err != nil) {
				t.Fatal("unexpected mutation result", err)
			}
			if scenario == "stale-online" && !errors.Is(err, bulletins.ErrPrecondition) {
				t.Fatal("missing CAS conflict", err)
			}
			var onlineVersion, canonicalVersion int64
			var published sql.NullInt64
			var pdfCount int
			if err = db.QueryRow(`SELECT d.version,d.published_revision,i.version,(SELECT count(*) FROM hhc_web.bulletin_published_version WHERE issue_id=i.id) FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id WHERE d.id=$1`, source.DocumentID).Scan(&onlineVersion, &published, &canonicalVersion, &pdfCount); err != nil {
				t.Fatal(err)
			}
			if failed {
				if onlineVersion != 2 || !published.Valid || canonicalVersion != 1 || pdfCount != 1 {
					t.Fatal("partial failed transaction")
				}
			} else if canonicalVersion != 2 || pdfCount != 0 || published.Valid != (scenario == "default") || (scenario == "both" && onlineVersion != 3) || (scenario == "default" && onlineVersion != 2) {
				t.Fatal("independent unpublish state", onlineVersion, published, canonicalVersion, pdfCount)
			}
		})
	}
}
