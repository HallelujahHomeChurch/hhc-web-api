package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"strings"
	"sync"
	"testing"
	"time"
)

func publishedWatermarkFixture(t *testing.T) (*sql.DB, *RecordingRepository, recordings.Recording, string, time.Time) {
	t.Helper()
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	now := time.Now().UTC().Truncate(time.Microsecond)
	actor := platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), actor, "recording-fixture")
	value, err := repo.Create(ctx, "watermark fixture", actor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	pkg := strings.ReplaceAll(platform.NewID(), "-", "")
	if err = repo.AddPackage(ctx, value.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RecordPackageReady(ctx, value.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.member_recording SET status='published' WHERE id=$1`, value.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hhc_web.recording_watermark_receipt WHERE recording_id=$1`, value.ID) })
	return db, repo, value, pkg, now
}

func TestRecordingWatermarkCollisionRetry(t *testing.T) {
	_, repo, recording, pkg, now := publishedWatermarkFixture(t)
	ctx := context.Background()
	first, err := repo.EnsureRecordingWatermark(ctx, platform.NewID(), recording.ID, pkg, now)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	if _, err = repo.ensureRecordingWatermark(ctx, platform.NewID(), recording.ID, pkg, now, func() (string, error) { attempts++; return first.Code, nil }); !errors.Is(err, recordings.ErrConflict) || attempts != 5 {
		t.Fatalf("retry bound: %d %v", attempts, err)
	}
	attempts = 0
	second, err := repo.ensureRecordingWatermark(ctx, platform.NewID(), recording.ID, pkg, now, func() (string, error) {
		attempts++
		if attempts == 1 {
			return first.Code, nil
		}
		return recordings.NewWatermarkCode()
	})
	if err != nil || attempts != 2 || second.Code == first.Code {
		t.Fatalf("collision retry: %d %v", attempts, err)
	}
}

func TestRecordingWatermarkSubjectFenceSerializesOtherConnections(t *testing.T) {
	for _, erased := range []bool{false, true} {
		t.Run(fmt.Sprint(erased), func(t *testing.T) {
			db, repo, recording, pkg, now := publishedWatermarkFixture(t)
			user, actor := platform.NewID(), platform.NewID()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			receipt, err := repo.EnsureRecordingWatermark(ctx, user, recording.ID, pkg, now)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err = lockWatermarkSubject(ctx, tx, user); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES($1,$2)`, watermarkSubjectHash(user), erased); err != nil {
				t.Fatal(err)
			}
			if erased {
				if _, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.recording_watermark_receipt WHERE user_id=$1`, user); err != nil {
					t.Fatal(err)
				}
			}
			issue, lookup := make(chan error, 1), make(chan error, 1)
			go func() { _, e := repo.EnsureRecordingWatermark(ctx, user, recording.ID, pkg, now); issue <- e }()
			go func() {
				_, e := repo.LookupRecordingWatermark(auditclient.WithProvenance(ctx, actor, "race"), actor, recording.ID, receipt.Code, now)
				lookup <- e
			}()
			// Both transactions use pool connections while blocked on the shared subject fence.
			for db.Stats().InUse < 3 && ctx.Err() == nil {
				time.Sleep(time.Millisecond)
			}
			select {
			case e := <-issue:
				t.Fatalf("issuance escaped fence: %v", e)
			default:
			}
			select {
			case e := <-lookup:
				t.Fatalf("lookup escaped fence: %v", e)
			default:
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = <-issue; !errors.Is(err, bulletinwatermark.ErrRestricted) {
				t.Fatal("late issuance", err)
			}
			if err = <-lookup; !errors.Is(err, recordings.ErrNotFound) {
				t.Fatal("late lookup", err)
			}
		})
	}
}

func TestRecordingWatermarkAuditFailureRollsBackCounter(t *testing.T) {
	db, repo, recording, pkg, now := publishedWatermarkFixture(t)
	user, actor := platform.NewID(), platform.NewID()
	receipt, err := repo.EnsureRecordingWatermark(context.Background(), user, recording.ID, pkg, now)
	if err != nil {
		t.Fatal(err)
	}
	// A scoped constraint rejects this actor's audit insert, not other test traffic.
	constraint := "trace_audit_" + strings.ReplaceAll(actor, "-", "")
	if _, err = db.Exec(`ALTER TABLE hhc_web.outbox_event ADD CONSTRAINT ` + constraint + ` CHECK (payload_json->>'actorId' IS DISTINCT FROM '` + actor + `')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`ALTER TABLE hhc_web.outbox_event DROP CONSTRAINT ` + constraint) })
	value, err := repo.LookupRecordingWatermark(auditclient.WithProvenance(context.Background(), actor, "audit-failure"), actor, recording.ID, receipt.Code, now)
	if !errors.Is(err, recordings.ErrAuditUnavailable) || value.UserID != "" {
		t.Fatalf("audit failure exposed identity: %v", err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.recording_watermark_lookup_rate WHERE subject_hash=$1`, watermarkSubjectHash(actor)).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial transaction: %d %v", count, err)
	}
}

