package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

const downloadJobColumns = `id::text,idempotency_key::text,account_id::text,issue_id::text,issue_date::text,series,locale,revision,source_asset_id,source_sha256,filename,renderer_version,fingerprint_payload,trace_code,status,generation,progress,COALESCE(derived_asset_id,''),COALESCE(receipt_id::text,''),COALESCE(error_code,''),COALESCE(lease_token::text,''),lease_until,attempts,cache_expires_at,created_at,updated_at,expires_at`

func scanDownloadJob(row interface{ Scan(...any) error }) (j bulletinwatermark.DownloadJob, err error) {
	var lease, cache sql.NullTime
	err = row.Scan(&j.ID, &j.IdempotencyKey, &j.AccountID, &j.IssueID, &j.IssueDate, &j.Series, &j.Locale, &j.Revision, &j.SourceAssetID, &j.SourceSHA256, &j.Filename, &j.RendererVersion, &j.FingerprintPayload, &j.TraceCode, &j.Status, &j.Generation, &j.Progress, &j.DerivedAssetID, &j.ReceiptID, &j.ErrorCode, &j.LeaseToken, &lease, &j.Attempts, &cache, &j.CreatedAt, &j.UpdatedAt, &j.ExpiresAt)
	j.LeaseUntil, j.CacheExpiresAt = lease.Time, cache.Time
	if errors.Is(err, sql.ErrNoRows) {
		err = bulletinwatermark.ErrNotFound
	}
	return
}

