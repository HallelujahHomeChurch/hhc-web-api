package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func seedAutomaticUpdate(t *testing.T, db *sql.DB, now time.Time) (*Repository, onlinebulletins.ExtractionJob) {
	t.Helper()
	id := seedOnlineExtraction(t, db, now)
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version=$2,auto_publish=true,authorizer_id=$3,authorizer_request_id='autopilot-test',template_snapshot_json=$4 WHERE id=$1`, id, onlinebulletins.RecognitionExtractorVersion(), postgresBulletinAuditActor, updateTestJSON(t, onlinebulletins.ConversionTemplateSettings())); err != nil {
		t.Fatal(err)
	}
	repo := New(db).WithAudit(NewAuditOutbox(db)).WithLLMBulletinExtraction(true)
	job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, onlinebulletins.RecognitionJobTimeout, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	candidate := extractionCandidate(*job)
	candidate.Document.TemplateSnapshot = job.TemplateSnapshot
	candidate.Document.LayoutManifest.RendererVersion, candidate.Document.LayoutManifest.RendererArtifactSha256 = "v11", onlinebulletins.RendererV11SHA256
	candidate.Evidence.Pages = []onlinebulletins.GeometryPage{{Number: 1, Texts: []onlinebulletins.GeometryText{{Text: "987654期 2099-12-30"}}}}
	candidate.Recognition = &onlinebulletins.RecognitionEvidence{ProtocolSHA256: onlinebulletins.RecognitionProtocolSHA256(), Deployment: onlinebulletins.RecognitionDeployment, ModelVersion: onlinebulletins.RecognitionModelVersion, SourceSHA256: job.SourceAssetChecksum}
	for p := 1; p <= 4; p++ {
		candidate.Recognition.Pages = append(candidate.Recognition.Pages, onlinebulletins.RecognitionPageEvidence{Page: p, InputSHA256: strings.Repeat("b", 64), SemanticSHA256: strings.Repeat("c", 64), Usage: onlinebulletins.RecognitionUsage{Model: onlinebulletins.RecognitionModelName, Requests: 2}})
	}
	if err = repo.CompleteOnlineBulletinExtraction(context.Background(), *job, candidate, []onlinebulletins.ReviewIssue{{Code: "layout_validation_required", Blocking: true}}, now); err != nil {
		t.Fatal("complete extraction", err)
	}
	return repo, *job
}

func updateTestJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func verifiedUpdateForTest(t *testing.T, input onlinebulletins.UpdateInput) onlinebulletins.UpdateEvidence {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plan := onlinebulletins.UpdatePlan{Readiness: "ready", ChangeKind: "layout", Summary: "核對完成", Choices: []onlinebulletins.UpdateChoice{}, Mappings: []onlinebulletins.SemanticMappingDecision{}}
		json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": onlinebulletins.RecognitionModelName, "usage": map[string]any{"input_tokens": 100, "output_tokens": 100}, "output": []any{map[string]any{"type": "message", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": string(updateTestJSON(t, plan))}}}}})
	}))
	t.Cleanup(server.Close)
	client, err := onlinebulletins.NewAzureExtractor(server.URL, "test", "test", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestAutomaticUpdateAdoptsVerifiedSourceAndQueuesFreshLayoutExactlyOnce(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source := seedAutomaticUpdate(t, db, now)
	job, err := repo.ClaimOnlineBulletinUpdate(ctx, now, 4*time.Minute, 2)
	if err != nil || job == nil || job.ExtractionJobID != source.ID {
		t.Fatal("claim update", err)
	}
	if layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3); err != nil || layout != nil {
		t.Fatal("layout spent before source acceptance", err)
	}
	evidence := verifiedUpdateForTest(t, job.Input)
	for _, attempt := range evidence.Attempts {
		if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, attempt, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = repo.CompleteOnlineBulletinUpdate(ctx, *job, evidence, now); err != nil {
		t.Fatal("complete", err)
	}
	if err = repo.CompleteOnlineBulletinUpdate(ctx, *job, evidence, now); err != nil {
		t.Fatal("idempotent", err)
	}
	layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || layout == nil || layout.Revision <= job.SourceRevision || layout.Document.SourceAssetChecksum != source.SourceAssetChecksum {
		t.Fatal("fresh layout missing", err)
	}
	var incoming sql.NullInt64
	var local, base, count int64
	if err = db.QueryRow(`SELECT d.local_revision,d.base_revision,d.incoming_revision,(SELECT count(*) FROM hhc_web.online_bulletin_layout_job WHERE document_id=d.id) FROM hhc_web.online_bulletin_document d WHERE d.id=$1`, source.DocumentID).Scan(&local, &base, &incoming, &count); err != nil || incoming.Valid || base != job.SourceRevision || local != layout.Revision || count != 1 {
		t.Fatal("adoption state", local, base, incoming, count, err)
	}
}

func TestAutomaticUpdateLeaseAndSourceFencesRejectStaleDecision(t *testing.T) {
	for _, change := range []string{"asset", "metadata", "local", "cancelled", "lease", "forged", "missing-checkpoint", "snapshot"} {
		t.Run(change, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := context.Background(), time.Now().UTC()
			repo, source := seedAutomaticUpdate(t, db, now)
			job, err := repo.ClaimOnlineBulletinUpdate(ctx, now, 4*time.Minute, 2)
			if err != nil || job == nil {
				t.Fatal(err)
			}
			evidence := verifiedUpdateForTest(t, job.Input)
			if change != "missing-checkpoint" {
				for _, a := range evidence.Attempts {
					if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *job, a, now); err != nil {
						t.Fatal(err)
					}
				}
			}
			switch change {
			case "asset":
				_, err = db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_asset_id='new-identical-bytes' WHERE issue_id=$1`, source.Canonical.IssueID)
			case "metadata":
				_, err = db.Exec(`UPDATE hhc_web.bulletin_issue SET version=version+1 WHERE id=$1`, source.Canonical.IssueID)
			case "local":
				_, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET version=version+1 WHERE id=$1`, source.DocumentID)
			case "cancelled":
				_, err = db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET auto_publish=false WHERE id=$1`, source.ID)
			case "lease":
				now = now.Add(5 * time.Minute)
			case "forged":
				evidence.Plan.Summary = "forged"
			case "snapshot":
				job.SourceRevision++
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.CompleteOnlineBulletinUpdate(ctx, *job, evidence, now); err == nil {
				t.Fatal("stale/unverified completion accepted", change)
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1`, source.DocumentID).Scan(&count); err != nil || count != 0 {
				t.Fatal("unsafe layout queued", count, err)
			}
		})
	}
}

func TestAutomaticUpdatePersistsAndResumesIndependentEvidenceWithinAttemptBudget(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, _ := seedAutomaticUpdate(t, db, now)
	first, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 2)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	evidence := verifiedUpdateForTest(t, first.Input)
	if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *first, evidence.Attempts[0], now); err != nil {
		t.Fatal(err)
	}
	second, err := repo.ClaimOnlineBulletinUpdate(ctx, now.Add(61*time.Second), time.Minute, 2)
	if err != nil || second == nil || second.LeaseToken == first.LeaseToken || len(second.SavedAttempts) != 1 {
		t.Fatal("lost independent checkpoint", err)
	}
	if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *first, evidence.Attempts[1], now.Add(62*time.Second)); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatal("ABA lease accepted", err)
	}
	if err = repo.FailOnlineBulletinUpdate(ctx, *second, "provider_failed", now.Add(62*time.Second), 2); err != nil {
		t.Fatal(err)
	}
	if third, err := repo.ClaimOnlineBulletinUpdate(ctx, now.Add(5*time.Minute), time.Minute, 2); err != nil || third != nil {
		t.Fatal("unbounded retry", err)
	}
}

func TestAutomaticUpdateAdoptsExistingIncomingWithoutManualComparison(t *testing.T) {
	db := accessDB(t)
	ctx, now := context.Background(), time.Now().UTC()
	repo, source := seedAutomaticUpdate(t, db, now)
	first, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 2)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	evidence := verifiedUpdateForTest(t, first.Input)
	for _, a := range evidence.Attempts {
		if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *first, a, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = repo.CompleteOnlineBulletinUpdate(ctx, *first, evidence, now); err != nil {
		t.Fatal(err)
	}
	layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || layout == nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_asset_id='new-same-bytes' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET superseded=true WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	id := platform.NewID()
	if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_extraction_job(id,document_id,issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version,available_at,auto_publish,authorizer_id,authorizer_request_id,template_snapshot_json) SELECT $2,document_id,issue_id,series,content_locale,'new-same-bytes',source_asset_checksum,template_version,extractor_version,$3,true,authorizer_id,authorizer_request_id,template_snapshot_json FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, source.ID, id, now); err != nil {
		t.Fatal(err)
	}
	second, err := repo.ClaimOnlineBulletinExtraction(ctx, now, onlinebulletins.RecognitionJobTimeout, 3)
	if err != nil || second == nil || second.ID != id {
		t.Fatal(err)
	}
	candidate := extractionCandidate(*second)
	candidate.Document.TemplateSnapshot = second.TemplateSnapshot
	candidate.Document.LayoutManifest.RendererVersion, candidate.Document.LayoutManifest.RendererArtifactSha256 = "v11", onlinebulletins.RendererV11SHA256
	candidate.Recognition = &onlinebulletins.RecognitionEvidence{ProtocolSHA256: onlinebulletins.RecognitionProtocolSHA256(), Deployment: onlinebulletins.RecognitionDeployment, ModelVersion: onlinebulletins.RecognitionModelVersion, SourceSHA256: second.SourceAssetChecksum}
	for p := 1; p <= 4; p++ {
		candidate.Recognition.Pages = append(candidate.Recognition.Pages, onlinebulletins.RecognitionPageEvidence{Page: p, InputSHA256: strings.Repeat("b", 64), SemanticSHA256: strings.Repeat("c", 64), Usage: onlinebulletins.RecognitionUsage{Model: onlinebulletins.RecognitionModelName, Requests: 2}})
	}
	candidate.Evidence.Pages = first.Input.Evidence.Pages
	if err = repo.CompleteOnlineBulletinExtraction(ctx, *second, candidate, []onlinebulletins.ReviewIssue{{Code: "layout_validation_required", Blocking: true}}, now); err != nil {
		t.Fatal(err)
	}
	update, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 2)
	if err != nil || update == nil || update.Input.Local == nil || update.Input.Base == nil || update.ExtractionJobID != id {
		t.Fatal("incoming not automatically planned", err)
	}
	evidence = verifiedUpdateForTest(t, update.Input)
	for _, a := range evidence.Attempts {
		if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *update, a, now); err != nil {
			t.Fatal(err)
		}
	}
	if err = repo.CompleteOnlineBulletinUpdate(ctx, *update, evidence, now); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.IncomingRevision != nil || state.Local == nil || state.BaseRevision == nil || *state.BaseRevision != update.SourceRevision || state.Local.Revision <= update.SourceRevision {
		t.Fatal("manual comparison remained", err)
	}
}
