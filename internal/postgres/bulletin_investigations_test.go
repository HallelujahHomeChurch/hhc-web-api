package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/google/uuid"
)

func investigationFixture(t *testing.T) (*Repository, bulletinwatermark.Investigation, []bulletinwatermark.Receipt) {
	t.Helper()
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err = migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	scope := bulletinwatermark.CandidateScope{IssueID: uuid.NewString(), Series: "general", Locale: "zh-Hant", Revision: 1, SourceAssetID: testWatermarkAssetID, SourceSHA256: stringHash(), RendererVersion: "investigation-test", ModelSHA256: stringHash()}
	job := bulletinwatermark.Investigation{ID: uuid.NewString(), ActorID: uuid.NewString(), Scope: scope, Evidence: []byte("synthetic-image-bytes"), CreatedAt: now}
	t.Cleanup(func() {
		_, err := db.Exec(`DELETE FROM hhc_web.bulletin_watermark_investigation WHERE issue_id=$1`, scope.IssueID)
		if err != nil {
			t.Error(err)
		}
	})
	receipts := []bulletinwatermark.Receipt{}
	for range 2 {
		code, _ := bulletinwatermark.NewCode()
		payload, _ := bulletinwatermark.NewFingerprint()
		rec := bulletinwatermark.Receipt{ID: uuid.NewString(), Code: code, UserID: uuid.NewString(), IssueID: scope.IssueID, Series: scope.Series, Locale: scope.Locale, Revision: scope.Revision, SourceAssetID: scope.SourceAssetID, SourceSHA256: scope.SourceSHA256, OutputSHA256: stringHash(), RendererVersion: scope.RendererVersion, ModelSHA256: scope.ModelSHA256, PrivacyVersion: "test", IssuedAt: now, ExpiresAt: now.Add(bulletinwatermark.Retention), FingerprintPayload: payload, Pages: []bulletinwatermark.PageMetadata{{Page: 1, Width: 100, Height: 200, SourceSHA256: stringHash(), OutputSHA256: stringHash()}}}
		if err = r.InsertWatermarkReceipt(ctx, rec); err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, rec)
	}
	return r, job, receipts
}

