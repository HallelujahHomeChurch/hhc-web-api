package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func measuredLayout(t *testing.T, job onlinebulletins.LayoutJob) onlinebulletins.LayoutResult {
	t.Helper()
	document := job.Document
	document.LayoutManifest.ContentHash, document.LayoutManifest.LayoutValidationHash = "", ""
	submission, err := json.Marshal(struct {
		Document  onlinebulletins.Document          `json:"document"`
		Canonical onlinebulletins.CanonicalMetadata `json:"canonicalMetadata"`
	}{document, job.Canonical})
	if err != nil {
		t.Fatal(err)
	}
	pages := make([]map[string]any, len(document.Pages))
	for i, page := range document.Pages {
		slots := []map[string]any{}
		for _, slot := range document.LayoutManifest.Pages[i].Slots {
			slots = append(slots, map[string]any{"slotId": slot.ID})
		}
		pages[i] = map[string]any{"pageId": page.ID, "width": page.Width, "height": page.Height, "slots": slots}
	}
	measurement, err := json.Marshal(map[string]any{"contentHash": job.ContentHash, "rendererArtifactSha256": job.RendererArtifactSHA256, "chromiumVersion": "153.0.8010.12", "fontHashes": []string{strings.Repeat("c", 64)}, "pages": pages, "overflow": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(measurement)
	hash := hex.EncodeToString(digest[:])
	measurement = append(measurement[:len(measurement)-1], []byte(`,"layoutValidationHash":"`+hash+`"}`)...)
	return onlinebulletins.LayoutResult{Document: document, SubmissionJSON: submission, MeasurementJSON: measurement, ContentHash: job.ContentHash, LayoutValidationHash: hash}
}

func seedOnlineLayout(t *testing.T, db *sql.DB, now time.Time) (*Repository, onlinebulletins.ExtractionJob) {
	return seedOnlineLayoutIssues(t, db, now, nil)
}

func seedOnlineLayoutIssues(t *testing.T, db *sql.DB, now time.Time, issues []onlinebulletins.ReviewIssue) (*Repository, onlinebulletins.ExtractionJob) {
	t.Helper()
	repo := New(db)
	seedOnlineExtraction(t, db, now)
	job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, 120*time.Second, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	if err = repo.CompleteOnlineBulletinExtraction(context.Background(), *job, extractionCandidate(*job), issues, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1`, job.DocumentID); err != nil {
			t.Error(err)
		}
	})
	return repo, *job
}

func TestOnlineLayoutQueueUsesCurrentDraftAndFencesStaleWorkers(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	jobID, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now.Add(3*time.Second)); err == nil {
		t.Fatal("stale editor queued a layout")
	}
	first, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(3*time.Second), 120*time.Second, 2)
	if err != nil || first == nil || first.ID != jobID || first.Revision != 1 || first.Canonical != source.Canonical.Metadata || first.ContentHash == "" {
		t.Fatal("invalid layout job", err)
	}
	if next, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(4*time.Second), 120*time.Second, 2); err != nil || next != nil {
		t.Fatal("live lease claimed twice", err)
	}
	second, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(124*time.Second), 120*time.Second, 2)
	if err != nil || second == nil || first.LeaseToken == second.LeaseToken {
		t.Fatal("expired lease not reclaimed", err)
	}
	if err = repo.FailOnlineBulletinLayout(ctx, *first, "layout_validation_failed", nil, now.Add(125*time.Second), 2); err == nil {
		t.Fatal("stale worker wrote layout failure")
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *first, measuredLayout(t, *first), now.Add(125*time.Second)); err == nil {
		t.Fatal("expired worker published a new proof")
	}
	issues := []onlinebulletins.ReviewIssue{{Code: "layout_validation_failed", SourcePage: 2, Blocking: true}}
	if err = repo.FailOnlineBulletinLayout(ctx, *second, "layout_validation_failed", issues, now.Add(126*time.Second), 2); err != nil {
		t.Fatal(err)
	}
	var status, draft string
	var published sql.NullInt64
	if err = db.QueryRow(`SELECT j.status,d.draft_status,d.published_revision FROM hhc_web.online_bulletin_layout_job j JOIN hhc_web.online_bulletin_document d ON d.id=j.document_id WHERE j.id=$1`, jobID).Scan(&status, &draft, &published); err != nil || status != "failed" || draft != "reviewRequired" || published.Valid {
		t.Fatal("failure changed publication or remained publishable", err)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Layout == nil || len(state.Layout.ReviewIssues) != 1 || state.Layout.ReviewIssues[0] != issues[0] || state.Local.Revision != 1 {
		t.Fatal("failure location or immutable draft lost", err)
	}
}

func TestOnlineLayoutCompletionCreatesImmutableProofWithoutPublishing(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(3*time.Second), 120*time.Second, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	result := measuredLayout(t, *job)
	bad := result
	bad.ContentHash = strings.Repeat("e", 64)
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, bad, now.Add(4*time.Second)); err == nil {
		t.Fatal("mismatched proof accepted")
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, result, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, result, now.Add(5*time.Second)); err != nil {
		t.Fatal("idempotent completion", err)
	}
	var local, base, count int64
	var published sql.NullInt64
	var status, proof string
	if err = db.QueryRow(`SELECT d.local_revision,d.base_revision,d.published_revision,d.draft_status,r.layout_validation_hash,(SELECT count(*) FROM hhc_web.online_bulletin_revision WHERE document_id=d.id) FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision WHERE d.id=$1`, source.DocumentID).Scan(&local, &base, &published, &status, &proof, &count); err != nil {
		t.Fatal(err)
	}
	if local != 2 || base != 1 || published.Valid || status != "ready" || proof != result.LayoutValidationHash || count != 2 {
		t.Fatal("proof overwrote history or publication", local, base, status, count)
	}
	var stored []byte
	if err = db.QueryRow(`SELECT measurement_json FROM hhc_web.online_bulletin_layout_job WHERE id=$1`, job.ID).Scan(&stored); err != nil || string(stored) != string(result.MeasurementJSON) {
		t.Fatal("measurement proof bytes were recanonicalized", err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_layout_job SET measurement_json='{}' WHERE id=$1`, job.ID); err == nil {
		t.Fatal("completed layout proof mutated")
	}
}

func TestOnlineLayoutCompletionRejectsConcurrentCanonicalChange(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(3*time.Second), 120*time.Second, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET title='changed' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measuredLayout(t, *job), now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	var local int64
	var status string
	if err = db.QueryRow(`SELECT d.local_revision,j.status FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_layout_job j ON j.document_id=d.id WHERE j.id=$1`, job.ID).Scan(&local, &status); err != nil || local != 1 || status != "superseded" {
		t.Fatal("stale canonical proof changed Local", err)
	}
}

func TestOnlineLayoutProofFailureRollsBackRevisionAndPointers(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	id, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(3*time.Second), 120*time.Second, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`ALTER TABLE hhc_web.online_bulletin_layout_job ADD CONSTRAINT test_layout_completion_failure CHECK(id::text <> '` + id + `' OR status <> 'ready') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`ALTER TABLE hhc_web.online_bulletin_layout_job DROP CONSTRAINT test_layout_completion_failure`); err != nil {
			t.Error(err)
		}
	})
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measuredLayout(t, *job), now.Add(4*time.Second)); err == nil {
		t.Fatal("forced proof write failure ignored")
	}
	var count, local int64
	if err = db.QueryRow(`SELECT d.local_revision,(SELECT count(*) FROM hhc_web.online_bulletin_revision WHERE document_id=d.id) FROM hhc_web.online_bulletin_document d WHERE d.id=$1`, source.DocumentID).Scan(&local, &count); err != nil || count != 1 || local != 1 {
		t.Fatal("partial proof commit", err)
	}
}

func TestOnlineLayoutCanReflowSavedTextWithoutTrustingChangedText(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(3*time.Second), 120*time.Second, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	// A real compositor can move stored allocation boxes while preserving the
	// exact semantic content, creating a different final content/proof identity.
	composed := *job
	data, _ := json.Marshal(job.Document)
	if json.Unmarshal(data, &composed.Document) != nil {
		t.Fatal("fixture")
	}
	composed.Document.LayoutManifest.Pages[0].Slots[0].Box.Y = .2
	result := measuredLayout(t, composed)
	digest := sha256.Sum256(result.SubmissionJSON)
	composed.ContentHash = hex.EncodeToString(digest[:])
	result = measuredLayout(t, composed)
	if result.ContentHash == job.ContentHash {
		t.Fatal("fixture did not reflow")
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *job, result, now.Add(4*time.Second)); err != nil {
		t.Fatal("valid reflow rejected", err)
	}
}
