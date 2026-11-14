package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type productionReader struct{ repo *Repository }

func (reader productionReader) LoadOnlineBulletinProduction(ctx context.Context, issue, series, locale string) (*onlinebulletins.AdminProduction, error) {
	state, err := reader.repo.OnlineBulletinState(ctx, issue, series, locale)
	return state.Production, err
}

func progressFixture(t *testing.T) (*Repository, onlinebulletins.ExtractionJob, onlinebulletins.RecognitionProgressStore, productionReader) {
	t.Helper()
	db := accessDB(t)
	now := time.Now().UTC()
	id := seedOnlineExtraction(t, db, now)
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version=$2 WHERE id=$1`, id, onlinebulletins.RecognitionExtractorVersion()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET template_snapshot_json=$2 WHERE id=$1`, id, updateTestJSON(t, onlinebulletins.ConversionTemplateSettings())); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	job, err := repo.ClaimOnlineBulletinExtraction(t.Context(), now, onlinebulletins.RecognitionJobTimeout, 10)
	if err != nil || job == nil || job.ID != id {
		t.Fatal("claim", job, err)
	}
	store, ok := any(repo).(onlinebulletins.RecognitionProgressStore)
	if !ok {
		t.Fatal("repository does not persist validated recognition progress")
	}
	reader := productionReader{repo}
	return repo, *job, store, reader
}