func TestWatermarkInvestigationLifecycle(t *testing.T) {
	r, job, receipts := investigationFixture(t)
	ctx := context.Background()
	now := job.CreatedAt
	versions, err := r.ListWatermarkVersions(ctx, job.Scope.IssueID, now)
	if err != nil || len(versions) != 1 || versions[0].CandidateScope != job.Scope || versions[0].PageCount != 1 {
		t.Fatal("version reconstruction scope", err)
	}
	if err = r.CreateWatermarkInvestigation(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetWatermarkInvestigation(ctx, job.ID, uuid.NewString(), now); !errors.Is(err, bulletinwatermark.ErrNotFound) {
		t.Fatal("other owner could read", err)
	}
	queued, err := r.GetWatermarkInvestigation(ctx, job.ID, job.ActorID, now)
	if err != nil || queued.Status != "queued" || queued.Receipt != nil || len(queued.Evidence) != 0 {
		t.Fatal("queued disclosure", err)
	}
	claimed, found, err := r.ClaimWatermarkInvestigation(ctx, now)
	if err != nil || !found || claimed.ID != job.ID || string(claimed.Evidence) != string(job.Evidence) {
		t.Fatal("claim", err)
	}
	completion := bulletinwatermark.InvestigationCompletion{Outcome: "matched", ReceiptID: receipts[0].ID, MatchMethod: bulletinwatermark.MatchCandidate, CandidateSetHash: bulletinwatermark.CandidateSetHash(receipts)}
	if err = r.CompleteWatermarkInvestigation(ctx, job.ID, claimed.LeaseToken, completion, now); err != nil {
		t.Fatal(err)
	}
	if err = r.CompleteWatermarkInvestigation(ctx, job.ID, claimed.LeaseToken, completion, now); !errors.Is(err, bulletinwatermark.ErrInvestigationLease) {
		t.Fatal("duplicate completion", err)
	}
	retry := job
	retry.CreatedAt = now.Add(time.Hour)
	if err = r.CreateWatermarkInvestigation(ctx, retry); err != nil {
		t.Fatal("idempotent retry after completion", err)
	}
	changed := job
	changed.Evidence = []byte("different-evidence")
	if err = r.CreateWatermarkInvestigation(ctx, changed); !errors.Is(err, bulletinwatermark.ErrInvestigationConflict) {
		t.Fatal("changed payload reused idempotency key", err)
	}
	changed = job
	changed.ActorID = uuid.NewString()
	if err = r.CreateWatermarkInvestigation(ctx, changed); !errors.Is(err, bulletinwatermark.ErrInvestigationConflict) {
		t.Fatal("foreign actor reused idempotency key", err)
	}
	result, err := r.GetWatermarkInvestigation(ctx, job.ID, job.ActorID, now)
	if err != nil || result.Result != "matched" || result.MatchMethod != bulletinwatermark.MatchCandidate || result.Receipt == nil || result.Receipt.ID != receipts[0].ID {
		t.Fatal("matched result", err)
	}
	history, err := r.ListWatermarkInvestigations(ctx, job.Scope.IssueID, job.ActorID, "", 50, now)
	if err != nil || len(history.Items) != 1 || history.Items[0].IssueID != job.Scope.IssueID || history.Items[0].ActualIssueID != receipts[0].IssueID || history.Items[0].MatchMethod != bulletinwatermark.MatchCandidate {
		t.Fatal("investigation issue projections", err)
	}
	var evidence bool
	var audits int
	if err = r.db.QueryRow(`SELECT evidence IS NULL FROM hhc_web.bulletin_watermark_investigation WHERE id=$1`, job.ID).Scan(&evidence); err != nil || !evidence {
		t.Fatal("terminal evidence retained", err)
	}
	if err = r.db.QueryRow(`SELECT count(*) FROM hhc_web.cms_audit_event WHERE action='bulletin_watermark.lookup' AND actor=$1`, job.ActorID).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("disclosure audit missing", err)
	}
	if err = seedWatermarkRestriction(ctx, r.db, receipts[0].UserID); err != nil {
		t.Fatal(err)
	}
	result, err = r.GetWatermarkInvestigation(ctx, job.ID, job.ActorID, now)
	if err != nil || result.Result != "inconclusive" || result.Receipt != nil {
		t.Fatal("restricted subject disclosed", err)
	}
	if _, _, err = r.CleanupWatermarkAccount(ctx, receipts[0].UserID, "investigation-erase:"+receipts[0].UserID); err != nil {
		t.Fatal(err)
	}
	var noReceipt bool
	if err = r.db.QueryRow(`SELECT receipt_id IS NULL FROM hhc_web.bulletin_watermark_investigation WHERE id=$1`, job.ID).Scan(&noReceipt); err != nil || !noReceipt {
		t.Fatal("erased subject reference retained", err)
	}
	if _, _, err = r.CleanupWatermarkAccount(ctx, job.ActorID, "investigation-owner-erase:"+job.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetWatermarkInvestigation(ctx, job.ID, job.ActorID, now); !errors.Is(err, bulletinwatermark.ErrNotFound) {
		t.Fatal("erased actor history retained", err)
	}
}

func TestWatermarkInvestigationAcceptsMultipleLocalesForOneIssue(t *testing.T) {
	r, job, _ := investigationFixture(t)
	second := job.Scope
	second.Locale = "en"
	job.Scopes = []bulletinwatermark.CandidateScope{job.Scope, second}

	if err := r.CreateWatermarkInvestigation(context.Background(), job); err != nil {
		t.Fatal(err)
	}
}

func TestWatermarkInvestigationCandidateChange(t *testing.T) {
	r, job, receipts := investigationFixture(t)
	ctx := context.Background()
	now := job.CreatedAt
	if err := r.CreateWatermarkInvestigation(ctx, job); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := r.ClaimWatermarkInvestigation(ctx, now)
	if err != nil || !found {
		t.Fatal(err)
	}
	if err = seedWatermarkRestriction(ctx, r.db, receipts[1].UserID); err != nil {
		t.Fatal(err)
	}
	if err = r.CompleteWatermarkInvestigation(ctx, job.ID, claimed.LeaseToken, bulletinwatermark.InvestigationCompletion{Outcome: "matched", ReceiptID: receipts[0].ID, MatchMethod: bulletinwatermark.MatchCandidate, CandidateSetHash: bulletinwatermark.CandidateSetHash(receipts)}, now); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetWatermarkInvestigation(ctx, job.ID, job.ActorID, now)
	if err != nil || got.Status != "failed" || got.ErrorCode != "candidate_set_changed" || got.Receipt != nil {
		t.Fatal("changed set disclosed", err)
	}
}

func TestWatermarkInvestigationGlobalExactCompletion(t *testing.T) {
	r, job, receipts := investigationFixture(t)
	ctx := context.Background()
	now := job.CreatedAt
	external := receipts[0]
	external.ID, external.UserID, external.IssueID = uuid.NewString(), uuid.NewString(), uuid.NewString()
	external.Code, _ = bulletinwatermark.NewCode()
	external.FingerprintPayload, _ = bulletinwatermark.NewFingerprint()
	external.OutputSHA256 = strings.Repeat("d", 64)
	if err := r.InsertWatermarkReceipt(ctx, external); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = r.db.Exec(`DELETE FROM hhc_web.bulletin_watermark_receipt WHERE issue_id=$1`, external.IssueID)
	})

	for _, test := range []struct {
		name   string
		method bulletinwatermark.MatchMethod
		proof  string
	}{
		{name: "output sha", method: bulletinwatermark.MatchOutputSHA256, proof: external.OutputSHA256},
		{name: "payload", method: bulletinwatermark.MatchPayload, proof: external.FingerprintPayload},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := job
			current.ID = uuid.NewString()
			if err := r.CreateWatermarkInvestigation(ctx, current); err != nil {
				t.Fatal(err)
			}
			claim, err := r.ClaimWatermarkInvestigationByID(ctx, current.ID, now)
			if err != nil {
				t.Fatal(err)
			}
			completion := bulletinwatermark.InvestigationCompletion{Outcome: "matched", ReceiptID: external.ID, MatchMethod: test.method, Proof: test.proof}
			if err = r.CompleteWatermarkInvestigation(ctx, current.ID, claim.LeaseToken, completion, now); err != nil {
				t.Fatal(err)
			}
			var storedHash string
			if err = r.db.QueryRow(`SELECT candidate_set_hash FROM hhc_web.bulletin_watermark_investigation WHERE id=$1`, current.ID).Scan(&storedHash); err != nil || storedHash != bulletinwatermark.CandidateSetHash(nil) || storedHash == test.proof {
				t.Fatalf("proof persisted or structural hash missing: %q err=%v", storedHash, err)
			}
			got, err := r.GetWatermarkInvestigation(ctx, current.ID, current.ActorID, now)
			if err != nil || got.Receipt == nil || got.Receipt.ID != external.ID || got.MatchMethod != test.method || got.Receipt.IssueID == current.Scope.IssueID {
				t.Fatalf("completion=%+v receipt=%+v err=%v", got.Investigation, got.Receipt, err)
			}
		})
	}
}

