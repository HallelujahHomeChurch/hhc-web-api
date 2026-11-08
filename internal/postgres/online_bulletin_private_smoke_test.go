package postgres

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

// Opt-in acceptance against a private PDF and the pinned extractor runtime.
// No source text, images or rendered report is written to logs or the repository.
func TestPrivateBulletinConversionPersistence(t *testing.T) {
	path, root := os.Getenv("HHC_PRIVATE_PDF_SOURCE"), os.Getenv("HHC_TEST_LAYOUT_RUNNER_ROOT")
	if path == "" || root == "" {
		t.Skip("private PDF and pinned renderer runtime required")
	}
	var metadata onlinebulletins.CanonicalMetadata
	if json.Unmarshal([]byte(os.Getenv("HHC_PRIVATE_CANONICAL_METADATA")), &metadata) != nil {
		t.Fatal("canonical metadata required")
	}
	source, err := onlinebulletins.ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := os.Getenv("HHC_TEST_LAYOUT_RENDERER_DIGEST")
	assets, err := onlinebulletins.LoadBundledTemplateAssets(filepath.Join(root, "packages/ui/src/bulletin-reader/v7/template-assets.json"), "983f2545fd63ad9afe04c9cde5c3456c76934d2aa08a81050595e8f909f6120f")
	if err != nil {
		t.Fatal(err)
	}
	runner := onlinebulletins.LayoutRunner{NodePath: "/usr/local/bin/node", ScriptPath: filepath.Join(root, "scripts/measure-bulletin-layout-v10.mjs"), AssetsDirectory: filepath.Join(root, "assets"), RendererArtifactSHA256: digest}
	for _, failFixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "ready", true: "fixed-layout-failure"}[failFixed], func(t *testing.T) {
			db := accessDB(t)
			ctx := bulletinAuditContext()
			now := time.Now().UTC()
			previous := seedOnlineExtraction(t, db, now)
			var issueID string
			if err := db.QueryRow(`DELETE FROM hhc_web.online_bulletin_extraction_job WHERE id=$1 RETURNING issue_id::text`, previous).Scan(&issueID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_issue SET issue_number=$2,issue_date=$3::date WHERE id=$1`, issueID, metadata.IssueNumber, metadata.Date); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET title=$2,subtitle=$3,pdf_sha256=$4 WHERE issue_id=$1`, issueID, metadata.Title, metadata.Subtitle, source.SourceSHA256); err != nil {
				t.Fatal(err)
			}
			repo := New(db).WithAudit(NewAuditOutbox(db))
			if _, err := repo.QueueOnlineBulletinExtraction(ctx, issueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 1, SourceAssetID: "test-asset", SourceChecksum: source.SourceSHA256}, postgresBulletinAuditActor, now); err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 2*time.Minute, 3)
			if err != nil || job == nil {
				t.Fatal("claim", err)
			}
			manifest := onlinebulletins.LayoutManifest{TemplateVersion: "v1", RendererVersion: "v10", RendererArtifactSha256: digest, Assets: assets}
			candidate, issues, err := onlinebulletins.ApplyTemplateV10(source, job.Canonical, manifest, *job.TemplateSnapshot)
			if err != nil || candidate.Document == nil {
				t.Fatal("candidate", err)
			}
			for _, issue := range issues {
				if issue.Code != "source_metadata_confirmation" {
					t.Fatalf("unexpected issue %s", issue.Code)
				}
			}
			if failFixed {
				for i := range candidate.Document.LayoutManifest.Pages[0].FixedSlots {
					slot := &candidate.Document.LayoutManifest.Pages[0].FixedSlots[i]
					if slot.Element == "visionMission" {
						slot.Style.FontSize = 8
					}
				}
			}
			result, composeErr := runner.Compose(ctx, *candidate.Document, metadata)
			if failFixed {
				var diagnostic *onlinebulletins.LayoutReviewError
				if !errors.As(composeErr, &diagnostic) || len(diagnostic.Issues) == 0 || diagnostic.Issues[0].FixedElement != "visionMission" {
					t.Fatal("missing fixed failure diagnostic", composeErr)
				}
				issues = append(issues, diagnostic.Issues...)
			} else {
				if composeErr != nil {
					t.Fatal("compose", composeErr)
				}
				if _, err := onlinebulletins.VerifyComposedLayout(result, *candidate.Document, metadata); err != nil {
					t.Fatal("composition proof", err)
				}
				candidate.Document = &result.Document
			}
			if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, issues, time.Now().UTC()); err != nil {
				t.Fatal("persist candidate", err)
			}
			state, err := repo.OnlineBulletinState(ctx, issueID, "general", "zh-Hant")
			if err != nil || state.Local == nil || state.Extraction.ResultRevision == nil {
				t.Fatal("candidate readback", err)
			}
			if failFixed {
				if onlinebulletins.ValidateReview(state.Local.ReviewIssues, state.Local.ReviewIssues) == nil {
					t.Fatal("fixed failure could be waived")
				}
				t.Logf("failed candidate persisted: pages=%d components=%d", len(state.Local.Document.Pages), len(state.Local.Document.Components))
				return
			}
			layout, err := repo.ClaimOnlineBulletinLayout(ctx, time.Now().UTC(), 2*time.Minute, 3)
			if err != nil || layout == nil {
				t.Fatal("layout claim", err)
			}
			measurement, err := runner.Validate(ctx, layout.Document, layout.Canonical)
			if err != nil {
				t.Fatal("saved layout validation", err)
			}
			if err := repo.CompleteOnlineBulletinLayout(ctx, *layout, measurement, time.Now().UTC()); err != nil {
				t.Fatal("persist proof", err)
			}
			state, err = repo.OnlineBulletinState(ctx, issueID, "general", "zh-Hant")
			if err != nil || state.Layout.Status != "ready" || state.Local.Document.LayoutManifest.LayoutValidationHash == "" {
				t.Fatal("proof readback", err)
			}
			if err := onlinebulletins.ValidateReview(state.Local.ReviewIssues, state.Local.ReviewIssues); err != nil {
				t.Fatal("review is not actionable", err)
			}
			t.Logf("ready for review: pages=%d components=%d issues=%d", len(state.Local.Document.Pages), len(state.Local.Document.Components), len(state.Local.ReviewIssues))
		})
	}
}