func (r *Repository) CreateDownloadJob(ctx context.Context, request bulletinwatermark.DownloadJobRequest) (bulletinwatermark.DownloadJob, error) {
	if request.Source.Series == "" {
		request.Source.Series = "general"
	}
	if request.Source.Series != "general" && request.Source.Series != "children" {
		return bulletinwatermark.DownloadJob{}, bulletinwatermark.ErrInvalidDownloadJob
	}
	if uuid.Validate(request.ID) != nil || uuid.Validate(request.IdempotencyKey) != nil || uuid.Validate(request.AccountID) != nil || uuid.Validate(request.Source.IssueID) != nil || request.Source.Revision < 1 || !validWatermarkAssetID(request.Source.AssetID) || len(request.SourceSHA256) != 64 || strings.Trim(request.SourceSHA256, "0123456789abcdef") != "" || request.Source.Filename == "" || request.RendererVersion == "" || request.Now.IsZero() {
		return bulletinwatermark.DownloadJob{}, bulletinwatermark.ErrInvalidDownloadJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletinwatermark.DownloadJob{}, err
	}
	defer tx.Rollback()
	if err = lockWatermarkSubject(ctx, tx, request.AccountID); err != nil {
		return bulletinwatermark.DownloadJob{}, err
	}
	blocked, err := watermarkRestricted(ctx, tx, request.AccountID)
	if err != nil {
		return bulletinwatermark.DownloadJob{}, err
	}
	if blocked {
		return bulletinwatermark.DownloadJob{}, bulletinwatermark.ErrRestricted
	}
	existing, readErr := scanDownloadJob(tx.QueryRowContext(ctx, `SELECT `+downloadJobColumns+` FROM hhc_web.bulletin_download_job WHERE account_id=$1 AND (idempotency_key=$2 OR (source_asset_id=$3 AND renderer_version=$4)) FOR UPDATE`, request.AccountID, request.IdempotencyKey, request.Source.AssetID, request.RendererVersion))
	if readErr == nil {
		if existing.IdempotencyKey == request.IdempotencyKey && (existing.IssueID != request.Source.IssueID || existing.Series != request.Source.Series || existing.Locale != request.Source.Locale || existing.Revision != request.Source.Revision || existing.SourceAssetID != request.Source.AssetID) {
			return bulletinwatermark.DownloadJob{}, bulletinwatermark.ErrDownloadConflict
		}
		sourceChanged := existing.IssueID != request.Source.IssueID || existing.Series != request.Source.Series || existing.Locale != request.Source.Locale || existing.Revision != request.Source.Revision || existing.SourceSHA256 != request.SourceSHA256
		// A running job whose lease has lapsed, or a queued job without claim
		// attempts left, cannot make progress; restart it as a new generation
		// (which also fences the old lease) instead of returning it forever.
		stalled := (existing.Status == "running" && !existing.LeaseUntil.After(request.Now)) || (existing.Status == "queued" && existing.Attempts >= maxDownloadAttempts)
		if sourceChanged || stalled || (existing.Status == "ready" && !existing.CacheExpiresAt.After(request.Now)) || existing.Status == "failed" || existing.Status == "expired" {
			if existing.DerivedAssetID != "" {
				if err = enqueueAssetDeletes(ctx, tx, "bulletin_download_job", existing.ID, int64(existing.Generation), []string{existing.DerivedAssetID}, request.Now); err != nil {
					return bulletinwatermark.DownloadJob{}, err
				}
			}
			existing.Generation++
			existing.IssueID, existing.Series, existing.Locale, existing.Revision = request.Source.IssueID, request.Source.Series, request.Source.Locale, request.Source.Revision
			existing.IssueDate, existing.Filename, existing.SourceSHA256 = request.IssueDate, request.Source.Filename, request.SourceSHA256
			existing.IdempotencyKey, existing.TraceCode, existing.FingerprintPayload = request.IdempotencyKey, request.TraceCode, request.FingerprintPayload
			existing.Status, existing.ErrorCode, existing.DerivedAssetID, existing.ReceiptID, existing.Progress = "queued", "", "", "", 5
			existing.Attempts, existing.LeaseToken = 0, ""
			existing.LeaseUntil, existing.CacheExpiresAt, existing.UpdatedAt = time.Time{}, time.Time{}, request.Now
			_, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_download_job SET idempotency_key=$2,status='queued',generation=$3,progress=5,derived_asset_id=NULL,receipt_id=NULL,error_code=NULL,lease_token=NULL,lease_until=NULL,attempts=0,cache_expires_at=NULL,updated_at=$4,trace_code=$5,fingerprint_payload=$6,issue_id=$7,series=$8,locale=$9,revision=$10,issue_date=$11,filename=$12,source_sha256=$13 WHERE id=$1`, existing.ID, request.IdempotencyKey, existing.Generation, request.Now, existing.TraceCode, existing.FingerprintPayload, existing.IssueID, existing.Series, existing.Locale, existing.Revision, existing.IssueDate, existing.Filename, existing.SourceSHA256)
			if err == nil {
				err = insertWatermarkQueueEvent(ctx, tx, "download", existing.ID, existing.Generation, request.Now)
			}
		}
		if err != nil {
			return bulletinwatermark.DownloadJob{}, err
		}
		if err = tx.Commit(); err != nil {
			return bulletinwatermark.DownloadJob{}, err
		}
		return existing, nil
	}
	if !errors.Is(readErr, bulletinwatermark.ErrNotFound) {
		return bulletinwatermark.DownloadJob{}, readErr
	}
	job := bulletinwatermark.DownloadJob{ID: request.ID, IdempotencyKey: request.IdempotencyKey, AccountID: request.AccountID, IssueID: request.Source.IssueID, IssueDate: request.IssueDate, Series: request.Source.Series, Locale: request.Source.Locale, Revision: request.Source.Revision, SourceAssetID: request.Source.AssetID, SourceSHA256: request.SourceSHA256, Filename: request.Source.Filename, RendererVersion: request.RendererVersion, FingerprintPayload: request.FingerprintPayload, TraceCode: request.TraceCode, Status: "queued", Generation: 1, Progress: 5, CreatedAt: request.Now, UpdatedAt: request.Now, ExpiresAt: request.Now.Add(bulletinwatermark.Retention)}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_download_job(id,idempotency_key,account_id,issue_id,issue_date,series,locale,revision,source_asset_id,source_sha256,filename,renderer_version,fingerprint_payload,trace_code,status,generation,created_at,updated_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'queued',1,$15,$15,$16)`, job.ID, job.IdempotencyKey, job.AccountID, job.IssueID, job.IssueDate, job.Series, job.Locale, job.Revision, job.SourceAssetID, job.SourceSHA256, job.Filename, job.RendererVersion, job.FingerprintPayload, job.TraceCode, job.CreatedAt, job.ExpiresAt)
	if err != nil {
		var dbErr *pgconn.PgError
		if errors.As(err, &dbErr) && dbErr.Code == "23505" {
			return bulletinwatermark.DownloadJob{}, bulletinwatermark.ErrDownloadConflict
		}
		return bulletinwatermark.DownloadJob{}, err
	}
	if err = insertWatermarkQueueEvent(ctx, tx, "download", job.ID, job.Generation, request.Now); err != nil {
		return bulletinwatermark.DownloadJob{}, err
	}
	return job, tx.Commit()
}

func insertWatermarkQueueEvent(ctx context.Context, tx *sql.Tx, kind, id string, generation int, now time.Time) error {
	payload, _ := json.Marshal(map[string]string{"kind": kind, "id": id})
	_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'bulletin-watermark-queue','bulletin.watermark.enqueue','bulletin_watermark',$2,$3,$4,$5,'pending',$6,$6,$6)`, uuid.NewString(), id, generation, payload, fmt.Sprintf("bulletin-watermark:%s:%s:%d", kind, id, generation), now)
	return err
}

func (r *Repository) GetDownloadJob(ctx context.Context, id, account string, now time.Time) (bulletinwatermark.DownloadJob, error) {
	if uuid.Validate(id) != nil || uuid.Validate(account) != nil {
		return bulletinwatermark.DownloadJob{}, bulletinwatermark.ErrNotFound
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletinwatermark.DownloadJob{}, err
	}
	defer tx.Rollback()
	blocked, err := watermarkRestricted(ctx, tx, account)
	if err != nil {
		return bulletinwatermark.DownloadJob{}, err
	}
	if blocked {
		return bulletinwatermark.DownloadJob{}, bulletinwatermark.ErrRestricted
	}
	j, err := scanDownloadJob(tx.QueryRowContext(ctx, `SELECT `+downloadJobColumns+` FROM hhc_web.bulletin_download_job WHERE id=$1 AND account_id=$2 AND expires_at>$3`, id, account, now))
	if err == nil && j.Status == "ready" && !j.CacheExpiresAt.After(now) {
		j.Status, j.DerivedAssetID = "expired", ""
	}
	if err != nil {
		return j, err
	}
	return j, tx.Commit()
}

func (r *Repository) ClaimDownloadJob(ctx context.Context, id string, now time.Time) (bulletinwatermark.DownloadJob, error) {
	lease := uuid.NewString()
	row := r.db.QueryRowContext(ctx, `UPDATE hhc_web.bulletin_download_job SET status='running',progress=GREATEST(progress,10),lease_token=$2,lease_until=$3,attempts=attempts+1,updated_at=$1 WHERE id=$4 AND status IN ('queued','running') AND (status='queued' OR lease_until<=$1) AND attempts<$5 RETURNING `+downloadJobColumns, now, lease, now.Add(downloadLease), id, maxDownloadAttempts)
	return scanDownloadJob(row)
}

func (r *Repository) CompleteDownloadJob(ctx context.Context, id, lease, assetID string, receipt bulletinwatermark.Receipt, now time.Time) error {
	if uuid.Validate(id) != nil || uuid.Validate(lease) != nil || !validWatermarkAssetID(assetID) || receipt.ID == "" {
		return bulletinwatermark.ErrInvalidDownloadJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = insertWatermarkReceipt(ctx, tx, receipt); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_download_job SET status='ready',progress=100,derived_asset_id=$3,receipt_id=$4,error_code=NULL,lease_token=NULL,lease_until=NULL,cache_expires_at=$5,updated_at=$6 WHERE id=$1 AND lease_token=$2 AND status='running' AND lease_until>$6`, id, lease, assetID, receipt.ID, now.Add(bulletinwatermark.DownloadCacheTTL), now)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return bulletinwatermark.ErrDownloadLease
	}
	return tx.Commit()
}

