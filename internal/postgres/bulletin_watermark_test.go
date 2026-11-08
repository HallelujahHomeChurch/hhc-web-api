package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/google/uuid"
)

const testWatermarkAssetID = "1ce9519008dc4f25966b0baffb62e186"

func TestWatermarkQueueEventIncludesAggregateVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	id := uuid.NewString()
	mock.ExpectBegin()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(`INSERT INTO hhc_web.outbox_event\(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version`).
		WithArgs(sqlmock.AnyArg(), id, 2, sqlmock.AnyArg(), "bulletin-watermark:download:"+id+":2", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err = insertWatermarkQueueEvent(context.Background(), tx, "download", id, 2, now); err != nil {
		t.Fatal(err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWatermarkReceiptLifecycle(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	now := time.Now().UTC()
	user := uuid.NewString()
	code, _ := bulletinwatermark.NewCode()
	rec := bulletinwatermark.Receipt{ID: uuid.NewString(), Code: code, UserID: user, IssueID: uuid.NewString(), Locale: "zh-Hant", Revision: 1, SourceAssetID: testWatermarkAssetID, SourceSHA256: stringHash(), OutputSHA256: stringHash(), RendererVersion: "test", PrivacyVersion: "test", IssuedAt: now, ExpiresAt: now.Add(bulletinwatermark.Retention)}
	if err = r.InsertWatermarkReceipt(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := r.LookupWatermarkReceipt(ctx, code, uuid.NewString(), now)
	if err != nil || got.UserID != user {
		t.Fatalf("lookup: %v", err)
	}
	if _, err = r.LookupWatermarkReceipt(ctx, code, uuid.NewString(), rec.ExpiresAt); !errors.Is(err, bulletinwatermark.ErrNotFound) {
		t.Fatalf("expiry: %v", err)
	}
	if err = seedWatermarkRestriction(ctx, r.db, user); err != nil {
		t.Fatal(err)
	}
	if got, lookupErr := r.LookupWatermarkReceipt(ctx, code, uuid.NewString(), now); !errors.Is(lookupErr, bulletinwatermark.ErrNotFound) || got.UserID != "" {
		t.Fatalf("restricted subject disclosed to another investigator: %+v %v", got, lookupErr)
	}
	rec.ID = uuid.NewString()
	rec.Code, _ = bulletinwatermark.NewCode()
	if err = r.InsertWatermarkReceipt(ctx, rec); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatalf("restriction: %v", err)
	}
	if _, _, err = r.CleanupWatermarkAccount(ctx, user, "erase:"+user); err != nil {
		t.Fatal(err)
	}
	if _, err = r.LookupWatermarkReceipt(ctx, code, uuid.NewString(), now); !errors.Is(err, bulletinwatermark.ErrNotFound) {
		t.Fatalf("erasure: %v", err)
	}
	if err = r.InsertWatermarkReceipt(ctx, rec); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatalf("post-erasure insertion: %v", err)
	}
	var remaining int
	if err = db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM hhc_web.bulletin_watermark_receipt WHERE user_id=$1)+
		(SELECT count(*) FROM hhc_web.bulletin_watermark_investigation WHERE actor_id=$1)+
		(SELECT count(*) FROM hhc_web.bulletin_download_job WHERE account_id=$1)`, user).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("postcondition remaining=%d err=%v", remaining, err)
	}
	rec.UserID = uuid.NewString()
	rec.IssuedAt = now.Add(-bulletinwatermark.Retention)
	rec.ExpiresAt = now
	if err = r.InsertWatermarkReceipt(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, err = r.PurgeWatermarkRecords(ctx, now, 100); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.bulletin_watermark_receipt WHERE id=$1`, rec.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("purge: %d %v", count, err)
	}
}