func TestGlobalWatermarkInvestigationHistoryPaginationAndAudit(t *testing.T) {
	r, job, receipts := investigationFixture(t)
	ctx := context.Background()
	now := job.CreatedAt
	externalIssue := uuid.NewString()
	for _, issue := range []struct {
		id, date string
		number   int
	}{{job.Scope.IssueID, "2099-01-01", 1738}, {externalIssue, "2099-01-02", 1737}} {
		if _, err := r.db.Exec(`INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,'published',1,$4,$5,$5,$6,$6)`, issue.id, issue.number, issue.date, "history:"+issue.id, job.ActorID, now); err != nil {
			t.Fatal(err)
		}
	}
	external := receipts[0]
	external.ID, external.UserID, external.IssueID = uuid.NewString(), uuid.NewString(), externalIssue
	external.Code, _ = bulletinwatermark.NewCode()
	external.FingerprintPayload, _ = bulletinwatermark.NewFingerprint()
	external.OutputSHA256 = strings.Repeat("e", 64)
	if err := r.InsertWatermarkReceipt(ctx, external); err != nil {
		t.Fatal(err)
	}
	ids := []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002"}
	for index, id := range ids {
		current := job
		current.ID = id
		if err := r.CreateWatermarkInvestigation(ctx, current); err != nil {
			t.Fatal(err)
		}
		claim, err := r.ClaimWatermarkInvestigationByID(ctx, id, now)
		if err != nil {
			t.Fatal(err)
		}
		completion := bulletinwatermark.InvestigationCompletion{Outcome: "inconclusive", ReasonCode: "watermark_inconclusive"}
		if index == 0 {
			completion = bulletinwatermark.InvestigationCompletion{Outcome: "matched", ReceiptID: external.ID, MatchMethod: bulletinwatermark.MatchOutputSHA256, Proof: external.OutputSHA256}
		}
		if err = r.CompleteWatermarkInvestigation(ctx, id, claim.LeaseToken, completion, now); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = r.db.Exec(`DELETE FROM hhc_web.cms_audit_event WHERE actor=$1`, job.ActorID)
		_, _ = r.db.Exec(`DELETE FROM hhc_web.bulletin_watermark_investigation WHERE id=$1 OR id=$2`, ids[0], ids[1])
		_, _ = r.db.Exec(`DELETE FROM hhc_web.bulletin_watermark_receipt WHERE id=$1`, external.ID)
		_, _ = r.db.Exec(`DELETE FROM hhc_web.bulletin_issue WHERE id IN ($1,$2)`, job.Scope.IssueID, externalIssue)
	})

	first, err := r.ListGlobalWatermarkInvestigations(ctx, job.ActorID, "", 1, "submitted", "desc", now)
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != ids[1] || first.NextCursor == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	decoded, err := decodeGlobalInvestigationCursor(first.NextCursor)
	if err != nil || decoded == nil || decoded.ID != ids[1] {
		t.Fatalf("cursor=%+v err=%v", decoded, err)
	}
	second, err := r.ListGlobalWatermarkInvestigations(ctx, job.ActorID, first.NextCursor, 1, "submitted", "desc", now)
	if err != nil || len(second.Items) != 1 || second.Items[0].ID != ids[0] || second.NextCursor != "" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	row := second.Items[0]
	if row.IssueNumber != 1738 || row.IssueDate != "2099-01-01" || row.ActualIssueID != externalIssue || row.ActualIssueNumber != 1737 || row.ActualIssueDate != "2099-01-02" || row.MatchedAccountID != external.UserID || row.ReceiptID != external.ID {
		t.Fatalf("projection=%+v", row)
	}
	if all, readErr := r.ListGlobalWatermarkInvestigations(ctx, uuid.NewString(), "", 10, "submitted", "desc", now); readErr != nil || len(all.Items) != 2 {
		t.Fatalf("authorized operator did not see global history: %+v err=%v", all, readErr)
	}
	var audits int
	if err = r.db.QueryRow(`SELECT count(*) FROM hhc_web.cms_audit_event WHERE action='bulletin_watermark.lookup' AND actor=$1 AND resource_id=$2`, job.ActorID, external.ID).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
	name := "test_global_audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	statement := fmt.Sprintf(`CREATE FUNCTION hhc_web.%s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor='%s' THEN RAISE EXCEPTION 'synthetic audit unavailable'; END IF; RETURN NEW; END; $$; CREATE TRIGGER %s BEFORE INSERT ON hhc_web.cms_audit_event FOR EACH ROW EXECUTE FUNCTION hhc_web.%s()`, name, job.ActorID, name, name)
	if _, err = r.db.Exec(statement); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = r.db.Exec(fmt.Sprintf(`DROP TRIGGER %s ON hhc_web.cms_audit_event; DROP FUNCTION hhc_web.%s()`, name, name))
	})
	if page, readErr := r.ListGlobalWatermarkInvestigations(ctx, job.ActorID, first.NextCursor, 1, "submitted", "desc", now); readErr == nil || len(page.Items) != 0 {
		t.Fatalf("audit failure disclosed page=%+v err=%v", page, readErr)
	}
	if err = seedWatermarkRestriction(ctx, r.db, external.UserID); err != nil {
		t.Fatal(err)
	}
	if page, readErr := r.ListGlobalWatermarkInvestigations(ctx, job.ActorID, first.NextCursor, 1, "submitted", "desc", now); readErr != nil || len(page.Items) != 1 || page.Items[0].MatchedAccountID != "" || page.Items[0].ActualIssueID != "" {
		t.Fatalf("restricted identity projected page=%+v err=%v", page, readErr)
	}
}

