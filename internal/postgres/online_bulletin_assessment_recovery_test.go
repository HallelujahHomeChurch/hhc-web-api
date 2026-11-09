package postgres

import (
	"context"
	"database/sql"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func seedPublishedAssessmentSource(t *testing.T) (*sql.DB, *Repository, onlinebulletins.ExtractionJob, time.Time) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedSimplifiedSource(t, db, now)
	t.Cleanup(func() {
		for _, q := range []string{`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE issue_id=$1`, `DELETE FROM hhc_web.online_bulletin_mapping WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`, `DELETE FROM hhc_web.online_bulletin_publication WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`, `DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`} {
			if _, e := db.Exec(q, source.Canonical.IssueID); e != nil {
				t.Error(e)
			}
		}
	})
	if _, e := db.Exec(`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, source.Canonical.IssueID); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,now(),1 FROM hhc_web.bulletin_version WHERE issue_id=$1`, source.Canonical.IssueID); e != nil {
		t.Fatal(e)
	}
	return db, repo, source, now
}
func publishAssessmentSource(t *testing.T, db *sql.DB, repo *Repository, source onlinebulletins.ExtractionJob, now time.Time) string {
	repo.WithAudit(NewAuditOutbox(db)).WithLLMBulletinExtraction(true)
	state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = repo.PublishOnlineBulletin(bulletinAuditContext(), source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision}, postgresBulletinAuditActor, now); e != nil {
		t.Fatal(e)
	}
	var id string
	if e = db.QueryRow(`SELECT id::text FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type=$2`, source.DocumentID, simplifiedReleaseEvent).Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}
