package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func TestOnlineWakeIsTransactionalAndClaimable(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := platform.NewID()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = enqueueOnlineBulletinWake(ctx, tx, id, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = enqueueOnlineBulletinWake(ctx, tx, id, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	event, found, err := New(db).Claim(ctx, now.Add(time.Second), 30*time.Second)
	if err != nil || !found || event.EventType != "bulletin.online.wake" || event.AggregateVersion != 1 {
		t.Fatal(event, found, err)
	}
}

func TestLayoutAndVersionRollBackWhenWakeCannotCommit(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	var version int64
	var jobs int
	snapshot := func() (int64, int) {
		t.Helper()
		var v int64
		var n int
		if err := db.QueryRow(`SELECT version,(SELECT count(*) FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1) FROM hhc_web.online_bulletin_document WHERE id=$1`, source.DocumentID).Scan(&v, &n); err != nil {
			t.Fatal(err)
		}
		return v, n
	}
	version, jobs = snapshot()
	_, err := db.Exec(`CREATE FUNCTION hhc_web.reject_test_wake() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.event_type='bulletin.online.wake' THEN RAISE EXCEPTION 'injected wake rejection'; END IF; RETURN NEW; END$$; CREATE TRIGGER reject_test_wake BEFORE INSERT ON hhc_web.outbox_event FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_test_wake()`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := db.Exec(`DROP TRIGGER reject_test_wake ON hhc_web.outbox_event; DROP FUNCTION hhc_web.reject_test_wake()`)
		if err != nil {
			t.Error(err)
		}
	})
	if _, err = repo.QueueOnlineBulletinLayout(context.Background(), source.DocumentID, version, now.Add(time.Second)); err == nil {
		t.Fatal("layout committed without its wake")
	}
	v, n := snapshot()
	if v != version || n != jobs {
		t.Fatalf("partial commit version %d/%d jobs %d/%d", v, version, n, jobs)
	}
}

func TestExtractionFailureRequiresManualRetryAndExpiredLeaseCanRecover(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	id := seedOnlineExtraction(t, db, now)
	repo := New(db)
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 3)
	if err != nil || job == nil || job.ID != id {
		t.Fatal(job, err)
	}
	if err = repo.FailOnlineBulletinExtraction(ctx, *job, "unsupported_source", now.Add(time.Second), 3); err != nil {
		t.Fatal(err)
	}
	var status string
	if err = db.QueryRow(`SELECT status FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, id).Scan(&status); err != nil || status != "failed" {
		t.Fatal(status, err)
	}
	if pending, err := repo.NextOnlineBulletinWork(ctx); err != nil || !pending.IsZero() {
		t.Fatal("terminal extraction was automatically retried", pending, err)
	}
	if next, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(time.Minute), 120*time.Second, 3); err != nil || next != nil {
		t.Fatal("failed job reclaimed", next, err)
	}
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 1, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64), Retry: true}
	retried, err := repo.QueueOnlineBulletinExtraction(ctx, job.Canonical.IssueID, input, "admin", now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var wakes int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.online.wake' AND status='pending'`, retried.Job.ID).Scan(&wakes); err != nil || wakes != 1 {
		t.Fatal(wakes, err)
	}
	first, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(2*time.Second), 120*time.Second, 3)
	if err != nil || first == nil {
		t.Fatal(first, err)
	}
	if next, err := repo.NextOnlineBulletinWork(ctx); err != nil || !next.Equal(first.LeaseUntil) {
		t.Fatal("unexpired lease loses its wake", next, err)
	}
	if next, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(time.Minute), 120*time.Second, 3); err != nil || next != nil {
		t.Fatal("live lease reclaimed", next, err)
	}
	second, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(123*time.Second), 120*time.Second, 3)
	if err != nil || second == nil || second.ID != first.ID || second.LeaseToken == first.LeaseToken {
		t.Fatal("crashed execution not recovered", second, err)
	}
	if err = repo.FailOnlineBulletinExtraction(ctx, *first, "stale_worker", now.Add(124*time.Second), 3); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatal("old execution overwrote new lease", err)
	}
}

// Use the real queueing entrypoint to cover conversion/layout producers too.
func TestAllExtractorWorkKindsHaveDurableWakes(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedSimplifiedSource(t, db, now)
	queued, err := repo.QueueOnlineBulletinConversion(context.Background(), source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	var conversions, layouts int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.online.wake'`, queued.Job.ID).Scan(&conversions); err != nil || conversions < 1 {
		t.Fatal(conversions, err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event e JOIN hhc_web.online_bulletin_layout_job j ON j.id=e.aggregate_id WHERE j.document_id=$1 AND e.event_type='bulletin.online.wake'`, source.DocumentID).Scan(&layouts); err != nil || layouts < 1 {
		t.Fatal(layouts, err)
	}
}
