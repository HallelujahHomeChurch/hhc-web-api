package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
)

func newRecoveryJob(t *testing.T, db *sql.DB, r *Repository, now time.Time) bulletinwatermark.DownloadJob {
	t.Helper()
	request := bulletinwatermark.DownloadJobRequest{
		ID: uuid.NewString(), IdempotencyKey: uuid.NewString(), AccountID: uuid.NewString(),
		Source:       bulletinwatermark.Source{IssueID: uuid.NewString(), Series: "general", Locale: "zh-Hant", Revision: 1, AssetID: testWatermarkAssetID, Filename: "weekly.pdf"},
		SourceSHA256: stringHash(), RendererVersion: "trustmark-v2", Now: now,
	}
	request.TraceCode, _ = bulletinwatermark.NewCode()
	request.FingerprintPayload, _ = bulletinwatermark.NewFingerprint()
	var number int
	if err := db.QueryRow(`SELECT COALESCE(max(issue_number),0)+1,(COALESCE(max(issue_date),CURRENT_DATE)+1)::text FROM hhc_web.bulletin_issue`).Scan(&number, &request.IssueDate); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,'published',1,$6,$4,$4,$5,$5)`, request.Source.IssueID, number, request.IssueDate, request.AccountID, now, request.Source.IssueID); err != nil {
		t.Fatal(err)
	}
	job, err := r.CreateDownloadJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

type recoveryState struct {
	status, errorCode string
	attempts          int
	leased            bool
	wakePending       bool
}

func readRecoveryState(t *testing.T, db *sql.DB, job bulletinwatermark.DownloadJob) recoveryState {
	t.Helper()
	var state recoveryState
	if err := db.QueryRow(`SELECT status,COALESCE(error_code,''),attempts,lease_token IS NOT NULL FROM hhc_web.bulletin_download_job WHERE id=$1`, job.ID).Scan(&state.status, &state.errorCode, &state.attempts, &state.leased); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM hhc_web.outbox_event WHERE destination='bulletin-watermark-queue' AND aggregate_id=$1 AND status='pending')`, job.ID).Scan(&state.wakePending); err != nil {
		t.Fatal(err)
	}
	return state
}