func derivedAssessmentDocument(job *onlinebulletins.ConversionJob) onlinebulletins.Document {
	d := job.Source
	d.ContentLocale = "zh-Hans"
	d.TemplateVersion = "v2"
	d.LayoutManifest.TemplateVersion = "v2"
	d.LayoutManifest.RendererVersion = "v2"
	return d
}
func TestAutomaticReleaseReassessesCompletedManualConversion(t *testing.T) {
	db, repo, source, now := seedPublishedAssessmentSource(t)
	ctx := context.Background()
	queued, e := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now)
	if e != nil {
		t.Fatal(e)
	}
	job, e := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if e != nil || job == nil {
		t.Fatal(e)
	}
	if e = repo.CompleteOnlineBulletinConversion(ctx, *job, derivedAssessmentDocument(job), job.SourceMetadata, now); e != nil {
		t.Fatal(e)
	}
	layout, e := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if e != nil || layout == nil {
		t.Fatal(e)
	}
	if e = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); e != nil {
		t.Fatal(e)
	}
	id := publishAssessmentSource(t, db, repo, source, now)
	event := claimSimplifiedRelease(t, db, id, now)
	if e = repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now); e != nil {
		t.Fatal(e)
	}
	event = claimSimplifiedRelease(t, db, id, now.Add(11*time.Second))
	if e = repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now.Add(11*time.Second)); e != nil {
		t.Fatal(e)
	}
	var n, proofs int
	if e = db.QueryRow(`SELECT (SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id=$1),(SELECT jsonb_array_length(assessment_json) FROM hhc_web.online_bulletin_conversion_job WHERE id=$2)`, queued.DocumentID, job.ID).Scan(&n, &proofs); e != nil {
		t.Fatal(e)
	}
	t.Logf("new llm-enabled automatic release publications=%d assessment passes=%d", n, proofs)
	if n > 0 && proofs < 2 {
		t.Fatal("automatic derived release published completed manual conversion without semantic assessment")
	}
	now = now.Add(12 * time.Second)
	fresh, e := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if e != nil || fresh == nil || fresh.PublicationEventID == "" {
		t.Fatal("legacy conversion was not queued for semantic assessment", e)
	}
	draft := derivedAssessmentDocument(fresh)
	input, e := onlinebulletins.SimplifiedAssessmentInput(fresh.Source, fresh.SourceMetadata, draft, fresh.SourceMetadata)
	if e != nil {
		t.Fatal(e)
	}
	if !fresh.InitializesMetadata {
		input.LocalMetadata = &fresh.TargetMetadata
	}
	evidence := verifiedUpdateForTest(t, input)
	for _, attempt := range evidence.Attempts {
		if e = repo.SaveOnlineBulletinConversionAssessment(ctx, *fresh, input, attempt, now); e != nil {
			t.Fatal(e)
		}
	}
	if e = repo.CompleteOnlineBulletinConversion(ctx, *fresh, draft, fresh.SourceMetadata, now); e != nil {
		t.Fatal(e)
	}
	measured, e := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if e != nil || measured == nil {
		t.Fatal(e)
	}
	if e = repo.CompleteOnlineBulletinLayout(ctx, *measured, measuredLayout(t, *measured), now); e != nil {
		t.Fatal(e)
	}
	event = claimSimplifiedRelease(t, db, id, now.Add(11*time.Second))
	if e = repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now.Add(11*time.Second)); e != nil {
		t.Fatal(e)
	}
	if e = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, queued.DocumentID).Scan(&n); e != nil || n != 1 {
		t.Fatal("assessed legacy replacement did not publish", n, e)
	}

}
func TestDerivedRetryRecoversIncompatibleAssessmentReceipt(t *testing.T) {
	for _, scenario := range []string{"protocol", "metadata", "compatible"} {
		t.Run(scenario, func(t *testing.T) {
			db, repo, source, now := seedPublishedAssessmentSource(t)
			ctx := context.Background()
			id := publishAssessmentSource(t, db, repo, source, now)
			event := claimSimplifiedRelease(t, db, id, now)
			if e := repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now); e != nil {
				t.Fatal(e)
			}
			job, e := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
			if e != nil || job == nil {
				t.Fatal(e)
			}
			input, e := onlinebulletins.SimplifiedAssessmentInput(job.Source, job.SourceMetadata, derivedAssessmentDocument(job), job.SourceMetadata)
			if e != nil {
				t.Fatal(e)
			}
			proof := verifiedUpdateForTest(t, input)
			old := proof.Attempts[:1]
			if scenario == "protocol" {
				old[0].ProtocolSHA256 = strings.Repeat("a", 64)
			}
			if _, e = db.Exec(`UPDATE hhc_web.online_bulletin_conversion_job SET assessment_json=$2 WHERE id=$1`, job.ID, updateTestJSON(t, old)); e != nil {
				t.Fatal(e)
			}
			if e = repo.FailOnlineBulletinConversion(ctx, *job, "simplified_conversion_failed", nil, now, 1); e != nil {
				t.Fatal(e)
			}
			event = claimSimplifiedRelease(t, db, id, now.Add(time.Second))
			if e = repo.CompleteSimplifiedRelease(bulletinAuditContext(), event, now.Add(time.Second)); e != nil {
				t.Fatal(e)
			}
			if scenario == "metadata" {
				if _, e = db.Exec(`UPDATE hhc_web.bulletin_version SET title='authored Hans metadata' WHERE issue_id=$1 AND series='general' AND locale='zh-Hans'`, source.Canonical.IssueID); e != nil {
					t.Fatal(e)
				}
			}
			hant, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if e != nil {
				t.Fatal(e)
			}
			hans, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
			if e != nil {
				t.Fatal(e)
			}
			queued, e := repo.QueueOnlineBulletinConversion(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: hans.Version, Retry: true}, postgresBulletinAuditActor, now.Add(2*time.Second))
			if e != nil {
				t.Fatal(e)
			}
			retry, e := repo.ClaimOnlineBulletinConversion(ctx, now.Add(2*time.Second), time.Minute, 3)
			if e != nil || retry == nil {
				t.Fatal(e)
			}
			if scenario == "compatible" && len(retry.AssessmentAttempts) != 1 {
				t.Fatal("compatible successful pass was discarded")
			}
			if scenario != "compatible" && len(retry.AssessmentAttempts) != 0 {
				t.Fatal("incompatible assessment context survived explicit retry")
			}
			calls := 0
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "test provider", 503) }))
			defer s.Close()
			client, e := onlinebulletins.NewAzureExtractor(s.URL, "test", "test", "", s.Client())
			if e != nil {
				t.Fatal(e)
			}
			_, e = client.ResumeVerifiedUpdate(ctx, input, successfulAssessmentAttempts(retry.AssessmentAttempts), nil)
			t.Logf("retried same row=%v saved passes=%d provider calls=%d err=%v", queued.Job.ID == job.ID, len(retry.AssessmentAttempts), calls, e)
			if calls == 0 {
				t.Fatal("explicit retry is poisoned by old protocol receipt")
			}
		})
	}
}