func TestWatermarkAccountCleanupWaitsForAssetDeleteAcknowledgement(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	userID := uuid.NewString()
	issueID := uuid.NewString()
	jobID := uuid.NewString()
	assetID := "2ce9519008dc4f25966b0baffb62e186"
	var issueNumber int
	var issueDate string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(issue_number),0)+1,(COALESCE(max(issue_date), CURRENT_DATE)+1)::text FROM hhc_web.bulletin_issue`).Scan(&issueNumber, &issueDate); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,'published',1,$4,$5,$5,$6,$6)`, issueID, issueNumber, issueDate, "cleanup:"+issueID, userID, now); err != nil {
		t.Fatal(err)
	}
	job, err := r.CreateDownloadJob(ctx, bulletinwatermark.DownloadJobRequest{
		ID: jobID, IdempotencyKey: uuid.NewString(), AccountID: userID,
		Source:    bulletinwatermark.Source{IssueID: issueID, AssetID: testWatermarkAssetID, Locale: "zh-Hant", Revision: 1, Filename: "weekly.pdf"},
		IssueDate: issueDate, SourceSHA256: stringHash(), RendererVersion: "test",
		FingerprintPayload: strings.Repeat("0", 61), TraceCode: "TEST-CLEANUP", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err = r.ClaimDownloadJob(ctx, job.ID, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	code, _ := bulletinwatermark.NewCode()
	receipt := bulletinwatermark.Receipt{ID: uuid.NewString(), Code: code, UserID: userID, IssueID: issueID, Locale: "zh-Hant", Revision: 1, SourceAssetID: testWatermarkAssetID, SourceSHA256: stringHash(), OutputSHA256: strings.Repeat("a", 64), RendererVersion: "test", PrivacyVersion: "test", IssuedAt: now, ExpiresAt: now.Add(bulletinwatermark.Retention)}
	if err = r.CompleteDownloadJob(ctx, job.ID, job.LeaseToken, assetID, receipt, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	idempotencyKey := "force-delete-cleanup-" + uuid.NewString()
	recordCount, remaining, err := r.CleanupWatermarkAccount(ctx, userID, idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if recordCount != 2 || remaining == 0 {
		t.Fatalf("cleanup completed before asset delete acknowledgement: count=%d remaining=%d", recordCount, remaining)
	}

	var eventID, aggregateID, outboxKey, status string
	if err = db.QueryRowContext(ctx, `SELECT id::text,aggregate_id::text,idempotency_key,status FROM hhc_web.outbox_event WHERE destination='asset-api' AND event_type='asset.owner.delete' AND payload_json->>'assetId'=$1`, assetID).Scan(&eventID, &aggregateID, &outboxKey, &status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || strings.Contains(aggregateID, userID) || strings.Contains(outboxKey, userID) {
		t.Fatalf("unsafe cleanup outbox identity: aggregate=%q key=%q status=%q user=%q", aggregateID, outboxKey, status, userID)
	}
	if _, err = uuid.Parse(aggregateID); err != nil {
		t.Fatalf("cleanup aggregate is not a durable opaque UUID: %q", aggregateID)
	}
	if _, err = db.ExecContext(ctx, `UPDATE hhc_web.account_cleanup_operation SET remaining_count=0 WHERE idempotency_key=$1`, idempotencyKey); err != nil {
		t.Fatal(err)
	}
	staleCount, staleRemaining, err := r.CleanupWatermarkAccount(ctx, userID, idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if staleCount != recordCount || staleRemaining == 0 {
		t.Fatalf("stale completion cache bypassed asset postcondition: count=%d remaining=%d", staleCount, staleRemaining)
	}
	if err = r.Complete(ctx, eventID, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	retryCount, retryRemaining, err := r.CleanupWatermarkAccount(ctx, userID, idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if retryCount != recordCount || retryRemaining != 0 {
		t.Fatalf("cleanup retry did not observe acknowledgement: first=%d retry=%d remaining=%d", recordCount, retryCount, retryRemaining)
	}
	var legacyActions int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.bulletin_watermark_dsr_action WHERE subject_hash=$1`, watermarkSubjectHash(userID)).Scan(&legacyActions); err != nil || legacyActions != 0 {
		t.Fatalf("ordinary cleanup created DSR state=%d err=%v", legacyActions, err)
	}
}
func stringHash() string { return "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" }

func TestFindWatermarkReceiptByExactKeys(t *testing.T) {
	now := time.Now().UTC()
	payload, _ := bulletinwatermark.NewFingerprint()
	code, _ := bulletinwatermark.NewCode()
	rec := bulletinwatermark.Receipt{ID: uuid.NewString(), Code: code, UserID: uuid.NewString(), IssueID: uuid.NewString(), Series: "general", Locale: "zh-Hant", Revision: 2, SourceAssetID: testWatermarkAssetID, SourceSHA256: stringHash(), OutputSHA256: strings.Repeat("a", 64), RendererVersion: "exact-test", PrivacyVersion: "test", IssuedAt: now, ExpiresAt: now.Add(time.Hour), FingerprintPayload: payload, ModelSHA256: stringHash(), Pages: []bulletinwatermark.PageMetadata{{Page: 1, Width: 100, Height: 200, SourceSHA256: stringHash(), OutputSHA256: stringHash()}}}
	pages, _ := json.Marshal(rec.Pages)
	rows := func(receipts ...bulletinwatermark.Receipt) *sqlmock.Rows {
		result := sqlmock.NewRows([]string{"id", "code", "user", "issue", "series", "locale", "revision", "asset", "source", "output", "renderer", "privacy", "issued", "expires", "fingerprint", "model", "pages"})
		for _, value := range receipts {
			result.AddRow(value.ID, value.Code, value.UserID, value.IssueID, value.Series, value.Locale, value.Revision, value.SourceAssetID, value.SourceSHA256, value.OutputSHA256, value.RendererVersion, value.PrivacyVersion, value.IssuedAt, value.ExpiresAt, value.FingerprintPayload, value.ModelSHA256, pages)
		}
		return result
	}
	for _, test := range []struct {
		name, column, key string
		lookup            func(*Repository) (bulletinwatermark.Receipt, error)
	}{
		{name: "output sha", column: "output_sha256", key: rec.OutputSHA256, lookup: func(r *Repository) (bulletinwatermark.Receipt, error) {
			return r.FindWatermarkReceiptByOutputSHA256(context.Background(), rec.OutputSHA256, now)
		}},
		{name: "payload", column: "fingerprint_payload", key: rec.FingerprintPayload, lookup: func(r *Repository) (bulletinwatermark.Receipt, error) {
			return r.FindWatermarkReceiptByPayload(context.Background(), rec.FingerprintPayload, now)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectQuery("SELECT user_id::text .*"+test.column).WithArgs(test.key, now).WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(rec.UserID))
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(watermarkSubjectHash(rec.UserID)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT .*"+test.column).WithArgs(test.key, now).WillReturnRows(rows(rec))
			mock.ExpectCommit()
			got, err := test.lookup(New(db))
			if err != nil || !reflect.DeepEqual(got, rec) {
				t.Fatalf("receipt=%+v err=%v", got, err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("duplicate exact key is an integrity error", func(t *testing.T) {
		other := rec
		other.ID, other.UserID = uuid.NewString(), uuid.NewString()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectBegin()
		subjects := []string{rec.UserID, other.UserID}
		slices.Sort(subjects)
		discovery := sqlmock.NewRows([]string{"user_id"})
		for _, subject := range subjects {
			discovery.AddRow(subject)
		}
		mock.ExpectQuery("SELECT user_id::text .*output_sha256").WithArgs(rec.OutputSHA256, now).WillReturnRows(discovery)
		for _, subject := range subjects {
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(watermarkSubjectHash(subject)).WillReturnResult(sqlmock.NewResult(0, 1))
		}
		mock.ExpectQuery("SELECT .*output_sha256").WithArgs(rec.OutputSHA256, now).WillReturnRows(rows(rec, other))
		mock.ExpectRollback()
		if _, err = New(db).FindWatermarkReceiptByOutputSHA256(context.Background(), rec.OutputSHA256, now); !errors.Is(err, bulletinwatermark.ErrReceiptIntegrity) {
			t.Fatal(err)
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("restricted receipt is indistinguishable from missing", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT user_id::text .*fingerprint_payload").WithArgs(rec.FingerprintPayload, now).WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow(rec.UserID))
		mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(watermarkSubjectHash(rec.UserID)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery("SELECT .*fingerprint_payload").WithArgs(rec.FingerprintPayload, now).WillReturnRows(rows())
		mock.ExpectRollback()
		got, err := New(db).FindWatermarkReceiptByPayload(context.Background(), rec.FingerprintPayload, now)
		if !errors.Is(err, bulletinwatermark.ErrNotFound) || !reflect.DeepEqual(got, bulletinwatermark.Receipt{}) {
			t.Fatalf("receipt=%+v err=%v", got, err)
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestWatermarkCleanupRateAndLookupActorFence(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx := context.Background()
	actor := uuid.NewString()
	now := time.Now().UTC()
	code, _ := bulletinwatermark.NewCode()
	for range 10 {
		if _, err := r.LookupWatermarkReceipt(ctx, code, actor, now); !errors.Is(err, bulletinwatermark.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if _, err := r.LookupWatermarkReceipt(ctx, code, actor, now); !errors.Is(err, bulletinwatermark.ErrRateLimited) {
		t.Fatalf("rate: %v", err)
	}
	if _, _, err := r.CleanupWatermarkAccount(ctx, actor, "erase:"+actor); err != nil {
		t.Fatal(err)
	}
	if _, err := r.LookupWatermarkReceipt(ctx, code, actor, now.Add(time.Minute)); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatalf("erased actor lookup: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.cms_audit_event WHERE action='bulletin_watermark.lookup' AND actor=$1`, actor).Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit resurrection %d %v", count, err)
	}
	if _, _, err := r.CleanupWatermarkAccount(ctx, actor, "erase:"+actor); err != nil {
		t.Fatal("idempotent retry", err)
	}
}

func TestWatermarkLookupWaitsForSubjectRestriction(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	now := time.Now().UTC()
	user, actor := uuid.NewString(), uuid.NewString()
	code, err := bulletinwatermark.NewCode()
	if err != nil {
		t.Fatal(err)
	}
	rec := bulletinwatermark.Receipt{ID: uuid.NewString(), Code: code, UserID: user, IssueID: uuid.NewString(), Locale: "zh-Hant", Revision: 1, SourceAssetID: testWatermarkAssetID, SourceSHA256: stringHash(), OutputSHA256: stringHash(), RendererVersion: "test", PrivacyVersion: "test", IssuedAt: now, ExpiresAt: now.Add(bulletinwatermark.Retention)}
	if err := r.InsertWatermarkReceipt(ctx, rec); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var blocker int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if err := lockWatermarkSubject(ctx, tx, user); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES($1,false)`, watermarkSubjectHash(user)); err != nil {
		t.Fatal(err)
	}
	type result struct {
		receipt bulletinwatermark.Receipt
		err     error
	}
	done := make(chan result, 1)
	go func() {
		v, err := r.LookupWatermarkReceipt(ctx, code, actor, now)
		done <- result{v, err}
	}()
	// Observe actual database contention rather than assume goroutine scheduling.
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case got := <-done:
			t.Fatalf("lookup bypassed subject fence: %v", got.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if !errors.Is(got.err, bulletinwatermark.ErrNotFound) || !reflect.DeepEqual(got.receipt, bulletinwatermark.Receipt{}) {
		t.Fatalf("restricted subject disclosure: %v", got.err)
	}
	var matched bool
	var resource string
	if err := db.QueryRowContext(ctx, `SELECT (payload_json->>'matched')::boolean,resource_id::text FROM hhc_web.cms_audit_event WHERE action='bulletin_watermark.lookup' AND actor=$1`, actor).Scan(&matched, &resource); err != nil || matched || resource == rec.ID {
		t.Fatalf("denial audit leaked target: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count FROM hhc_web.bulletin_watermark_lookup_rate WHERE subject_hash=$1 AND window_start=$2`, watermarkSubjectHash(actor), now.Truncate(time.Minute)).Scan(&count); err != nil || count != 1 {
		t.Fatalf("denial rate count: %d %v", count, err)
	}
}

func TestFingerprintReceiptStorageAndCandidateBoundaries(t *testing.T) {
	now := time.Now().UTC()
	payload, _ := bulletinwatermark.NewFingerprint()
	code, _ := bulletinwatermark.NewCode()
	rec := bulletinwatermark.Receipt{ID: uuid.NewString(), Code: code, UserID: uuid.NewString(), IssueID: uuid.NewString(), Series: "general", Locale: "zh-Hant", Revision: 2, SourceAssetID: testWatermarkAssetID, SourceSHA256: stringHash(), OutputSHA256: stringHash(), RendererVersion: "new-pdf", PrivacyVersion: "test", IssuedAt: now, ExpiresAt: now.Add(bulletinwatermark.Retention), FingerprintPayload: payload, ModelSHA256: stringHash(), Pages: []bulletinwatermark.PageMetadata{{Page: 1, Width: 100, Height: 200, SourceSHA256: stringHash(), OutputSHA256: stringHash()}}}
	pages, _ := json.Marshal(rec.Pages)
	scope := bulletinwatermark.CandidateScope{IssueID: rec.IssueID, Series: rec.Series, Locale: rec.Locale, Revision: rec.Revision, SourceAssetID: rec.SourceAssetID, SourceSHA256: rec.SourceSHA256, RendererVersion: rec.RendererVersion, ModelSHA256: rec.ModelSHA256}
	newRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"id", "code", "user", "issue", "series", "locale", "revision", "asset", "source", "output", "renderer", "privacy", "issued", "expires", "fingerprint", "model", "pages"})
	}
	add := func(rows *sqlmock.Rows) *sqlmock.Rows {
		return rows.AddRow(rec.ID, rec.Code, rec.UserID, rec.IssueID, rec.Series, rec.Locale, rec.Revision, rec.SourceAssetID, rec.SourceSHA256, rec.OutputSHA256, rec.RendererVersion, rec.PrivacyVersion, rec.IssuedAt, rec.ExpiresAt, rec.FingerprintPayload, rec.ModelSHA256, pages)
	}
	t.Run("insert and fingerprint collision", func(t *testing.T) {
		for _, collision := range []bool{false, true} {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(fmt.Sprintf("%s:%d:%s", uuid.MustParse(rec.SourceAssetID).String(), rec.Revision, rec.RendererVersion)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(watermarkSubjectHash(rec.UserID)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("SELECT EXISTS").WithArgs(watermarkSubjectHash(rec.UserID)).WillReturnRows(sqlmock.NewRows([]string{"blocked"}).AddRow(false))
			expectation := mock.ExpectExec("INSERT INTO hhc_web.bulletin_watermark_receipt").WithArgs(rec.ID, rec.Code, rec.UserID, rec.IssueID, rec.Series, rec.Locale, rec.Revision, rec.SourceAssetID, rec.SourceSHA256, rec.OutputSHA256, rec.RendererVersion, rec.PrivacyVersion, rec.IssuedAt, rec.ExpiresAt, rec.FingerprintPayload, rec.ModelSHA256, string(pages))
			if collision {
				expectation.WillReturnError(&pgconn.PgError{Code: "23505", ConstraintName: "bulletin_watermark_receipt_fingerprint_key"})
				mock.ExpectRollback()
			} else {
				expectation.WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectCommit()
			}
			err = New(db).InsertWatermarkReceipt(context.Background(), rec)
			if collision && !errors.Is(err, bulletinwatermark.ErrCodeCollision) || !collision && err != nil {
				t.Fatal(err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("complete set or fail whole", func(t *testing.T) {
		for _, count := range []int{0, 2, 3} {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			rows := newRows()
			for range count {
				add(rows)
			}
			mock.ExpectQuery(`(?s)SELECT .* WHERE issue_id=\$1 AND series=\$2 AND locale=\$3 AND revision=\$4 AND source_asset_id=\$5 AND source_sha256=\$6 AND renderer_version=\$7 AND model_sha256=\$8.*expires_at>\$9.*NOT EXISTS .*bulletin_watermark_subject.*ORDER BY id LIMIT \$10`).WithArgs(scope.IssueID, scope.Series, scope.Locale, scope.Revision, scope.SourceAssetID, scope.SourceSHA256, scope.RendererVersion, scope.ModelSHA256, now, 3).WillReturnRows(rows)
			got, err := New(db).ListWatermarkCandidates(context.Background(), scope, now, 2)
			if count > 2 {
				if !errors.Is(err, bulletinwatermark.ErrCandidateLimit) || got != nil {
					t.Fatal("partial candidate set released")
				}
			} else if err != nil || len(got) != count {
				t.Fatal(err)
			} else if count > 0 && !reflect.DeepEqual(got[0], rec) {
				t.Fatal("reconstruction metadata not retained")
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("legacy and malformed metadata scan", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		legacy := newRows().AddRow(rec.ID, rec.Code, rec.UserID, rec.IssueID, rec.Series, rec.Locale, rec.Revision, rec.SourceAssetID, rec.SourceSHA256, rec.OutputSHA256, rec.RendererVersion, rec.PrivacyVersion, rec.IssuedAt, rec.ExpiresAt, "", "", nil)
		mock.ExpectQuery("legacy").WillReturnRows(legacy)
		got, err := scanWatermark(db.QueryRow("legacy"))
		if err != nil || got.FingerprintPayload != "" || got.Pages != nil {
			t.Fatal("legacy incompatible", err)
		}
		bad := newRows().AddRow(rec.ID, rec.Code, rec.UserID, rec.IssueID, rec.Series, rec.Locale, rec.Revision, rec.SourceAssetID, rec.SourceSHA256, rec.OutputSHA256, rec.RendererVersion, rec.PrivacyVersion, rec.IssuedAt, rec.ExpiresAt, payload, rec.ModelSHA256, []byte(`[]`))
		mock.ExpectQuery("malformed").WillReturnRows(bad)
		if _, err = scanWatermark(db.QueryRow("malformed")); !errors.Is(err, bulletinwatermark.ErrInvalidFingerprint) {
			t.Fatal("malformed metadata accepted", err)
		}
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestFingerprintReceiptDatabase(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	r := New(db)
	now := time.Now().UTC().Truncate(time.Microsecond)
	base := bulletinwatermark.Receipt{IssueID: uuid.NewString(), Series: "general", Locale: "zh-Hant", Revision: 1, SourceAssetID: testWatermarkAssetID, SourceSHA256: stringHash(), OutputSHA256: stringHash(), RendererVersion: "fingerprint-database-test", ModelSHA256: stringHash(), PrivacyVersion: "test", IssuedAt: now, ExpiresAt: now.Add(bulletinwatermark.Retention), Pages: []bulletinwatermark.PageMetadata{{Page: 1, Width: 200, Height: 300, SourceSHA256: stringHash(), OutputSHA256: stringHash()}}}
	insert := func(change func(*bulletinwatermark.Receipt)) bulletinwatermark.Receipt {
		t.Helper()
		v := base
		v.ID = uuid.NewString()
		v.UserID = uuid.NewString()
		v.Code, _ = bulletinwatermark.NewCode()
		v.FingerprintPayload, _ = bulletinwatermark.NewFingerprint()
		if change != nil {
			change(&v)
		}
		if err := r.InsertWatermarkReceipt(ctx, v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	a := insert(nil)
	insert(nil)
	restricted := insert(nil)
	if err = seedWatermarkRestriction(ctx, r.db, restricted.UserID); err != nil {
		t.Fatal(err)
	}
	insert(func(v *bulletinwatermark.Receipt) {
		v.IssuedAt = now.Add(-bulletinwatermark.Retention)
		v.ExpiresAt = now
	})
	insert(func(v *bulletinwatermark.Receipt) { v.ModelSHA256 = strings.Repeat("c", 64) })
	insert(func(v *bulletinwatermark.Receipt) { v.SourceSHA256 = strings.Repeat("c", 64) })
	scope := bulletinwatermark.CandidateScope{IssueID: base.IssueID, Locale: base.Locale, Revision: base.Revision, SourceAssetID: base.SourceAssetID, SourceSHA256: base.SourceSHA256, RendererVersion: base.RendererVersion, ModelSHA256: base.ModelSHA256}
	got, err := r.ListWatermarkCandidates(ctx, scope, now, 2)
	if err != nil || len(got) != 2 {
		t.Fatalf("eligible candidate selection: count=%d error=%v", len(got), err)
	}
	if partial, err := r.ListWatermarkCandidates(ctx, scope, now, 1); !errors.Is(err, bulletinwatermark.ErrCandidateLimit) || partial != nil {
		t.Fatal("candidate overflow did not fail whole")
	}
	duplicate := a
	duplicate.ID = uuid.NewString()
	duplicate.Code, _ = bulletinwatermark.NewCode()
	if err = r.InsertWatermarkReceipt(ctx, duplicate); !errors.Is(err, bulletinwatermark.ErrCodeCollision) {
		t.Fatal("fingerprint collision not retriable", err)
	}
	for _, update := range []string{
		`fingerprint_payload='0'`, `model_sha256='bad'`, `pages_json='[]'::jsonb`, `pages_json=NULL`, `pages_json='{}'::jsonb`,
		`pages_json=(SELECT jsonb_agg('{}'::jsonb) FROM generate_series(1,61))`,
	} {
		if _, err = db.ExecContext(ctx, `UPDATE hhc_web.bulletin_watermark_receipt SET `+update+` WHERE id=$1`, a.ID); err == nil {
			t.Fatal("database accepted invalid metadata")
		}
	}
	if _, _, err = r.CleanupWatermarkAccount(ctx, a.UserID, "fingerprint-erase:"+a.UserID); err != nil {
		t.Fatal(err)
	}
	got, err = r.ListWatermarkCandidates(ctx, scope, now, 2)
	if err != nil || len(got) != 1 {
		t.Fatal("erased receipt still candidate", err)
	}
}