func (r *Repository) UpdateDownloadJobProgress(ctx context.Context, id, lease string, progress int, now time.Time) error {
	if progress < 10 || progress > 99 {
		return bulletinwatermark.ErrInvalidDownloadJob
	}
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.bulletin_download_job SET progress=GREATEST(progress,$3),updated_at=$4 WHERE id=$1 AND lease_token=$2 AND status='running'`, id, lease, progress, now)
	if err == nil {
		if n, _ := result.RowsAffected(); n != 1 {
			return bulletinwatermark.ErrNotFound
		}
	}
	return err
}

func (r *Repository) FailDownloadJob(ctx context.Context, id, lease, code string, now time.Time) error {
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.bulletin_download_job SET status='failed',error_code=$3,lease_token=NULL,lease_until=NULL,updated_at=$4 WHERE id=$1 AND lease_token=$2 AND status='running'`, id, lease, code, now)
	if err == nil {
		if n, _ := result.RowsAffected(); n != 1 {
			return bulletinwatermark.ErrDownloadLease
		}
	}
	return err
}

const (
	maxDownloadAttempts = 3
	// downloadLease is shorter than the queue visibility (300s) and the Job
	// replicaTimeout (300s); render deadline is 180s.
	downloadLease = 270 * time.Second
	// downloadLeaseGrace lets a slow but live worker finish before recovery.
	downloadLeaseGrace = time.Minute
	// downloadQueuedStall is how long a queued job may wait for delivery
	// before the sweeper re-sends its wake-up message.
	downloadQueuedStall = 5 * time.Minute
)

