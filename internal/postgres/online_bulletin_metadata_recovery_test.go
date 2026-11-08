package postgres

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestExtractionReplayUsesCurrentCanonicalMetadata(t *testing.T) {
	for _, field := range []string{"unchanged", "title", "subtitle", "date", "issueNumber"} {
		t.Run(field, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			ctx := context.Background()
			repo := New(db)
			id := seedOnlineExtraction(t, db, now)
			if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version=$2 WHERE id=$1`, id, onlinebulletins.ExtractorVersion); err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 3)
			if err != nil || job == nil {
				t.Fatal("claim", err)
			}
			if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, extractionCandidate(*job), []onlinebulletins.ReviewIssue{{Code: "canonical_source_mismatch", ComponentID: "cover", Blocking: true}}, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			before, err := repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
			if err != nil {
				t.Fatal(err)
			}
			var snapshot string
			if err = db.QueryRow(`SELECT snapshot_json::text FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, id).Scan(&snapshot); err != nil {
				t.Fatal(err)
			}
			// A publication/status-only issue version change must remain idempotent.
			if _, err = db.Exec(`UPDATE hhc_web.bulletin_issue SET version=version+1 WHERE id=$1`, job.Canonical.IssueID); err != nil {
				t.Fatal(err)
			}
			statements := map[string]string{
				"title":       `UPDATE hhc_web.bulletin_version SET title='corrected title' WHERE issue_id=$1`,
				"subtitle":    `UPDATE hhc_web.bulletin_version SET subtitle='corrected subtitle' WHERE issue_id=$1`,
				"date":        `UPDATE hhc_web.bulletin_issue SET issue_date='2099-12-29' WHERE id=$1`,
				"issueNumber": `UPDATE hhc_web.bulletin_issue SET issue_number=987653 WHERE id=$1`,
			}
			if statement := statements[field]; statement != "" {
				if _, err = db.Exec(statement, job.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 2, OnlineVersion: before.Version, SourceAssetID: job.SourceAssetID, SourceChecksum: job.SourceAssetChecksum}
			queued, err := repo.QueueOnlineBulletinExtraction(ctx, job.Canonical.IssueID, input, "admin", now.Add(2*time.Second))
			if err != nil {
				t.Fatal(err)
			}
			if field == "unchanged" {
				if queued.Job.ID != id || queued.Version != before.Version {
					t.Fatal("unchanged metadata created a new extraction", queued)
				}
				return
			}
			if queued.Job.ID == id || queued.Job.Status != "queued" || queued.Job.ResultRevision != nil || queued.Version != before.Version+1 {
				t.Fatal("metadata correction reused stale evidence", queued)
			}
			var stored string
			var superseded bool
			if err = db.QueryRow(`SELECT snapshot_json::text,superseded FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, id).Scan(&stored, &superseded); err != nil || stored != snapshot || !superseded {
				t.Fatal("original evidence was modified or remains current", err)
			}
			after, err := repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
			if err != nil || !reflect.DeepEqual(before.Local, after.Local) || !reflect.DeepEqual(before.BaseRevision, after.BaseRevision) || !reflect.DeepEqual(before.IncomingRevision, after.IncomingRevision) || !reflect.DeepEqual(before.PublishedRevision, after.PublishedRevision) {
				t.Fatal("queue changed saved content pointers", err)
			}
			input.OnlineVersion = queued.Version
			replayed, err := repo.QueueOnlineBulletinExtraction(ctx, job.Canonical.IssueID, input, "admin", now.Add(3*time.Second))
			if err != nil || replayed.Job.ID != queued.Job.ID || replayed.Version != queued.Version {
				t.Fatal("new active job was duplicated", err)
			}
			claimed, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(4*time.Second), 120*time.Second, 3)
			if err != nil || claimed == nil || claimed.ID != queued.Job.ID || claimed.Canonical.Metadata != after.CanonicalMetadata {
				t.Fatal("new worker did not receive corrected metadata", err)
			}
		})
	}
}

func TestMetadataCorrectionDoesNotCancelActiveExtraction(t *testing.T) {
	for _, status := range []string{"queued", "parsing"} {
		t.Run(status, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			ctx := context.Background()
			repo := New(db)
			id := seedOnlineExtraction(t, db, now)
			var issueID string
			if err := db.QueryRow(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version=$2 WHERE id=$1 RETURNING issue_id::text`, id, onlinebulletins.ExtractorVersion).Scan(&issueID); err != nil {
				t.Fatal(err)
			}
			if status == "parsing" {
				if _, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 3); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_issue SET version=2 WHERE id=$1`, issueID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET subtitle='corrected' WHERE issue_id=$1`, issueID); err != nil {
				t.Fatal(err)
			}
			queued, err := repo.QueueOnlineBulletinExtraction(ctx, issueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: 2, OnlineVersion: 1, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64)}, "admin", now)
			if err != nil || queued.Job.ID != id || queued.Job.Status != status || queued.Version != 1 {
				t.Fatal("active extraction was replaced", queued, err)
			}
		})
	}
}