// markWakeDispatched simulates the outbox having already sent the wake-up.
func markWakeDispatched(t *testing.T, db *sql.DB, job bulletinwatermark.DownloadJob) {
	t.Helper()
	if _, err := db.Exec(`UPDATE hhc_web.outbox_event SET status='delivered' WHERE destination='bulletin-watermark-queue' AND aggregate_id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseDownloadJobRequeuesUntilAttemptsAreSpent(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	job := newRecoveryJob(t, db, r, now)
	for attempt := 1; attempt <= 3; attempt++ {
		claimed, err := r.ClaimDownloadJob(ctx, job.ID, now)
		if err != nil {
			t.Fatalf("attempt %d claim: %v", attempt, err)
		}
		exhausted, err := r.ReleaseDownloadJob(ctx, job.ID, claimed.LeaseToken, now)
		if err != nil || exhausted != (attempt == 3) {
			t.Fatalf("attempt %d release exhausted=%v err=%v", attempt, exhausted, err)
		}
		state := readRecoveryState(t, db, job)
		want := "queued"
		if attempt == 3 {
			want = "failed"
		}
		if state.status != want || state.attempts != attempt || state.leased {
			t.Fatalf("attempt %d state %+v", attempt, state)
		}
	}
	if state := readRecoveryState(t, db, job); state.errorCode != "retry_exhausted" {
		t.Fatalf("exhausted state %+v", state)
	}
	if _, err := r.ReleaseDownloadJob(ctx, job.ID, uuid.NewString(), now); !errors.Is(err, bulletinwatermark.ErrDownloadLease) {
		t.Fatalf("stale lease release err=%v", err)
	}
}

// Scenario 1: the worker is killed mid-render and never releases its lease.
func TestRecoverStalledDownloadJobRequeuesKilledRender(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	job := newRecoveryJob(t, db, r, now)
	claimed, err := r.ClaimDownloadJob(ctx, job.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	markWakeDispatched(t, db, job)
	// Within lease + grace nothing happens.
	if _, _, err = r.RecoverStalledDownloadJobs(ctx, now.Add(downloadLease+downloadLeaseGrace-30*time.Second), 1000); err != nil {
		t.Fatal(err)
	}
	if state := readRecoveryState(t, db, job); state.status != "running" || state.wakePending {
		t.Fatalf("recovered a live lease: %+v", state)
	}
	recoveredAt := now.Add(downloadLease + downloadLeaseGrace) // ≤ 15 minute SLO
	requeued, _, err := r.RecoverStalledDownloadJobs(ctx, recoveredAt, 1000)
	if err != nil || requeued < 1 {
		t.Fatalf("requeued=%d err=%v", requeued, err)
	}
	state := readRecoveryState(t, db, job)
	if state.status != "queued" || state.leased || state.attempts != 1 || !state.wakePending {
		t.Fatalf("killed render state %+v", state)
	}
	// The killed worker's lease is fenced.
	if err = r.CompleteDownloadJob(ctx, job.ID, claimed.LeaseToken, testWatermarkAssetID, bulletinwatermark.Receipt{ID: uuid.NewString()}, recoveredAt); err == nil {
		t.Fatal("stale lease completed a recovered job")
	}
	if _, err = r.ClaimDownloadJob(ctx, job.ID, recoveredAt); err != nil {
		t.Fatalf("re-delivered job not claimable: %v", err)
	}
}

// Scenario 2: the message returns before the lease expires, the claim fails
// and the message is acked; only the sweeper can recover the job.
func TestRecoverStalledDownloadJobAfterEarlyRedelivery(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	job := newRecoveryJob(t, db, r, now)
	if _, err := r.ClaimDownloadJob(ctx, job.ID, now); err != nil {
		t.Fatal(err)
	}
	markWakeDispatched(t, db, job)
	if _, err := r.ClaimDownloadJob(ctx, job.ID, now.Add(downloadLease/2)); !errors.Is(err, bulletinwatermark.ErrNotFound) {
		t.Fatalf("early redelivery claimed a live lease: %v", err)
	}
	if _, _, err := r.RecoverStalledDownloadJobs(ctx, now.Add(downloadLease+downloadLeaseGrace+time.Minute), 1000); err != nil {
		t.Fatal(err)
	}
	if state := readRecoveryState(t, db, job); state.status != "queued" || !state.wakePending {
		t.Fatalf("early redelivery state %+v", state)
	}
}

// Scenario 3: the wake-up message is poisoned or lost while the job is queued.
// Each stalled interval re-sends the wake-up and costs one attempt, so the job
// reaches failed within three sweeps instead of waiting forever.
func TestRecoverStalledDownloadJobBoundsLostQueuedMessages(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	job := newRecoveryJob(t, db, r, now)
	markWakeDispatched(t, db, job)
	if _, _, err := r.RecoverStalledDownloadJobs(ctx, now.Add(4*time.Minute), 1000); err != nil {
		t.Fatal(err)
	}
	if state := readRecoveryState(t, db, job); state.attempts != 0 || state.wakePending {
		t.Fatalf("fresh queued job was swept: %+v", state)
	}
	at := now
	for sweep := 1; sweep <= 3; sweep++ {
		at = at.Add(5 * time.Minute)
		if _, _, err := r.RecoverStalledDownloadJobs(ctx, at, 1000); err != nil {
			t.Fatal(err)
		}
		state := readRecoveryState(t, db, job)
		if state.status != "queued" || state.attempts != sweep || !state.wakePending {
			t.Fatalf("sweep %d state %+v", sweep, state)
		}
		markWakeDispatched(t, db, job)
	}
	if _, failed, err := r.RecoverStalledDownloadJobs(ctx, at.Add(time.Minute), 1000); err != nil || failed < 1 {
		t.Fatalf("failed=%d err=%v", failed, err)
	}
	if state := readRecoveryState(t, db, job); state.status != "failed" || state.errorCode != "retry_exhausted" {
		t.Fatalf("lost message state %+v", state)
	}
	if at.Sub(now) > 15*time.Minute+time.Minute {
		t.Fatalf("lost message took %s to fail", at.Sub(now))
	}
}

func TestRecoverStalledDownloadJobFailsExhaustedExpiredLease(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	job := newRecoveryJob(t, db, r, now)
	if _, err := r.ClaimDownloadJob(ctx, job.ID, now); err != nil {
		t.Fatal(err)
	}
	// Third attempt is running when the worker dies: previously permanent.
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_download_job SET attempts=3 WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ClaimDownloadJob(ctx, job.ID, now.Add(11*time.Minute)); !errors.Is(err, bulletinwatermark.ErrNotFound) {
		t.Fatalf("exhausted job claimed: %v", err)
	}
	if _, _, err := r.RecoverStalledDownloadJobs(ctx, now.Add(11*time.Minute), 1000); err != nil {
		t.Fatal(err)
	}
	if state := readRecoveryState(t, db, job); state.status != "failed" || state.errorCode != "retry_exhausted" {
		t.Fatalf("exhausted lease state %+v", state)
	}
}

func TestCreateDownloadJobRestartsExpiredRunningLease(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	job := newRecoveryJob(t, db, r, now)
	claimed, err := r.ClaimDownloadJob(ctx, job.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	var request bulletinwatermark.DownloadJobRequest
	if err = db.QueryRow(`SELECT account_id::text,issue_id::text,issue_date::text,source_sha256 FROM hhc_web.bulletin_download_job WHERE id=$1`, job.ID).Scan(&request.AccountID, &request.Source.IssueID, &request.IssueDate, &request.SourceSHA256); err != nil {
		t.Fatal(err)
	}
	request.ID, request.IdempotencyKey = uuid.NewString(), uuid.NewString()
	request.Source.Series, request.Source.Locale, request.Source.Revision, request.Source.AssetID, request.Source.Filename = "general", "zh-Hant", 1, testWatermarkAssetID, "weekly.pdf"
	request.RendererVersion = "trustmark-v2"
	request.TraceCode, _ = bulletinwatermark.NewCode()
	request.FingerprintPayload, _ = bulletinwatermark.NewFingerprint()
	request.Now = now.Add(time.Minute)
	live, err := r.CreateDownloadJob(ctx, request)
	if err != nil || live.Status != "running" || live.Generation != claimed.Generation {
		t.Fatalf("live lease was restarted: %+v %v", live, err)
	}
	request.IdempotencyKey = uuid.NewString()
	request.Now = now.Add(10 * time.Minute)
	restarted, err := r.CreateDownloadJob(ctx, request)
	if err != nil || restarted.Status != "queued" || restarted.Generation != claimed.Generation+1 || restarted.Attempts != 0 {
		t.Fatalf("expired lease not restarted: %+v %v", restarted, err)
	}
	if state := readRecoveryState(t, db, job); !state.wakePending || state.leased {
		t.Fatalf("restart state %+v", state)
	}
}