func TestWatermarkInvestigationCompletedNonMatchesKeepStableReasons(t *testing.T) {
	r, job, receipts := investigationFixture(t)
	ctx := context.Background()
	for _, test := range []struct {
		outcome, reason string
	}{
		{outcome: "inconclusive", reason: "watermark_inconclusive"},
		{outcome: "conflict", reason: "evidence_conflict"},
	} {
		t.Run(test.outcome, func(t *testing.T) {
			current := job
			current.ID = uuid.NewString()
			if err := r.CreateWatermarkInvestigation(ctx, current); err != nil {
				t.Fatal(err)
			}
			claim, err := r.ClaimWatermarkInvestigationByID(ctx, current.ID, current.CreatedAt)
			if err != nil {
				t.Fatal(err)
			}
			completion := bulletinwatermark.InvestigationCompletion{Outcome: test.outcome, ReasonCode: test.reason, CandidateSetHash: bulletinwatermark.CandidateSetHash(receipts)}
			if err = r.CompleteWatermarkInvestigation(ctx, current.ID, claim.LeaseToken, completion, current.CreatedAt); err != nil {
				t.Fatal(err)
			}
			got, err := r.GetWatermarkInvestigation(ctx, current.ID, current.ActorID, current.CreatedAt)
			if err != nil || got.Status != "completed" || got.Result != test.outcome || got.ErrorCode != test.reason || got.Receipt != nil || got.MatchMethod != "" {
				t.Fatalf("completion=%+v err=%v", got.Investigation, err)
			}
		})
	}
}