func TestRecordingWatermarkCleanupAndMediaDeletionAreIndependent(t *testing.T) {
	db, repo, recording, pkg, now := publishedWatermarkFixture(t)
	receipt, err := repo.EnsureRecordingWatermark(context.Background(), platform.NewID(), recording.ID, pkg, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.member_recording WHERE id=$1`, recording.ID); err != nil {
		t.Fatal(err)
	}
	actor := platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), actor, "after-media-delete")
	if _, err = repo.LookupRecordingWatermark(ctx, actor, recording.ID, receipt.Code, now.Add(31*24*time.Hour)); err != nil {
		t.Fatal("media deletion removed receipt", err)
	}
	governance := New(db)
	if _, err = governance.PurgeWatermarkRecords(ctx, receipt.ExpiresAt, 1000); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.recording_watermark_receipt WHERE id=$1`, receipt.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("expiry retained receipt", err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.recording_watermark_lookup_rate WHERE subject_hash=$1`, watermarkSubjectHash(actor)).Scan(&count); err != nil || count != 0 {
		t.Fatal("two-day rate retention failed", err)
	}
}

func TestRecordingWatermarkStableConcurrentIssuance(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	user, actor := platform.NewID(), platform.NewID()
	ctx = auditclient.WithProvenance(ctx, actor, "trace-fixture-create")
	value, err := repo.Create(ctx, "trace fixture", actor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	pkg := strings.ReplaceAll(platform.NewID(), "-", "")
	if err = repo.AddPackage(ctx, value.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	value, err = repo.RecordPackageReady(ctx, value.ID, pkg, now)
	if err != nil {
		t.Fatal(err)
	}
	// Seed publication without introducing unrelated notification/audit dependencies.
	if _, err = db.Exec(`UPDATE hhc_web.member_recording SET status='published' WHERE id=$1`, value.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hhc_web.recording_watermark_receipt WHERE recording_id=$1`, value.ID) })
	var wg sync.WaitGroup
	codes := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := repo.EnsureRecordingWatermark(ctx, user, value.ID, pkg, now)
			if e != nil {
				t.Error(e)
				return
			}
			codes <- v.Code
		}()
	}
	wg.Wait()
	close(codes)
	code := ""
	for c := range codes {
		if code != "" && c != code {
			t.Fatal("concurrent code drift")
		}
		code = c
	}
	if len(code) != 10 {
		t.Fatal("missing code")
	}
	receipt, err := repo.EnsureRecordingWatermark(ctx, user, value.ID, pkg, now.Add(time.Minute))
	if err != nil || receipt.Code != code || !receipt.ExpiresAt.Equal(now.Add(365*24*time.Hour)) {
		t.Fatal("renewal changed issuance", err)
	}
	lookupCtx := auditclient.WithProvenance(ctx, actor, "trace-lookup-"+platform.NewID())
	got, err := repo.LookupRecordingWatermark(lookupCtx, actor, value.ID, code, now)
	if err != nil || got.UserID != user {
		t.Fatal("lookup", err)
	}
	if _, err = repo.LookupRecordingWatermark(lookupCtx, actor, platform.NewID(), code, now); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatal("wrong recording resolved", err)
	}
	if _, err = NewRecordingRepository(db).LookupRecordingWatermark(lookupCtx, actor, value.ID, code, now); !errors.Is(err, recordings.ErrAuditUnavailable) {
		t.Fatal("missing audit allowed identity", err)
	}
	if _, err = repo.LookupRecordingWatermark(lookupCtx, actor, value.ID, code, receipt.ExpiresAt); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatal("expired identity resolved", err)
	}
	governance := New(db)
	request := platform.NewID()
	if err = seedWatermarkRestriction(ctx, governance.db, user); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.LookupRecordingWatermark(lookupCtx, actor, value.ID, code, now); !errors.Is(err, recordings.ErrNotFound) {
		t.Fatal("restricted identity resolved", err)
	}
	if _, err = repo.EnsureRecordingWatermark(ctx, user, value.ID, pkg, now); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatal("restricted issuance allowed", err)
	}
	request = platform.NewID()
	if _, _, err = governance.CleanupWatermarkAccount(ctx, user, request); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.recording_watermark_receipt WHERE user_id=$1`, user).Scan(&count); err != nil || count != 0 {
		t.Fatal("erasure retained receipt", err)
	}
}

func TestRecordingWatermarkInvalidLookupsConsumeBudget(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	actor := platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), actor, "lookup-budget")
	now := time.Now().UTC()
	for i := 0; i < 12; i++ {
		_, err := repo.LookupRecordingWatermark(ctx, actor, platform.NewID(), "legacy88", now)
		want := recordings.ErrInvalid
		if i >= 10 {
			want = bulletinwatermark.ErrRateLimited
		}
		if !errors.Is(err, want) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	request := platform.NewID()
	if _, _, err := New(db).CleanupWatermarkAccount(ctx, actor, request); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LookupRecordingWatermark(ctx, actor, platform.NewID(), "legacy88", now); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.cms_audit_event WHERE action='recording_watermark.lookup' AND actor=$1`, actor).Scan(&rows); err != nil || rows != 0 {
		t.Fatal("erased actor trace resurrected", err)
	}
}

func TestRecordingWatermarkNonV4IDsStillConsumeBudget(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	actor := platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), actor, "invalid-id-budget")
	now := time.Now().UTC()
	ids := []string{"00000000-0000-0000-0000-000000000000", "0199a000-0000-7000-8000-000000000002", "00000000-0000-4000-0000-000000000001"}
	for i := 0; i < 12; i++ {
		value, err := repo.LookupRecordingWatermark(ctx, actor, ids[i%len(ids)], "01234ABCDE", now)
		want := recordings.ErrInvalid
		if i >= 10 {
			want = bulletinwatermark.ErrRateLimited
		}
		if !errors.Is(err, want) || value.UserID != "" {
			t.Fatalf("attempt %d: got %v want %v", i, err, want)
		}
	}
}