func progressPage(t *testing.T, job onlinebulletins.ExtractionJob, page int, previous *onlinebulletins.RecognitionContinuation) (onlinebulletins.RecognitionPage, onlinebulletins.RecognitionResult) {
	t.Helper()
	var pixels bytes.Buffer
	if err := png.Encode(&pixels, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("p%d-t1", page)
	box := onlinebulletins.Box{X: .1, Y: .1, Width: .1, Height: .1}
	input := onlinebulletins.RecognitionPage{SourceSHA256: job.SourceAssetChecksum, Page: page, TotalPages: 4, Width: 612, Height: 792, PNG: pixels.Bytes(), Native: []onlinebulletins.RecognitionText{{ID: id, Text: "X", Box: box}}, Canonical: &job.Canonical.Metadata, Continuation: previous}
	// One fixed source label has a hand-derived omitted scalar inventory.
	raw, _ := json.Marshal([]any{input.SourceSHA256, page, previous, nil, map[string]string{id + ":0": "omitted"}, nil})
	sum := sha256.Sum256(raw)
	result := onlinebulletins.RecognitionResult{Page: page, Blocks: []onlinebulletins.RecognitionBlock{{Mode: "native", Role: "fixed_label", Group: "label", Text: "X", SourceIDs: []string{id}, Box: box}}, UnassignedIDs: []string{}, SemanticSHA256: hex.EncodeToString(sum[:])}
	return input, result
}

func persistProgressCheckpoint(t *testing.T, repo *Repository, job onlinebulletins.ExtractionJob, input onlinebulletins.RecognitionPage, result onlinebulletins.RecognitionResult) {
	t.Helper()
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	checkpoint := onlinebulletins.RecognitionCheckpoint{Canonical: job.Canonical.Metadata, Page: input.Page, Phase: "verified", InputSHA256: hex.EncodeToString(sum[:]), ProtocolSHA256: onlinebulletins.RecognitionProtocolSHA256(), Deployment: onlinebulletins.RecognitionDeployment, ModelVersion: onlinebulletins.RecognitionModelVersion, Result: result, TotalUsage: onlinebulletins.RecognitionUsage{Model: onlinebulletins.RecognitionDeployment, Requests: 2}}
	if err := repo.SaveRecognitionCheckpoint(t.Context(), job, checkpoint, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func TestProductionProgressCountsOnlyBoundContiguousProof(t *testing.T) {
	repo, job, store, reader := progressFixture(t)
	read := func() *onlinebulletins.AdminProduction {
		p, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
		if err != nil || p == nil {
			t.Fatal("progress", p, err)
		}
		return p
	}
	initial := read()
	if initial.TotalPages != nil || initial.VerifiedPages != 0 || initial.Phase != "scan" || initial.StartedAt == nil {
		t.Fatalf("untrusted initial progress: %+v", initial)
	}
	if err := store.BeginRecognitionProgress(t.Context(), job, 4, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	input, result := progressPage(t, job, 1, nil)
	persistProgressCheckpoint(t, repo, job, input, result)
	// A verified checkpoint alone has not passed the runner's materialization/continuation proof.
	if p := read(); p.VerifiedPages != 0 {
		t.Fatalf("raw checkpoint counted: %+v", p)
	}
	if err := store.StartRecognitionPage(t.Context(), job, input, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	bad := result
	bad.SemanticSHA256 = strings.Repeat("0", 64)
	if err := store.CompleteRecognitionPage(t.Context(), job, input, bad, false, time.Now().UTC()); err == nil {
		t.Fatal("bad semantic proof counted")
	}
	if err := store.CompleteRecognitionPage(t.Context(), job, input, result, false, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	first := read()
	if first.VerifiedPages != 1 || first.TotalPages == nil || *first.TotalPages != 4 || first.LastProgressAt == nil || first.ProgressVersion <= initial.ProgressVersion {
		t.Fatalf("verified progress: %+v", first)
	}
	previous, err := onlinebulletins.NextRecognitionContinuation(input, result)
	if err != nil {
		t.Fatal(err)
	}
	second, secondResult := progressPage(t, job, 2, previous)
	persistProgressCheckpoint(t, repo, job, second, secondResult)
	if err := store.CompleteRecognitionPage(t.Context(), job, second, secondResult, true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if p := read(); p.VerifiedPages != 2 || p.VerifiedReuseCount != 1 {
		t.Fatalf("reused prefix: %+v", p)
	}
	changed := second
	copyPrevious := *previous
	copyPrevious.PreviousSHA256 = strings.Repeat("f", 64)
	changed.Continuation = &copyPrevious
	if err := store.StartRecognitionPage(t.Context(), job, changed, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if p := read(); p.VerifiedPages != 1 || p.VerifiedReuseCount != 0 || p.ProgressVersion <= first.ProgressVersion {
		t.Fatalf("dependent suffix retained: %+v", p)
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_asset_id='replacement' WHERE issue_id=$1`, job.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginRecognitionProgress(t.Context(), job, 4, time.Now().UTC()); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatalf("replacement accepted stale progress: %v", err)
	}
	if p, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant"); err != nil || p != nil {
		t.Fatalf("replaced progress leaked: %+v %v", p, err)
	}
}

func TestProductionProgressVersionSurvivesStaffRetryAttemptABA(t *testing.T) {
	repo, job, _, reader := progressFixture(t)
	read := func() *onlinebulletins.AdminProduction {
		p, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
		if err != nil || p == nil {
			t.Fatal(p, err)
		}
		return p
	}
	first := read()
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='failed',lease_token=NULL,lease_until=NULL WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	failed := read()
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='queued',attempts=0,available_at=$2 WHERE id=$1`, job.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	queued := read()
	next, err := repo.ClaimOnlineBulletinExtraction(t.Context(), time.Now().UTC(), onlinebulletins.RecognitionJobTimeout, 10)
	if err != nil || next == nil || next.ID != job.ID {
		t.Fatal(next, err)
	}
	claimed := read()
	if first.ProgressVersion >= failed.ProgressVersion || failed.ProgressVersion >= queued.ProgressVersion || queued.ProgressVersion >= claimed.ProgressVersion || first.AttemptID == claimed.AttemptID || first.StartedAt == nil || claimed.StartedAt == nil || !first.StartedAt.Equal(*claimed.StartedAt) {
		t.Fatalf("retry version/attempt ABA: first=%+v failed=%+v queued=%+v claimed=%+v", first, failed, queued, claimed)
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status=status WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if p := read(); p.ProgressVersion <= claimed.ProgressVersion {
		t.Fatalf("same-row no-op did not advance snapshot order: %+v", p)
	}
}

func TestProductionProgressSurvivesCompletionProofCleanup(t *testing.T) {
	repo, job, store, reader := progressFixture(t)
	if err := store.BeginRecognitionProgress(t.Context(), job, 4, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	evidence := &onlinebulletins.RecognitionEvidence{ProtocolSHA256: onlinebulletins.RecognitionProtocolSHA256(), Deployment: onlinebulletins.RecognitionDeployment, ModelVersion: onlinebulletins.RecognitionModelVersion, SourceSHA256: job.SourceAssetChecksum}
	var previous *onlinebulletins.RecognitionContinuation
	for page := 1; page <= 4; page++ {
		input, result := progressPage(t, job, page, previous)
		if err := store.StartRecognitionPage(t.Context(), job, input, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		persistProgressCheckpoint(t, repo, job, input, result)
		if err := store.CompleteRecognitionPage(t.Context(), job, input, result, false, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		proof, err := onlinebulletins.VerifiedRecognitionProgress(input, result)
		if err != nil {
			t.Fatal(err)
		}
		evidence.Pages = append(evidence.Pages, onlinebulletins.RecognitionPageEvidence{Page: page, InputSHA256: proof.InputSHA256, SemanticSHA256: proof.SemanticSHA256, Usage: onlinebulletins.RecognitionUsage{Model: onlinebulletins.RecognitionDeployment, Requests: 2}})
		previous, err = onlinebulletins.NextRecognitionContinuation(input, result)
		if err != nil {
			t.Fatal(err)
		}
	}
	candidate := extractionCandidate(job)
	candidate.Document.TemplateSnapshot = job.TemplateSnapshot
	candidate.Document.LayoutManifest.RendererVersion = "v11"
	candidate.Document.LayoutManifest.RendererArtifactSha256 = onlinebulletins.RendererV11SHA256
	candidate.Recognition = evidence
	if err := repo.CompleteOnlineBulletinExtraction(t.Context(), job, candidate, []onlinebulletins.ReviewIssue{{Code: "layout_validation_required", Blocking: true}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := repo.db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_recognition_checkpoint WHERE job_id=$1 AND checkpoint_json ? 'result'`, job.ID).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("private checkpoint results retained: %d %v", retained, err)
	}
	p, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || p == nil || p.VerifiedPages != 4 || p.Phase != "reconciliation" || p.Status != "completed" || p.CurrentPage != nil {
		t.Fatalf("completed re-entry lost proof: %+v %v", p, err)
	}
	state, err := repo.OnlineBulletinState(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Production == nil || state.Production.VerifiedPages != 4 {
		t.Fatalf("Admin MVCC projection lost proof: %+v %v", state.Production, err)
	}
	raw, _ := json.Marshal(state.Production)
	for _, private := range []string{"sourceAsset", "lease", "checkpoint_json", "semanticSha256", "InputTokens", "test-asset"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("private progress field leaked: %s", private)
		}
	}
}

func TestProductionProgressVerifiedSnapshotReuse(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedAutomaticUpdate(t, db, now)
	repo.WithAutomaticBulletinExtraction(true)
	state, err := repo.OnlineBulletinState(t.Context(), source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.QueueOnlineBulletinExtraction(bulletinAuditContext(), source.Canonical.IssueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum, AutoPublish: true}, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinExtraction(t.Context(), now, onlinebulletins.RecognitionJobTimeout, 3)
	if err != nil || job == nil {
		t.Fatal(job, err)
	}
	if reused, _, err := repo.LoadReusableRecognition(t.Context(), *job, now); err != nil || reused == nil {
		t.Fatal("reuse", reused, err)
	}
	p, err := (productionReader{repo}).LoadOnlineBulletinProduction(t.Context(), source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || p == nil || p.TotalPages == nil || *p.TotalPages != 4 || p.VerifiedPages != 4 || p.VerifiedReuseCount != 4 || p.Subphase != "snapshot_reuse" {
		t.Fatalf("snapshot reuse not accounted: %+v %v", p, err)
	}
}

func TestProductionProgressRetryRevalidatesWithoutLosingProof(t *testing.T) {
	repo, job, store, reader := progressFixture(t)
	if err := store.BeginRecognitionProgress(t.Context(), job, 4, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	input, result := progressPage(t, job, 1, nil)
	persistProgressCheckpoint(t, repo, job, input, result)
	if err := store.CompleteRecognitionPage(t.Context(), job, input, result, false, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.FailOnlineBulletinExtraction(t.Context(), job, "recognition_failed", time.Now().UTC(), 10); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='queued',attempts=0,available_at=$2 WHERE id=$1`, job.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	retry, err := repo.ClaimOnlineBulletinExtraction(t.Context(), time.Now().UTC(), onlinebulletins.RecognitionJobTimeout, 10)
	if err != nil || retry == nil {
		t.Fatal(retry, err)
	}
	if err = store.StartRecognitionPage(t.Context(), job, input, time.Now().UTC()); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatalf("old attempt wrote progress: %v", err)
	}
	if err = store.BeginRecognitionProgress(t.Context(), *retry, 4, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = store.StartRecognitionPage(t.Context(), *retry, input, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteRecognitionPage(t.Context(), *retry, input, result, true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	after, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || after.VerifiedPages != 1 || after.VerifiedReuseCount != 1 || after.ProgressVersion <= before.ProgressVersion || after.AttemptID == before.AttemptID || !after.LastProgressAt.Equal(*before.LastProgressAt) {
		t.Fatalf("retry lost proof or invented advancement: before=%+v after=%+v %v", before, after, err)
	}
	if err = store.BeginRecognitionProgress(t.Context(), *retry, 4, retry.LeaseUntil.Add(time.Second)); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatalf("expired lease wrote progress: %v", err)
	}
	if _, err = repo.db.Exec(`UPDATE hhc_web.bulletin_version SET title='new canonical metadata' WHERE issue_id=$1`, job.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if err = store.BeginRecognitionProgress(t.Context(), *retry, 4, time.Now().UTC()); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatalf("stale canonical wrote progress: %v", err)
	}
	changed, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || changed.VerifiedPages != 0 || changed.TotalPages != nil || changed.SourceIdentity == after.SourceIdentity {
		t.Fatalf("old metadata proof exposed: %+v %v", changed, err)
	}
}

func TestProductionProgressResultTamperDoesNotCount(t *testing.T) {
	repo, job, store, reader := progressFixture(t)
	if err := store.BeginRecognitionProgress(t.Context(), job, 4, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	input, result := progressPage(t, job, 1, nil)
	persistProgressCheckpoint(t, repo, job, input, result)
	if err := store.CompleteRecognitionPage(t.Context(), job, input, result, false, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_recognition_checkpoint SET checkpoint_json=jsonb_set(checkpoint_json,'{result,blocks,0,text}','"changed source text"') WHERE job_id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	p, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || p.VerifiedPages != 0 {
		t.Fatalf("changed materialization still counted: %+v %v", p, err)
	}
}

func TestProductionProgressFortyPageQueryPlan(t *testing.T) {
	repo, job, store, reader := progressFixture(t)
	if err := store.BeginRecognitionProgress(t.Context(), job, 40, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var previous *onlinebulletins.RecognitionContinuation
	for page := 1; page <= 40; page++ {
		input, result := progressPage(t, job, page, previous)
		input.TotalPages = 40
		persistProgressCheckpoint(t, repo, job, input, result)
		if err := store.CompleteRecognitionPage(t.Context(), job, input, result, false, time.Now().UTC()); err != nil {
			t.Fatal(page, err)
		}
		var err error
		previous, err = onlinebulletins.NextRecognitionContinuation(input, result)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.db.Exec(`INSERT INTO hhc_web.online_bulletin_extraction_job(id,document_id,issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version,status,superseded,created_at) SELECT gen_random_uuid(),document_id,issue_id,series,content_locale,source_asset_id,source_asset_checksum,'history-'||n,extractor_version,'failed',true,now()+interval '1 day' FROM hhc_web.online_bulletin_extraction_job CROSS JOIN generate_series(1,20) n WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	p, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || p == nil || p.JobID != job.ID || p.VerifiedPages != 40 {
		t.Fatalf("forty page/current job prefix: %+v %v", p, err)
	}
	var raw []byte
	query := ` FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant' WHERE i.id=$1`
	if err := repo.db.QueryRow(`EXPLAIN (ANALYZE, BUFFERS, VERBOSE, FORMAT JSON) SELECT `+onlineBulletinProductionProjectionSQL+query, job.Canonical.IssueID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []map[string]any
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	prefixLoops := float64(0)
	var walk func(map[string]any)
	walk = func(node map[string]any) {
		name, _ := node["Function Name"].(string)
		cte, _ := node["CTE Name"].(string)
		nodeType, _ := node["Node Type"].(string)
		// PostgreSQL may inline the SQL function; its bounded prefix CTE is
		// then the equivalent observable execution boundary.
		if name == "online_bulletin_production_prefix" || cte == "prefix" && nodeType == "CTE Scan" {
			prefixLoops += node["Actual Loops"].(float64)
		}
		if children, ok := node["Plans"].([]any); ok {
			for _, child := range children {
				walk(child.(map[string]any))
			}
		}
	}
	walk(plans[0]["Plan"].(map[string]any))
	if prefixLoops != 1 {
		t.Fatalf("prefix evaluated for historical jobs: loops=%v", prefixLoops)
	}
	t.Logf("40-page progress with 20 newer superseded jobs: execution_ms=%v prefix_loops=%v", plans[0]["Execution Time"], prefixLoops)
	if err := repo.db.QueryRow(`EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT i.version`+query, job.Canonical.IssueID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatal(err)
	}
	t.Logf("same-scope baseline without progress: execution_ms=%v", plans[0]["Execution Time"])
}

func TestProductionProgressHistoricalStartRemainsUnknown(t *testing.T) {
	repo, job, _, reader := progressFixture(t)
	// Simulate the migration's marker on a job with preexisting attempts, then
	// the real staff retry's attempt-counter reset before a fresh claim.
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET production_tracking_started=false,production_started_at=NULL,status='queued',attempts=0,lease_token=NULL,lease_until=NULL,available_at=$2 WHERE id=$1`, job.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	retry, err := repo.ClaimOnlineBulletinExtraction(t.Context(), time.Now().UTC(), onlinebulletins.RecognitionJobTimeout, 10)
	if err != nil || retry == nil {
		t.Fatal(retry, err)
	}
	p, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || p == nil || p.StartedAt != nil {
		t.Fatalf("historical first start invented: %+v %v", p, err)
	}
}

func TestProductionProgressOldWriterFirstStartRemainsUnknown(t *testing.T) {
	repo, job, _, reader := progressFixture(t)
	// A compatible old worker can claim a newly inserted job without recording
	// its first start. A later tracked retry cannot reconstruct that timestamp.
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET production_started_at=NULL,status='queued',attempts=1,lease_token=NULL,lease_until=NULL,available_at=$2 WHERE id=$1`, job.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	retry, err := repo.ClaimOnlineBulletinExtraction(t.Context(), time.Now().UTC(), onlinebulletins.RecognitionJobTimeout, 10)
	if err != nil || retry == nil {
		t.Fatal(retry, err)
	}
	p, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || p == nil || p.StartedAt != nil {
		t.Fatalf("old-writer first start invented: %+v %v", p, err)
	}
}

func TestProductionProgressOldWriterStaffRetryResetKeepsUnknownStart(t *testing.T) {
	repo, job, _, reader := progressFixture(t)
	// Recreate a newly inserted post-migration row before an old worker's first
	// claim. All subsequent updates use raw SQL, as compatible old binaries do.
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET production_started_at=NULL,production_tracking_started=true,status='queued',attempts=0,lease_token=NULL,lease_until=NULL WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='parsing',attempts=1,lease_token=gen_random_uuid(),lease_until=now()+interval '1 minute' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='failed',lease_token=NULL,lease_until=NULL WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	// An old staff-retry writer knows no progress columns and resets attempts.
	if _, err := repo.db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='queued',attempts=0,available_at=$2 WHERE id=$1`, job.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	retry, err := repo.ClaimOnlineBulletinExtraction(t.Context(), time.Now().UTC(), onlinebulletins.RecognitionJobTimeout, 10)
	if err != nil || retry == nil || retry.Attempts != 1 {
		t.Fatal(retry, err)
	}
	p, err := reader.LoadOnlineBulletinProduction(t.Context(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || p == nil || p.StartedAt != nil {
		t.Fatalf("old-worker/reset ABA invented first start: %+v %v", p, err)
	}
	var tracked bool
	if err := repo.db.QueryRow(`SELECT production_tracking_started FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, job.ID).Scan(&tracked); err != nil || tracked {
		t.Fatalf("unknown historical start was not durably marked: %v %v", tracked, err)
	}
}