func TestWatermarkInvestigationLeaseAndRetention(t *testing.T) {
	r, job, _ := investigationFixture(t)
	ctx := context.Background()
	now := job.CreatedAt
	if err := r.CreateWatermarkInvestigation(ctx, job); err != nil {
		t.Fatal(err)
	}
	first, found, err := r.ClaimWatermarkInvestigation(ctx, now)
	if err != nil || !found {
		t.Fatal(err)
	}
	second, found, err := r.ClaimWatermarkInvestigation(ctx, now.Add(11*time.Minute))
	if err != nil || !found || first.LeaseToken == second.LeaseToken || second.Attempts != 2 {
		t.Fatal("lease retry", err)
	}
	if err = r.CompleteWatermarkInvestigation(ctx, job.ID, first.LeaseToken, bulletinwatermark.InvestigationCompletion{Outcome: "failed", ReasonCode: "worker_failure"}, now.Add(11*time.Minute)); !errors.Is(err, bulletinwatermark.ErrInvestigationLease) {
		t.Fatal("old lease completed", err)
	}
	_, found, err = r.ClaimWatermarkInvestigation(ctx, now.Add(22*time.Minute))
	if err != nil || found {
		t.Fatal("exhausted job retried", err)
	}
	got, err := r.GetWatermarkInvestigation(ctx, job.ID, job.ActorID, now.Add(22*time.Minute))
	if err != nil || got.Status != "failed" || got.ErrorCode != "job_timeout" {
		t.Fatal("attempt exhaustion", err)
	}
	expired := job
	expired.ID = uuid.NewString()
	expired.CreatedAt = now.Add(-25 * time.Hour)
	if err = r.CreateWatermarkInvestigation(ctx, expired); err != nil {
		t.Fatal(err)
	}
	got, err = r.GetWatermarkInvestigation(ctx, expired.ID, expired.ActorID, now)
	if err != nil || got.Status != "expired" {
		t.Fatal("evidence TTL", err)
	}
	if _, err = r.PurgeWatermarkRecords(ctx, now.Add(bulletinwatermark.Retention), 1000); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = r.db.QueryRow(`SELECT count(*) FROM hhc_web.bulletin_watermark_investigation WHERE issue_id=$1`, job.Scope.IssueID).Scan(&count); err != nil || count != 0 {
		t.Fatal("365-day metadata expiry", err)
	}
}