// ReleaseDownloadJob returns a running job to queued after a retryable
// failure so the next delivery can claim it immediately. When the attempt
// budget is spent the job becomes failed/retry_exhausted instead.
func (r *Repository) ReleaseDownloadJob(ctx context.Context, id, lease string, now time.Time) (bool, error) {
	var status string
	err := r.db.QueryRowContext(ctx, `UPDATE hhc_web.bulletin_download_job SET status=CASE WHEN attempts>=$4 THEN 'failed' ELSE 'queued' END,error_code=CASE WHEN attempts>=$4 THEN 'retry_exhausted' ELSE NULL END,lease_token=NULL,lease_until=NULL,updated_at=$3 WHERE id=$1 AND lease_token=$2 AND status='running' RETURNING status`, id, lease, now, maxDownloadAttempts).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, bulletinwatermark.ErrDownloadLease
	}
	return status == "failed", err
}

// RecoverStalledDownloadJobs reconciles jobs the queue can no longer advance:
//   - running with a lease expired for longer than the grace period (worker
//     killed or message returned before the lease) is re-queued;
//   - queued without delivery for downloadQueuedStall is re-sent and charged
//     one attempt, so a poisoned or lost message stays bounded;
//   - either case without attempts left becomes failed/retry_exhausted.
//
// Re-queued jobs get their outbox wake-up re-armed in the same transaction.
func (r *Repository) RecoverStalledDownloadJobs(ctx context.Context, now time.Time, limit int) (requeued, failed int64, err error) {
	if limit < 1 || limit > 1000 {
		limit = 1000
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_download_job SET status='failed',error_code='retry_exhausted',lease_token=NULL,lease_until=NULL,updated_at=$1 WHERE id IN (SELECT id FROM hhc_web.bulletin_download_job WHERE expires_at>$1 AND attempts>=$4 AND ((status='running' AND lease_until<=$1::timestamptz-$3::interval) OR status='queued') ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED)`, now, limit, downloadLeaseGrace.String(), maxDownloadAttempts)
	if err != nil {
		return 0, 0, err
	}
	failed, _ = result.RowsAffected()
	rows, err := tx.QueryContext(ctx, `UPDATE hhc_web.bulletin_download_job j SET attempts=j.attempts+CASE WHEN j.status='queued' THEN 1 ELSE 0 END,status='queued',lease_token=NULL,lease_until=NULL,updated_at=$1 FROM (SELECT id FROM hhc_web.bulletin_download_job WHERE expires_at>$1 AND attempts<$5 AND ((status='running' AND lease_until<=$1::timestamptz-$3::interval) OR (status='queued' AND updated_at<=$1::timestamptz-$4::interval)) ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED) stalled WHERE j.id=stalled.id RETURNING j.id::text,j.generation`, now, limit, downloadLeaseGrace.String(), downloadQueuedStall.String(), maxDownloadAttempts)
	if err != nil {
		return 0, 0, err
	}
	type wake struct {
		id         string
		generation int
	}
	var wakes []wake
	for rows.Next() {
		var item wake
		if err = rows.Scan(&item.id, &item.generation); err != nil {
			rows.Close()
			return 0, 0, err
		}
		wakes = append(wakes, item)
	}
	if err = rows.Close(); err != nil {
		return 0, 0, err
	}
	for _, item := range wakes {
		if err = rearmWatermarkQueueEvent(ctx, tx, "download", item.id, item.generation, now); err != nil {
			return 0, 0, err
		}
	}
	return int64(len(wakes)), failed, tx.Commit()
}

// rearmWatermarkQueueEvent re-sends the generation's existing wake-up event,
// mirroring migration 046: the idempotency key is per generation, so a
// dispatched row is reset to pending rather than duplicated.
func rearmWatermarkQueueEvent(ctx context.Context, tx *sql.Tx, kind, id string, generation int, now time.Time) error {
	payload, _ := json.Marshal(map[string]string{"kind": kind, "id": id})
	_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,attempts,next_attempt_at,created_at,updated_at) VALUES($1,'bulletin-watermark-queue','bulletin.watermark.enqueue','bulletin_watermark',$2,$3,$4,$5,'pending',0,$6,$6,$6) ON CONFLICT(destination,idempotency_key) DO UPDATE SET aggregate_version=EXCLUDED.aggregate_version,payload_json=EXCLUDED.payload_json,status='pending',attempts=0,claimed_until=NULL,last_error='',next_attempt_at=EXCLUDED.next_attempt_at,updated_at=EXCLUDED.updated_at`, uuid.NewString(), id, generation, payload, fmt.Sprintf("bulletin-watermark:%s:%s:%d", kind, id, generation), now)
	return err
}

func (r *Repository) ExhaustDownloadJob(ctx context.Context, id string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.bulletin_download_job SET status='failed',error_code='retry_exhausted',lease_token=NULL,lease_until=NULL,updated_at=$2 WHERE id=$1 AND status IN ('queued','running')`, id, now)
	return err
}
