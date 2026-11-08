package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestRecognitionCheckpointIsImmutableAndLeaseFenced(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	id := seedOnlineExtraction(t, db, now)
	repo := New(db)
	job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
	if err != nil || job == nil || job.ID != id {
		t.Fatal(err)
	}
	checkpoint := onlinebulletins.RecognitionCheckpoint{Canonical: job.Canonical.Metadata, Page: 1, Phase: "verified", InputSHA256: strings.Repeat("b", 64), ProtocolSHA256: strings.Repeat("c", 64), Deployment: "hhc-bulletin-ai", ModelVersion: "2026-10-26", Result: onlinebulletins.RecognitionResult{SemanticSHA256: strings.Repeat("d", 64), Page: 1, Blocks: []onlinebulletins.RecognitionBlock{}, UnassignedIDs: []string{}}, Usage: onlinebulletins.RecognitionUsage{Model: "hhc-bulletin-ai", InputTokens: 100, OutputTokens: 20}}
	if err := repo.SaveRecognitionCheckpoint(context.Background(), *job, checkpoint, now); err != nil {
		t.Fatal(err)
	}
	saved, err := repo.LoadRecognitionCheckpoint(context.Background(), *job, checkpoint, now)
	if err != nil || saved == nil || saved.Result.Page != 1 || saved.Usage.InputTokens != 100 {
		t.Fatal("checkpoint not resumable", saved, err)
	}
	if err := repo.SaveRecognitionCheckpoint(context.Background(), *job, checkpoint, now); err != nil {
		t.Fatal("identical retry is not idempotent", err)
	}
	changed := checkpoint
	changed.Result.Page = 2
	if err := repo.SaveRecognitionCheckpoint(context.Background(), *job, changed, now); err == nil {
		t.Fatal("checkpoint overwritten")
	}
	changed = checkpoint
	changed.InputSHA256 = strings.Repeat("d", 64)
	if _, err := repo.LoadRecognitionCheckpoint(context.Background(), *job, changed, now); err == nil {
		t.Fatal("checkpoint reused for different input")
	}
	if err := repo.SaveRecognitionCheckpoint(context.Background(), *job, checkpoint, now.Add(2*time.Minute)); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatal("expired lease saved checkpoint", err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_asset_id='replacement' WHERE issue_id=$1`, job.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LoadRecognitionCheckpoint(context.Background(), *job, checkpoint, now); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatal("replaced source resumed inference", err)
	}
}

func TestRecognitionLeaseBudgetDoesNotExtendLegacyJobs(t *testing.T) {
	for _, modern := range []bool{false, true} {
		t.Run(fmt.Sprint(modern), func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			id := seedOnlineExtraction(t, db, now)
			if modern {
				if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version=$2 WHERE id=$1`, id, onlinebulletins.RecognitionExtractorVersion()); err != nil {
					t.Fatal(err)
				}
			}
			job, err := New(db).ClaimOnlineBulletinExtraction(context.Background(), now, onlinebulletins.RecognitionJobTimeout, 3)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			expected := 120 * time.Second
			if modern {
				expected = onlinebulletins.RecognitionJobTimeout
			}
			if delta := job.LeaseUntil.Sub(now); delta < expected-time.Millisecond || delta > expected+time.Millisecond {
				t.Fatal("wrong scoped lease", modern, delta)
			}
		})
	}
}

func TestRecognitionProducerCreatesNewFrozenJobFromLegacyFailure(t *testing.T) {
	db := accessDB(t)
	ctx := bulletinAuditContext()
	now := time.Now().UTC()
	old := seedOnlineExtraction(t, db, now)
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='failed' WHERE id=$1`, old); err != nil {
		t.Fatal(err)
	}
	var issue, document string
	if err := db.QueryRow(`SELECT issue_id::text,document_id::text FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, old).Scan(&issue, &document); err != nil {
		t.Fatal(err)
	}
	repo := New(db).WithLLMBulletinExtraction(true)
	queued, err := repo.QueueOnlineBulletinExtraction(ctx, issue, onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 1, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64), Retry: true}, postgresBulletinAuditActor, now)
	if err != nil || queued.Job.ID == old {
		t.Fatal("legacy failure did not become a fresh recognition job", err)
	}
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, onlinebulletins.RecognitionJobTimeout, 3)
	if err != nil || job == nil || job.ExtractorVersion != onlinebulletins.RecognitionExtractorVersion() || job.TemplateSnapshot == nil || *job.TemplateSnapshot != onlinebulletins.ConversionTemplateSettings() || len(job.LearningRules) != 0 {
		t.Fatal("new job did not freeze current recognition input", job, err)
	}
	var authorizer string
	if err := db.QueryRow(`SELECT authorizer_id::text FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, job.ID).Scan(&authorizer); err != nil || authorizer != postgresBulletinAuditActor {
		t.Fatal("durable publication authorizer lost", err)
	}
}
