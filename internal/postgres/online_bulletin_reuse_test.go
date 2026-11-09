package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestAutomaticReprocessReusesVerifiedSourceUnderFreshAuthority(t *testing.T) {
	for _, scenario := range []string{"same-asset", "same-bytes-new-asset", "metadata-change", "template-change"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := context.Background(), time.Now().UTC()
			repo, source := seedAutomaticUpdate(t, db, now)
			repo.WithAutomaticBulletinExtraction(true)
			var originalHash string
			if err := db.QueryRow(`SELECT snapshot_sha256 FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, source.ID).Scan(&originalHash); err != nil {
				t.Fatal(err)
			}
			asset := "test-asset"
			if scenario == "same-bytes-new-asset" {
				asset = "new-identical-source"
				if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_asset_id=$2 WHERE issue_id=$1`, source.Canonical.IssueID, asset); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "metadata-change" {
				if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET title='corrected canonical title' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil {
				t.Fatal(err)
			}
			queued, err := repo.QueueOnlineBulletinExtraction(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, SourceAssetID: asset, SourceChecksum: source.SourceAssetChecksum, AutoPublish: true}, postgresBulletinAuditActor, now)
			if err != nil || queued.Job.ID == source.ID {
				t.Fatal("reprocess retained old job authority", err)
			}
			job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, onlinebulletins.RecognitionJobTimeout, 3)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			if scenario == "template-change" {
				job.TemplateSnapshot.Version++
				if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET template_snapshot_json=$2 WHERE id=$1`, job.ID, updateTestJSON(t, job.TemplateSnapshot)); err != nil {
					t.Fatal(err)
				}
			}
			candidate, issues, err := repo.LoadReusableRecognition(ctx, *job, now)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "metadata-change" || scenario == "template-change" {
				if candidate != nil {
					t.Fatal("reused incompatible/unverified evidence")
				}
				return
			}
			if candidate == nil || !onlinebulletins.ValidRecognitionEvidence(candidate.Recognition, *candidate.Document) {
				t.Fatal("exact verified source was paid for again")
			}
			if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, *candidate, issues, now); err != nil {
				t.Fatal("fresh source completion", err)
			}
			var retainedHash, reusedFrom string
			if err = db.QueryRow(`SELECT old.snapshot_sha256,fresh.recognition_reused_from::text FROM hhc_web.online_bulletin_extraction_job fresh JOIN hhc_web.online_bulletin_extraction_job old ON old.id=$2 WHERE fresh.id=$1`, job.ID, source.ID).Scan(&retainedHash, &reusedFrom); err != nil || retainedHash != originalHash || reusedFrom != source.ID {
				t.Fatal("reuse damaged immutable provenance", err)
			}
			update, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 3)
			if err != nil || update == nil || update.ExtractionJobID != job.ID {
				t.Fatal("new asset did not own semantic update", err)
			}
		})
	}
}

func TestAutomaticUpdateProviderRetryKeepsSuccessfulPassAndUsesDelayedWake(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC().Truncate(time.Microsecond)
	repo, _ := seedAutomaticUpdate(t, db, now)
	job, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 2)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	evidence := verifiedUpdateForTest(t, job.Input)
	if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, evidence.Attempts[0], now); err != nil {
		t.Fatal(err)
	}
	failed := evidence.Attempts[1]
	failed.ErrorCode = "update_plan_failed"
	failed.Usage.Model = ""
	if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, failed, now); err != nil {
		t.Fatal(err)
	}
	if err = repo.FailOnlineBulletinUpdate(ctx, *job, "provider_unavailable", now, 2); err != nil {
		t.Fatal(err)
	}
	if early, err := repo.ClaimOnlineBulletinUpdate(ctx, now.Add(59*time.Second), time.Minute, 2); err != nil || early != nil {
		t.Fatal("retry did not yield event execution", err)
	}
	next, err := repo.NextOnlineBulletinWork(ctx)
	if err != nil || next.Before(now.Add(time.Minute)) {
		t.Fatal("delayed retry lost its wake", next, err)
	}
	retry, err := repo.ClaimOnlineBulletinUpdate(ctx, now.Add(61*time.Second), time.Minute, 2)
	if err != nil || retry == nil || len(retry.SavedAttempts) != 1 {
		t.Fatal("failed attempt discarded successful independent evidence", err)
	}
	if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *retry, evidence.Attempts[1], now.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinUpdate(ctx, *retry, evidence, now.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestAutomaticRecognitionRetriesOnlyWithinSourceFailureBudget(t *testing.T) {
	for _, scenario := range []string{"automatic-provider", "manual-provider", "unsupported-source"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := context.Background(), time.Now().UTC()
			id := seedOnlineExtraction(t, db, now)
			if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version=$2,auto_publish=$3 WHERE id=$1`, id, onlinebulletins.RecognitionExtractorVersion(), scenario != "manual-provider"); err != nil {
				t.Fatal(err)
			}
			repo := New(db).WithLLMBulletinExtraction(true)
			for n := 1; n <= 3; n++ {
				job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, onlinebulletins.RecognitionJobTimeout, 10)
				if err != nil || job == nil {
					t.Fatal("claim", n, err)
				}
				code := "recognition_failed"
				if scenario == "unsupported-source" {
					code = "unsupported_source"
				}
				if err = repo.FailOnlineBulletinExtraction(ctx, *job, code, now, 10); err != nil {
					t.Fatal(err)
				}
				var status string
				if err = db.QueryRow(`SELECT status FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, id).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if scenario != "automatic-provider" || n == 3 {
					if status != "failed" {
						t.Fatal("unbounded or unsafe source retry", status)
					}
					break
				}
				if status != "queued" {
					t.Fatal("transient automatic recognition required manual intervention", status)
				}
				if early, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(59*time.Second), onlinebulletins.RecognitionJobTimeout, 10); err != nil || early != nil {
					t.Fatal("retry did not yield event execution", err)
				}
				now = now.Add(61 * time.Second)
			}
			if extra, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(time.Minute), onlinebulletins.RecognitionJobTimeout, 10); err != nil || extra != nil {
				t.Fatal("source retry budget exceeded", err)
			}
		})
	}
}