func TestWatermarkInvestigationOwnerLimits(t *testing.T) {
	r, job, _ := investigationFixture(t)
	ctx := context.Background()
	for range 3 {
		job.ID = uuid.NewString()
		if err := r.CreateWatermarkInvestigation(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	job.ID = uuid.NewString()
	if err := r.CreateWatermarkInvestigation(ctx, job); !errors.Is(err, bulletinwatermark.ErrRateLimited) {
		t.Fatal("pending limit", err)
	}
	if err := seedWatermarkRestriction(ctx, r.db, job.ActorID); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateWatermarkInvestigation(ctx, job); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatal("restricted owner created job", err)
	}
	var pending int
	if err := r.db.QueryRow(`SELECT count(*) FROM hhc_web.bulletin_watermark_investigation WHERE actor_id=$1 AND (status IN ('queued','running') OR evidence IS NOT NULL)`, job.ActorID).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("restriction did not cancel evidence", err)
	}
}

func TestWatermarkInvestigationAuditFailureDisclosesNothing(t *testing.T) {
	r, job, receipts := investigationFixture(t)
	ctx := context.Background()
	now := job.CreatedAt
	if err := r.CreateWatermarkInvestigation(ctx, job); err != nil {
		t.Fatal(err)
	}
	claim, found, err := r.ClaimWatermarkInvestigation(ctx, now)
	if err != nil || !found {
		t.Fatal(err)
	}
	if err = r.CompleteWatermarkInvestigation(ctx, job.ID, claim.LeaseToken, bulletinwatermark.InvestigationCompletion{Outcome: "matched", ReceiptID: receipts[0].ID, MatchMethod: bulletinwatermark.MatchCandidate, CandidateSetHash: bulletinwatermark.CandidateSetHash(receipts)}, now); err != nil {
		t.Fatal(err)
	}
	// UUID-only generated SQL identifiers and one synthetic actor isolate this injected failure.
	name := "test_audit_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	statement := fmt.Sprintf(`CREATE FUNCTION hhc_web.%s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor='%s' THEN RAISE EXCEPTION 'synthetic audit unavailable'; END IF; RETURN NEW; END; $$; CREATE TRIGGER %s BEFORE INSERT ON hhc_web.cms_audit_event FOR EACH ROW EXECUTE FUNCTION hhc_web.%s()`, name, job.ActorID, name, name)
	if _, err = r.db.Exec(statement); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := r.db.Exec(fmt.Sprintf(`DROP TRIGGER %s ON hhc_web.cms_audit_event; DROP FUNCTION hhc_web.%s()`, name, name)); err != nil {
			t.Error(err)
		}
	})
	got, err := r.GetWatermarkInvestigation(ctx, job.ID, job.ActorID, now)
	if err == nil || got.Receipt != nil {
		t.Fatal("audit failure released identity")
	}
	var count int
	if err = r.db.QueryRow(`SELECT count FROM hhc_web.bulletin_watermark_lookup_rate WHERE subject_hash=$1 AND window_start=$2`, watermarkSubjectHash(job.ActorID), now.Truncate(time.Minute)).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed disclosure transaction did not roll back", err)
	}
}

