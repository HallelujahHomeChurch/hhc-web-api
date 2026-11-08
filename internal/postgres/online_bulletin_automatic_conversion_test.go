package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestSimplifiedWaitsForTraditionalPublication(t *testing.T) {
	for _, scenario := range []string{"clean", "review", "changed-pdf", "incoming", "legacy"} {
		t.Run(scenario, func(t *testing.T) {
			db, now := accessDB(t), time.Now().UTC()
			issues := []onlinebulletins.ReviewIssue{}
			if scenario == "review" {
				issues = append(issues, onlinebulletins.ReviewIssue{Code: "unknown_font_role", ComponentID: "body", Blocking: true})
			}
			repo, source := seedOnlineLayoutIssues(t, db, now, issues)
			repo = repo.WithAudit(NewAuditOutbox(db))
			t.Cleanup(func() {
				for _, query := range []string{
					`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId' IN (SELECT id::text FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
					`DELETE FROM hhc_web.online_bulletin_conversion_job WHERE issue_id=$1`,
					`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
				} {
					if _, err := db.Exec(query, source.Canonical.IssueID); err != nil {
						t.Error(err)
					}
				}
			})
			if scenario != "legacy" {
				if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET authorizer_id=$2,authorizer_request_id=$3 WHERE id=$1`, source.ID, postgresBulletinAuditActor, postgresBulletinAuditRequest); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now); err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			if scenario == "changed-pdf" {
				if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_asset_id='new-pdf' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "incoming" {
				if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET incoming_revision=local_revision WHERE id=$1`, source.DocumentID); err != nil {
					t.Fatal(err)
				}
			}

			result := measuredLayout(t, *job)
			err = repo.CompleteOnlineBulletinLayout(ctx, *job, result, now)
			if err != nil {
				t.Fatal(err)
			}
			countJobs := func(want int) {
				t.Helper()
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_conversion_job WHERE issue_id=$1`, source.Canonical.IssueID).Scan(&count); err != nil || count != want {
					t.Fatalf("jobs=%d want=%d err=%v", count, want, err)
				}
			}
			countJobs(0)
			if scenario == "review" {
				state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if err != nil {
					t.Fatal(err)
				}
				_, err = repo.ConfirmOnlineBulletin(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision, LayoutValidationHash: result.LayoutValidationHash, PageCount: 4, AcceptedIssues: issues}, postgresBulletinAuditActor, now)
				if err != nil {
					t.Fatal(err)
				}
				countJobs(0)
			}
			if scenario == "clean" {
				if err = repo.CompleteOnlineBulletinLayout(ctx, *job, result, now); err != nil {
					t.Fatal(err)
				}
				countJobs(0)
				var published int
				if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND published_revision IS NOT NULL`, source.Canonical.IssueID).Scan(&published); err != nil || published != 0 {
					t.Fatal("automatic publication", published, err)
				}
			}

		})
	}
}
