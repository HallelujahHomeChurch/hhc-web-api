package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
)

func TestDownloadJobRegeneratesAfterPublishedRevisionChanges(t *testing.T) {
	for _, status := range []string{"queued", "running", "ready"} {
		t.Run(status, func(t *testing.T) {
			db := accessDB(t)
			r := New(db)
			ctx := context.Background()
			now := time.Now().UTC()
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
			first, err := r.CreateDownloadJob(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			receiptFor := func(job bulletinwatermark.DownloadJob) bulletinwatermark.Receipt {
				return bulletinwatermark.Receipt{ID: uuid.NewString(), Code: job.TraceCode, UserID: job.AccountID, IssueID: job.IssueID, Series: job.Series, Locale: job.Locale, Revision: job.Revision, SourceAssetID: job.SourceAssetID, SourceSHA256: job.SourceSHA256, OutputSHA256: stringHash(), RendererVersion: "trustmark-pdf-v2", PrivacyVersion: "test", IssuedAt: now, ExpiresAt: now.Add(bulletinwatermark.Retention), FingerprintPayload: job.FingerprintPayload, ModelSHA256: stringHash(), Pages: []bulletinwatermark.PageMetadata{{Page: 1, Width: 100, Height: 100, SourceSHA256: job.SourceSHA256, OutputSHA256: stringHash()}}}
			}
			var oldReceipt bulletinwatermark.Receipt
			if status != "queued" {
				first, err = r.ClaimDownloadJob(ctx, first.ID, now)
				if err != nil {
					t.Fatal(err)
				}
				oldReceipt = receiptFor(first)
				if status == "ready" {
					if err := r.CompleteDownloadJob(ctx, first.ID, first.LeaseToken, strings.Repeat("a", 32), oldReceipt, now); err != nil {
						t.Fatal(err)
					}
				}
			}
			unchanged := request
			unchanged.ID, unchanged.IdempotencyKey = uuid.NewString(), uuid.NewString()
			cached, err := r.CreateDownloadJob(ctx, unchanged)
			if err != nil || cached.ID != first.ID || cached.Generation != first.Generation || cached.Status != status {
				t.Fatalf("unchanged source lost reuse: generation=%d status=%s err=%v", cached.Generation, cached.Status, err)
			}
			request.Source.Revision++
			request.Source.Filename = "weekly-republished.pdf"
			if _, err := r.CreateDownloadJob(ctx, request); !errors.Is(err, bulletinwatermark.ErrDownloadConflict) {
				t.Fatalf("changed source with the same idempotency key: %v", err)
			}
			request.ID, request.IdempotencyKey = uuid.NewString(), uuid.NewString()
			request.TraceCode, _ = bulletinwatermark.NewCode()
			request.FingerprintPayload, _ = bulletinwatermark.NewFingerprint()
			second, err := r.CreateDownloadJob(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if second.ID != first.ID || second.Revision != request.Source.Revision || second.Filename != request.Source.Filename || second.Generation != first.Generation+1 || second.Status != "queued" {
				t.Fatalf("reused stale download job: revision=%d generation=%d status=%s filename=%s", second.Revision, second.Generation, second.Status, second.Filename)
			}
			if second.FingerprintPayload == first.FingerprintPayload || second.TraceCode == first.TraceCode || second.LeaseToken != "" || second.DerivedAssetID != "" || second.ReceiptID != "" || second.Attempts != 0 {
				t.Fatal("regeneration retained old issuance or worker state")
			}
			replay, err := r.CreateDownloadJob(ctx, request)
			if err != nil || replay.Generation != second.Generation {
				t.Fatalf("retry regenerated the job again: generation=%d err=%v", replay.Generation, err)
			}
			claimed, err := r.ClaimDownloadJob(ctx, second.ID, now)
			if err != nil {
				t.Fatal(err)
			}
			if status == "running" {
				if err := r.CompleteDownloadJob(ctx, first.ID, first.LeaseToken, strings.Repeat("a", 32), oldReceipt, now); !errors.Is(err, bulletinwatermark.ErrDownloadLease) {
					t.Fatalf("superseded worker completion: %v", err)
				}
			}
			if err := r.CompleteDownloadJob(ctx, claimed.ID, claimed.LeaseToken, strings.Repeat("b", 32), receiptFor(claimed), now); err != nil {
				t.Fatal(err)
			}
			ready, err := r.GetDownloadJob(ctx, second.ID, request.AccountID, now)
			if err != nil || !ready.Ready(now) || ready.Revision != request.Source.Revision || ready.Filename != request.Source.Filename {
				t.Fatalf("current revision cannot be downloaded: ready=%t revision=%d err=%v", ready.Ready(now), ready.Revision, err)
			}
			var receipts, deletes, enqueues int
			if err := db.QueryRow(`SELECT count(*) FROM hhc_web.bulletin_watermark_receipt WHERE user_id=$1`, request.AccountID).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='asset.owner.delete'`, first.ID).Scan(&deletes); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.watermark.enqueue'`, first.ID).Scan(&enqueues); err != nil {
				t.Fatal(err)
			}
			wantReceipts, wantDeletes := 1, 0
			if status == "ready" {
				wantReceipts, wantDeletes = 2, 1
			}
			if receipts != wantReceipts || deletes != wantDeletes || enqueues != 2 {
				t.Fatalf("receipts=%d deletes=%d enqueues=%d", receipts, deletes, enqueues)
			}
		})
	}
}

func TestDownloadJobCanCompleteAgainAfterCacheExpiry(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	request := bulletinwatermark.DownloadJobRequest{
		ID: uuid.NewString(), IdempotencyKey: uuid.NewString(), AccountID: uuid.NewString(),
		Source:    bulletinwatermark.Source{IssueID: uuid.NewString(), Series: "general", Locale: "zh-Hant", Revision: 1, AssetID: testWatermarkAssetID, Filename: "weekly.pdf"},
		IssueDate: "2026-09-13", SourceSHA256: stringHash(), RendererVersion: "trustmark-v2", Now: now,
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
	complete := func(job bulletinwatermark.DownloadJob, at time.Time) {
		t.Helper()
		claimed, err := r.ClaimDownloadJob(ctx, job.ID, at)
		if err != nil {
			t.Fatal(err)
		}
		receipt := bulletinwatermark.Receipt{ID: uuid.NewString(), Code: claimed.TraceCode, UserID: claimed.AccountID, IssueID: claimed.IssueID, Series: claimed.Series, Locale: claimed.Locale, Revision: claimed.Revision, SourceAssetID: claimed.SourceAssetID, SourceSHA256: claimed.SourceSHA256, OutputSHA256: stringHash(), RendererVersion: "trustmark-pdf-v2", PrivacyVersion: "test", IssuedAt: at, ExpiresAt: at.Add(bulletinwatermark.Retention), FingerprintPayload: claimed.FingerprintPayload, ModelSHA256: stringHash(), Pages: []bulletinwatermark.PageMetadata{{Page: 1, Width: 100, Height: 100, SourceSHA256: stringHash(), OutputSHA256: stringHash()}}}
		assetID := strings.Repeat("a", 32)
		if job.Generation > 1 {
			assetID = strings.Repeat("b", 32)
		}
		if err := r.CompleteDownloadJob(ctx, job.ID, claimed.LeaseToken, assetID, receipt, at); err != nil {
			t.Fatal(err)
		}
		ready, err := r.GetDownloadJob(ctx, job.ID, job.AccountID, at)
		if err != nil || !ready.Ready(at) {
			t.Fatalf("not ready: %+v %v", ready, err)
		}
	}
	first, err := r.CreateDownloadJob(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	complete(first, now)
	request.ID, request.IdempotencyKey = uuid.NewString(), uuid.NewString()
	request.TraceCode, _ = bulletinwatermark.NewCode()
	request.FingerprintPayload, _ = bulletinwatermark.NewFingerprint()
	request.Now = now.Add(bulletinwatermark.DownloadCacheTTL)
	second, err := r.CreateDownloadJob(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Generation != 2 || second.TraceCode == first.TraceCode || second.FingerprintPayload == first.FingerprintPayload {
		t.Fatalf("regeneration reused issuance identity: %+v", second)
	}
	complete(second, request.Now)
	var deletes int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='asset.owner.delete' AND payload_json->>'assetId'=$2`, first.ID, strings.Repeat("a", 32)).Scan(&deletes); err != nil || deletes != 1 {
		t.Fatalf("expired asset deletion events=%d err=%v", deletes, err)
	}
	var receipts int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.bulletin_watermark_receipt WHERE user_id=$1`, request.AccountID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatalf("receipts=%d err=%v", receipts, err)
	}
	_, remaining, err := r.CleanupWatermarkAccount(ctx, request.AccountID, "renewal-cleanup:"+request.AccountID)
	if err != nil || remaining != 2 {
		t.Fatalf("pending old and current asset cleanup remaining=%d err=%v", remaining, err)
	}
}