func TestWatermarkInvestigationWaitsForSubjectFence(t *testing.T) {
	r, job, receipts := investigationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := job.CreatedAt
	if err := r.CreateWatermarkInvestigation(ctx, job); err != nil {
		t.Fatal(err)
	}
	claim, found, err := r.ClaimWatermarkInvestigation(ctx, now)
	if err != nil || !found {
		t.Fatal(err)
	}
	if err = r.CompleteWatermarkInvestigation(ctx, job.ID, claim.LeaseToken, bulletinwatermark.InvestigationCompletion{Outcome: "matched", ReceiptID: receipts[0].ID, MatchMethod: bulletinwatermark.MatchCandidate, CandidateSetHash: bulletinwatermark.CandidateSetHash(receipts)}, now); err != nil {
		t.Fatal(err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var blocker int
	if err = tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if err = lockWatermarkSubject(ctx, tx, receipts[0].UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES($1,false)`, watermarkSubjectHash(receipts[0].UserID)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		view bulletinwatermark.InvestigationView
		err  error
	}
	done := make(chan result, 1)
	go func() { v, e := r.GetWatermarkInvestigation(ctx, job.ID, job.ActorID, now); done <- result{v, e} }()
	for {
		var waiting bool
		if err = r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-done:
			t.Fatal("result bypassed subject fence")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.view.Receipt != nil || got.view.Result != "inconclusive" {
		t.Fatal("subject restriction race disclosed identity", got.err)
	}
}

func TestWatermarkInvestigationConcurrentClaims(t *testing.T) {
	r, job, _ := investigationFixture(t)
	ctx := context.Background()
	if err := r.CreateWatermarkInvestigation(ctx, job); err != nil {
		t.Fatal(err)
	}
	second := job
	second.ID = uuid.NewString()
	if err := r.CreateWatermarkInvestigation(ctx, second); err != nil {
		t.Fatal(err)
	}
	type result struct {
		v     bulletinwatermark.Investigation
		found bool
		err   error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			v, found, err := r.ClaimWatermarkInvestigation(ctx, job.CreatedAt)
			results <- result{v, found, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || !a.found || !b.found || a.v.ID == b.v.ID {
		t.Fatal("replicas claimed the same job", a.err, b.err)
	}
}

func TestGlobalInvestigationCursorPreservesOrdering(t *testing.T) {
	want := globalInvestigationCursor{Sort: "account", Direction: "asc", Key: "alice", ID: uuid.NewString(), Missing: 0}
	got, err := decodeGlobalInvestigationCursor(encodeGlobalInvestigationCursor(want))
	if err != nil || got == nil || *got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
	missing := globalInvestigationCursor{Sort: "account", Direction: "asc", ID: uuid.NewString(), Missing: 1}
	got, err = decodeGlobalInvestigationCursor(encodeGlobalInvestigationCursor(missing))
	if err != nil || got == nil || *got != missing {
		t.Fatalf("missing value cursor got %+v, %v", got, err)
	}
}

func TestWatermarkIdentityProjectionSelectsReferencesAndStoresUnavailablePlaceholders(t *testing.T) {
	r, job, receipts := investigationFixture(t)
	ctx := context.Background()
	requireIDs := []string{job.ActorID, receipts[0].UserID}
	if err := r.CreateWatermarkInvestigation(ctx, job); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = r.db.Exec(`DELETE FROM hhc_web.account_identity_projection WHERE account_user_id IN ($1,$2)`, requireIDs[0], requireIDs[1])
	})
	ids, err := r.ListStaleWatermarkIdentityIDs(ctx, time.Now().UTC(), 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	for _, id := range requireIDs {
		if !seen[id] {
			t.Fatalf("referenced identity %s was not selected", id)
		}
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := r.UpsertWatermarkIdentities(ctx, requireIDs, []accountclient.Identity{{ID: job.ActorID, DisplayName: "Operator", Email: "operator@example.test"}}, now); err != nil {
		t.Fatal(err)
	}
	rows, err := r.db.Query(`SELECT account_user_id::text,display_name,email,available FROM hhc_web.account_identity_projection WHERE account_user_id IN ($1,$2)`, requireIDs[0], requireIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	projected := map[string]struct {
		name, email string
		available   bool
	}{}
	for rows.Next() {
		var id, name, email string
		var available bool
		if err := rows.Scan(&id, &name, &email, &available); err != nil {
			t.Fatal(err)
		}
		projected[id] = struct {
			name, email string
			available   bool
		}{name, email, available}
	}
	if got := projected[job.ActorID]; !got.available || got.name != "Operator" || got.email != "operator@example.test" {
		t.Fatalf("resolved projection %+v", got)
	}
	if got := projected[receipts[0].UserID]; got.available || got.name != "" || got.email != "" {
		t.Fatalf("unavailable projection %+v", got)
	}
}
