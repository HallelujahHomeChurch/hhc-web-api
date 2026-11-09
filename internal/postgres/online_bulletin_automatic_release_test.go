package postgres

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"strings"
	"testing"
	"time"
)

func TestRecognitionWithoutExplicitIntentNeverAutomaticallyPublishes(t *testing.T) {
	for _, scenario := range []string{"clean", "edited-local", "incoming", "metadata", "source", "unpublished", "unverified", "semantic-receipt", "issue", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			ctx := context.Background()
			id := seedOnlineExtraction(t, db, now)
			snapshot := onlinebulletins.ConversionTemplateSettings()
			if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='failed' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			var issue string
			if err := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, id).Scan(&issue); err != nil {
				t.Fatal(err)
			}
			repo := New(db).WithAudit(NewAuditOutbox(db)).WithLLMBulletinExtraction(true)
			if _, err := repo.QueueOnlineBulletinExtraction(bulletinAuditContext(), issue, onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 1, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64), Retry: true}, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, onlinebulletins.RecognitionJobTimeout, 3)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				for _, q := range []string{
					`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE issue_id=$1`,
					`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
					`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
					`DELETE FROM hhc_web.outbox_event WHERE aggregate_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1) OR payload_json->>'resourceId' IN (SELECT id::text FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
					`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`,
				} {
					if _, err := db.Exec(q, job.Canonical.IssueID); err != nil {
						t.Error(err)
					}
				}
			})
			candidate := extractionCandidate(*job)
			candidate.Document.TemplateSnapshot = &snapshot
			candidate.Document.LayoutManifest.RendererVersion = "v11"
			candidate.Document.LayoutManifest.RendererArtifactSha256 = onlinebulletins.RendererV11SHA256
			candidate.Recognition = &onlinebulletins.RecognitionEvidence{ProtocolSHA256: onlinebulletins.RecognitionProtocolSHA256(), Deployment: onlinebulletins.RecognitionDeployment, ModelVersion: onlinebulletins.RecognitionModelVersion, SourceSHA256: job.SourceAssetChecksum}
			for i := 1; i <= 4; i++ {
				candidate.Recognition.Pages = append(candidate.Recognition.Pages, onlinebulletins.RecognitionPageEvidence{Page: i, InputSHA256: strings.Repeat("b", 64), SemanticSHA256: strings.Repeat("d", 64), Usage: onlinebulletins.RecognitionUsage{Model: onlinebulletins.RecognitionDeployment, Requests: 2, InputTokens: 10, OutputTokens: 10}})
			}
			if scenario == "semantic-receipt" {
				candidate.Recognition.Pages[0].SemanticSHA256 = ""
			}
			if scenario == "unverified" {
				candidate.Recognition = nil
			}
			if scenario == "unverified" || scenario == "semantic-receipt" {
				if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now); err == nil {
					t.Fatal("unverified receipt accepted")
				}
				return
			}
			issues := []onlinebulletins.ReviewIssue{{Code: "layout_validation_required", Blocking: true}}
			if scenario == "issue" {
				issues = append(issues, onlinebulletins.ReviewIssue{Code: "unknown_body_kind", Blocking: true})
			}
			if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, issues, now); err != nil {
				t.Fatal("complete", err)
			}
			layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
			if err != nil || layout == nil {
				t.Fatal("claim layout", err)
			}
			if err := repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
				t.Fatal("layout", err)
			}
			for _, q := range []string{`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, `INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,now(),1 FROM hhc_web.bulletin_version WHERE issue_id=$1`} {
				if _, err := db.Exec(q, job.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			var mutation string
			switch scenario {
			case "edited-local":
				state, err := repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = repo.SaveOnlineBulletinDraft(bulletinAuditContext(), job.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: state.Version, Components: state.Local.Document.Components}, postgresBulletinAuditActor, now); err != nil {
					t.Fatal(err)
				}
				edited, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
				if err != nil || edited == nil {
					t.Fatal(err)
				}
				if err = repo.CompleteOnlineBulletinLayout(ctx, *edited, measuredLayout(t, *edited), now); err != nil {
					t.Fatal(err)
				}
			case "incoming":
				mutation = `UPDATE hhc_web.online_bulletin_document SET incoming_revision=base_revision WHERE id=$1`
			case "metadata":
				mutation = `UPDATE hhc_web.bulletin_version SET title='edited' WHERE issue_id=(SELECT issue_id FROM hhc_web.online_bulletin_document WHERE id=$1)`
			case "source":
				mutation = `UPDATE hhc_web.bulletin_version SET pdf_asset_id='replacement' WHERE issue_id=(SELECT issue_id FROM hhc_web.online_bulletin_document WHERE id=$1)`
			case "unpublished":
				mutation = `UPDATE hhc_web.bulletin_version SET status='draft' WHERE issue_id=(SELECT issue_id FROM hhc_web.online_bulletin_document WHERE id=$1)`
			case "unverified":
				mutation = `UPDATE hhc_web.online_bulletin_extraction_job SET snapshot_json=jsonb_set(snapshot_json,'{candidate,recognition}', 'null') WHERE document_id=$1`
			case "disabled":
				repo.WithLLMBulletinExtraction(false)
			}
			if mutation != "" {
				if _, err := db.Exec(mutation, job.DocumentID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := repo.NextOnlineBulletinWork(ctx); err != nil {
				t.Fatal("wake", err)
			}
			published, err := repo.AutoPublishNextRecognition(ctx, now)
			if err != nil || published {
				t.Fatal("draft/legacy source acquired automatic publication authority", scenario, published, err)
			}
		})
	}
}
