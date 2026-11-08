package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func watermarkSubjectHash(userID string) string {
	sum := sha256.Sum256([]byte("bulletin-watermark-subject:" + userID))
	return hex.EncodeToString(sum[:])
}
func validWatermarkAssetID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}
func lockWatermarkSubject(ctx context.Context, tx *sql.Tx, userID string) error {
	// Serialize issuance and lookup with restrict/erase, across replicas.
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,20260911))`, watermarkSubjectHash(userID))
	return err
}

func shareWatermarkSubject(ctx context.Context, tx *sql.Tx, userID string) error {
	// Parallel issuers still fence restriction/erasure's exclusive subject lock.
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,20260911))`, watermarkSubjectHash(userID))
	return err
}
func (r *Repository) InsertWatermarkReceipt(ctx context.Context, v bulletinwatermark.Receipt) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = insertWatermarkReceipt(ctx, tx, v); err != nil {
		return err
	}
	return tx.Commit()
}

func insertWatermarkReceipt(ctx context.Context, tx *sql.Tx, v bulletinwatermark.Receipt) error {
	var err error
	if v.Series == "" {
		v.Series = "general"
	}
	if v.Series != "general" && v.Series != "children" {
		return bulletinwatermark.ErrInvalidFingerprint
	}
	if _, err := bulletinwatermark.NormalizeCode(v.Code); err != nil {
		return err
	}
	if err := v.ValidateFingerprint(); err != nil {
		return err
	}
	var pages any
	if len(v.Pages) > 0 {
		encoded, err := json.Marshal(v.Pages)
		if err != nil {
			return err
		}
		pages = string(encoded)
	}
	if v.FingerprintPayload != "" {
		if err = lockWatermarkScope(ctx, tx, bulletinwatermark.CandidateScope{SourceAssetID: v.SourceAssetID, Revision: v.Revision, RendererVersion: v.RendererVersion}); err != nil {
			return err
		}
	}
	if err = lockWatermarkSubject(ctx, tx, v.UserID); err != nil {
		return err
	}
	var blocked bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1)`, watermarkSubjectHash(v.UserID)).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return bulletinwatermark.ErrRestricted
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_receipt
 (id,trace_code,user_id,issue_id,series,locale,revision,source_asset_id,source_sha256,output_sha256,renderer_version,privacy_notice_version,issued_at,expires_at,fingerprint_payload,model_sha256,pages_json)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,NULLIF($15,''),NULLIF($16,''),$17)`, v.ID, v.Code, v.UserID, v.IssueID, v.Series, v.Locale, v.Revision, v.SourceAssetID, v.SourceSHA256, v.OutputSHA256, v.RendererVersion, v.PrivacyVersion, v.IssuedAt, v.ExpiresAt, v.FingerprintPayload, v.ModelSHA256, pages)
	if err != nil {
		var dbErr *pgconn.PgError
		if errors.As(err, &dbErr) && dbErr.Code == "23505" && (dbErr.ConstraintName == "bulletin_watermark_receipt_trace_code_key" || dbErr.ConstraintName == "bulletin_watermark_receipt_fingerprint_key" || dbErr.ConstraintName == "bulletin_trace_code_unique") {
			return bulletinwatermark.ErrCodeCollision
		}
		return err
	}
	return nil
}

const watermarkColumns = `id::text,trace_code,user_id::text,issue_id::text,series,locale,revision,source_asset_key,source_sha256,output_sha256,renderer_version,privacy_notice_version,issued_at,expires_at,COALESCE(fingerprint_payload,''),COALESCE(model_sha256,''),pages_json`

func scanWatermark(scan interface{ Scan(...any) error }) (v bulletinwatermark.Receipt, err error) {
	var pages []byte
	err = scan.Scan(&v.ID, &v.Code, &v.UserID, &v.IssueID, &v.Series, &v.Locale, &v.Revision, &v.SourceAssetID, &v.SourceSHA256, &v.OutputSHA256, &v.RendererVersion, &v.PrivacyVersion, &v.IssuedAt, &v.ExpiresAt, &v.FingerprintPayload, &v.ModelSHA256, &pages)
	if err != nil {
		return
	}
	if len(pages) > 0 {
		err = json.Unmarshal(pages, &v.Pages)
		if err != nil {
			return
		}
	}
	err = v.ValidateFingerprint()
	return
}

func (r *Repository) FindWatermarkReceiptByOutputSHA256(ctx context.Context, outputSHA256 string, now time.Time) (bulletinwatermark.Receipt, error) {
	if len(outputSHA256) != 64 || strings.Trim(outputSHA256, "0123456789abcdef") != "" {
		return bulletinwatermark.Receipt{}, bulletinwatermark.ErrInvalidFingerprint
	}
	return r.findWatermarkReceiptExact(ctx, "output_sha256", outputSHA256, now)
}

func (r *Repository) FindWatermarkReceiptByPayload(ctx context.Context, payload string, now time.Time) (bulletinwatermark.Receipt, error) {
	if len(payload) != 61 || strings.Trim(payload, "01") != "" {
		return bulletinwatermark.Receipt{}, bulletinwatermark.ErrInvalidFingerprint
	}
	return r.findWatermarkReceiptExact(ctx, "fingerprint_payload", payload, now)
}

func (r *Repository) findWatermarkReceiptExact(ctx context.Context, column, key string, now time.Time) (bulletinwatermark.Receipt, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT user_id::text FROM hhc_web.bulletin_watermark_receipt WHERE `+column+`=$1 AND expires_at>$2 ORDER BY user_id`, key, now)
	if err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	var subjects []string
	for rows.Next() {
		var subject string
		if err = rows.Scan(&subject); err != nil {
			rows.Close()
			return bulletinwatermark.Receipt{}, err
		}
		subjects = append(subjects, subject)
	}
	if err = rows.Close(); err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	slices.Sort(subjects)
	for _, subject := range slices.Compact(subjects) {
		if err = lockWatermarkSubject(ctx, tx, subject); err != nil {
			return bulletinwatermark.Receipt{}, err
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT `+watermarkColumns+` FROM hhc_web.bulletin_watermark_receipt r WHERE r.`+column+`=$1 AND r.expires_at>$2 AND NOT EXISTS (SELECT 1 FROM hhc_web.bulletin_watermark_subject s WHERE s.subject_hash=encode(sha256(convert_to('bulletin-watermark-subject:'||r.user_id::text,'UTF8')),'hex')) ORDER BY r.id LIMIT 2 FOR SHARE OF r`, key, now)
	if err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	matches := []bulletinwatermark.Receipt{}
	for rows.Next() {
		v, scanErr := scanWatermark(rows)
		if scanErr != nil {
			rows.Close()
			return bulletinwatermark.Receipt{}, scanErr
		}
		matches = append(matches, v)
	}
	if err = rows.Close(); err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	if len(matches) == 0 {
		return bulletinwatermark.Receipt{}, bulletinwatermark.ErrNotFound
	}
	if len(matches) != 1 {
		return bulletinwatermark.Receipt{}, bulletinwatermark.ErrReceiptIntegrity
	}
	v := matches[0]
	validHash := func(value string) bool { return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == "" }
	if uuid.Validate(v.ID) != nil || uuid.Validate(v.UserID) != nil || uuid.Validate(v.IssueID) != nil || !validWatermarkAssetID(v.SourceAssetID) ||
		v.Locale == "" || v.Revision < 1 || !validHash(v.SourceSHA256) || !validHash(v.OutputSHA256) || v.RendererVersion == "" || v.PrivacyVersion == "" ||
		v.IssuedAt.IsZero() || !v.ExpiresAt.After(now) || column == "output_sha256" && v.OutputSHA256 != key || column == "fingerprint_payload" && v.FingerprintPayload != key {
		return bulletinwatermark.Receipt{}, bulletinwatermark.ErrReceiptIntegrity
	}
	if err = tx.Commit(); err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	return v, nil
}

func (r *Repository) LookupWatermarkReceipt(ctx context.Context, code, actor string, now time.Time) (bulletinwatermark.Receipt, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	defer tx.Rollback()
	// Discover the subject without a row lock, then fence both subjects in a
	// stable order. Re-read under those fences so erase/restrict cannot race
	// disclosure, and reciprocal investigations cannot deadlock.
	var subject string
	err = tx.QueryRowContext(ctx, `SELECT user_id::text FROM hhc_web.bulletin_watermark_receipt WHERE trace_code=$1 AND expires_at>$2 UNION ALL SELECT account_id::text FROM hhc_web.online_bulletin_reader_receipt WHERE trace_code=$1 AND expires_at>$2`, code, now).Scan(&subject)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return bulletinwatermark.Receipt{}, err
	}
	subjects := []string{actor}
	if subject != "" && subject != actor {
		subjects = append(subjects, subject)
	}
	slices.Sort(subjects)
	for _, userID := range subjects {
		if err = lockWatermarkSubject(ctx, tx, userID); err != nil {
			return bulletinwatermark.Receipt{}, err
		}
	}
	var restricted bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1)`, watermarkSubjectHash(actor)).Scan(&restricted); err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	if restricted {
		return bulletinwatermark.Receipt{}, bulletinwatermark.ErrRestricted
	}
	var count int
	if err = tx.QueryRowContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_lookup_rate AS counters(subject_hash,window_start,count) VALUES($1,$2,1) ON CONFLICT(subject_hash,window_start) DO UPDATE SET count=counters.count+1 RETURNING count`, watermarkSubjectHash(actor), now.UTC().Truncate(time.Minute)).Scan(&count); err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	if count > 10 {
		return bulletinwatermark.Receipt{}, bulletinwatermark.ErrRateLimited
	}
	v, err := scanWatermark(tx.QueryRowContext(ctx, `SELECT `+watermarkColumns+` FROM hhc_web.bulletin_watermark_receipt WHERE trace_code=$1 AND expires_at>$2 AND user_id=NULLIF($3,'')::uuid FOR SHARE`, code, now, subject))
	if errors.Is(err, sql.ErrNoRows) {
		v, err = lookupReaderTrace(ctx, tx, code, subject, now)
	}
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return v, err
	}
	if !missing {
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1)`, watermarkSubjectHash(v.UserID)).Scan(&restricted); err != nil {
			return bulletinwatermark.Receipt{}, err
		}
		if restricted {
			// A restricted subject is indistinguishable from a missing receipt;
			// still count and audit the attempt without disclosing its target.
			missing = true
			v = bulletinwatermark.Receipt{}
		}
	}
	resource := v.ID
	if missing {
		resource = uuid.NewString()
	}
	payload, _ := json.Marshal(map[string]any{"matched": !missing})
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.cms_audit_event(id,action,resource_type,resource_id,actor,payload_json,created_at) VALUES($1,'bulletin_watermark.lookup','bulletin_watermark_receipt',$2,$3,$4,$5)`, uuid.NewString(), resource, actor, payload, now); err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return bulletinwatermark.Receipt{}, err
	}
	if missing {
		return v, bulletinwatermark.ErrNotFound
	}
	return v, nil
}
func (r *Repository) PurgeWatermarkRecords(ctx context.Context, now time.Time, limit int) (int64, error) {
	if limit < 1 || limit > 1000 {
		limit = 1000
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var total int64
	rows, err := tx.QueryContext(ctx, `SELECT id::text,derived_asset_id FROM hhc_web.bulletin_download_job WHERE status='ready' AND cache_expires_at<=$1 ORDER BY cache_expires_at LIMIT $2 FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return 0, err
	}
	var expiredJobs, expiredAssets []string
	for rows.Next() {
		var id, asset string
		if err = rows.Scan(&id, &asset); err != nil {
			rows.Close()
			return 0, err
		}
		expiredJobs, expiredAssets = append(expiredJobs, id), append(expiredAssets, asset)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	for i, id := range expiredJobs {
		if err = enqueueAssetDeletes(ctx, tx, "bulletin_download_job", id, 1, []string{expiredAssets[i]}, now); err != nil {
			return 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_download_job SET status='expired',derived_asset_id=NULL,receipt_id=NULL,cache_expires_at=NULL,updated_at=$2 WHERE id=$1`, id, now); err != nil {
			return 0, err
		}
		total++
	}
	for _, query := range []string{
		`DELETE FROM hhc_web.bulletin_reader_processed_mutation WHERE (account_id,mutation_id) IN (SELECT account_id,mutation_id FROM hhc_web.bulletin_reader_processed_mutation WHERE expires_at<=$1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.online_bulletin_reader_receipt WHERE id IN (SELECT id FROM hhc_web.online_bulletin_reader_receipt WHERE expires_at<=$1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.bulletin_watermark_investigation WHERE id IN (SELECT id FROM hhc_web.bulletin_watermark_investigation WHERE expires_at<=$1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.recording_watermark_receipt WHERE id IN (SELECT id FROM hhc_web.recording_watermark_receipt WHERE expires_at<=$1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.member_recording_progress WHERE (account_id,recording_id) IN (SELECT account_id,recording_id FROM hhc_web.member_recording_progress WHERE expires_at<=$1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.member_recording_progress WHERE (account_id,recording_id) IN (
			SELECT p.account_id,p.recording_id FROM hhc_web.member_recording_progress p
			JOIN hhc_web.member_recording r ON r.id=p.recording_id
			WHERE r.media_terminal OR r.deleted_at IS NOT NULL OR r.expires_at<=$1
			ORDER BY p.expires_at LIMIT $2 FOR UPDATE OF p SKIP LOCKED)`,
		`DELETE FROM hhc_web.member_recording_capture_scope WHERE (capture_intent_id,scope_id) IN (SELECT s.capture_intent_id,s.scope_id FROM hhc_web.member_recording_capture_scope s JOIN hhc_web.member_recording_capture c ON c.id=s.capture_intent_id WHERE (c.payload_json->'Status'->>'expiresAt')::timestamptz<=$1 ORDER BY s.registered_at LIMIT $2 FOR UPDATE OF s SKIP LOCKED)`,
		`DELETE FROM hhc_web.recording_watermark_lookup_rate WHERE (subject_hash,window_start) IN (SELECT subject_hash,window_start FROM hhc_web.recording_watermark_lookup_rate WHERE window_start<$1::timestamptz-interval '2 days' ORDER BY window_start LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.cms_audit_event WHERE id IN (SELECT id FROM hhc_web.cms_audit_event WHERE action='recording_watermark.lookup' AND created_at<=$1::timestamptz-interval '8760 hours' ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`UPDATE hhc_web.bulletin_watermark_investigation SET status='expired',evidence=NULL,lease_token=NULL,lease_until=NULL WHERE id IN (SELECT id FROM hhc_web.bulletin_watermark_investigation WHERE status IN ('queued','running') AND evidence_expires_at<=$1 ORDER BY evidence_expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.bulletin_watermark_lookup_rate WHERE (subject_hash,window_start) IN (SELECT subject_hash,window_start FROM hhc_web.bulletin_watermark_lookup_rate WHERE window_start<$1::timestamptz-interval '2 days' ORDER BY window_start LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.bulletin_watermark_dsr_action WHERE key_hash IN (SELECT key_hash FROM hhc_web.bulletin_watermark_dsr_action WHERE created_at<=$1::timestamptz-interval '8760 hours' ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.bulletin_download_job WHERE id IN (SELECT id FROM hhc_web.bulletin_download_job WHERE expires_at<=$1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.bulletin_watermark_receipt WHERE id IN (SELECT id FROM hhc_web.bulletin_watermark_receipt WHERE expires_at<=$1 ORDER BY expires_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.cms_audit_event WHERE id IN (SELECT id FROM hhc_web.cms_audit_event WHERE action='bulletin_watermark.lookup' AND created_at<=$1::timestamptz-interval '8760 hours' ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
		`DELETE FROM hhc_web.bulletin_watermark_subject WHERE subject_hash IN (SELECT subject_hash FROM hhc_web.bulletin_watermark_subject WHERE erased AND updated_at<=$1::timestamptz-interval '8760 hours' ORDER BY updated_at LIMIT $2 FOR UPDATE SKIP LOCKED)`,
	} {
		result, err := tx.ExecContext(ctx, query, now, limit)
		if err != nil {
			return 0, err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, tx.Commit()
}

func (r *Repository) PublishedWatermarkSource(ctx context.Context, date, locale string) (bulletinwatermark.Source, error) {
	var source bulletinwatermark.Source
	var title string
	err := r.db.QueryRowContext(ctx, `SELECT i.id::text,p.asset_id,p.version,p.title FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_published_version p ON p.issue_id=i.id WHERE p.series='general' AND i.issue_date=$1 AND p.locale=$2`, date, locale).Scan(&source.IssueID, &source.AssetID, &source.Revision, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return source, bulletinwatermark.ErrNotFound
	}
	if err != nil {
		return source, err
	}
	if source.AssetID == "" || source.Revision < 1 {
		return source, bulletinwatermark.ErrNotFound
	}
	source.Locale = locale
	source.Filename = date + "-" + title + ".pdf"
	return source, nil
}

const readerPrivateCountSQL = `(SELECT count(*) FROM hhc_web.bulletin_reader_migration_state WHERE account_id=$1)+
(SELECT count(*) FROM hhc_web.bulletin_reader_highlight WHERE account_id=$1)+
(SELECT count(*) FROM hhc_web.bulletin_reader_note WHERE account_id=$1)+
(SELECT count(*) FROM hhc_web.bulletin_reader_bookmark WHERE account_id=$1)+
(SELECT count(*) FROM hhc_web.bulletin_reader_progress WHERE account_id=$1)+
(SELECT count(*) FROM hhc_web.bulletin_reader_processed_mutation WHERE account_id=$1)`

func (r *Repository) CleanupWatermarkAccount(ctx context.Context, userID, idempotencyKey string) (int64, int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	subjectRef := watermarkCleanupSubjectRef(userID)
	count, remaining, err := cleanupWatermarkAccount(ctx, tx, userID, idempotencyKey, subjectRef)
	if err != nil {
		return 0, 0, err
	}
	return count, remaining, tx.Commit()
}

func cleanupWatermarkAccount(ctx context.Context, tx *sql.Tx, userID, idempotencyKey, subjectRef string) (int64, int64, error) {
	var err error
	aggregateID := watermarkCleanupAggregateID(subjectRef)
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.account_cleanup_operation(idempotency_key,subject_ref) VALUES($1,$2) ON CONFLICT DO NOTHING`, idempotencyKey, subjectRef); err != nil {
		return 0, 0, err
	}
	var operationSubject string
	var storedCount, storedRemaining sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT subject_ref::text,record_count,remaining_count FROM hhc_web.account_cleanup_operation WHERE idempotency_key=$1 FOR UPDATE`, idempotencyKey).Scan(&operationSubject, &storedCount, &storedRemaining); err != nil {
		return 0, 0, err
	}
	if operationSubject != subjectRef {
		return 0, 0, bulletinwatermark.ErrInvalidInvestigation
	}
	if err = lockWatermarkSubject(ctx, tx, userID); err != nil {
		return 0, 0, err
	}
	hash := watermarkSubjectHash(userID)
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES($1,true) ON CONFLICT(subject_hash) DO UPDATE SET erased=true,updated_at=now()`, hash); err != nil {
		return 0, 0, err
	}
	count := storedCount.Int64
	if !storedCount.Valid {
		if err = tx.QueryRowContext(ctx, `SELECT `+readerPrivateCountSQL+`+`+recordingInteractionPrivateCountSQL+`+(SELECT count(*) FROM hhc_web.statement_dismissal WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.recording_watermark_receipt WHERE user_id=$1)+(SELECT count(*) FROM hhc_web.member_recording_capture_scope WHERE user_id=$1)+(SELECT count(*) FROM hhc_web.online_bulletin_reader_receipt WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.online_bulletin_reader_request WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.bulletin_watermark_receipt WHERE user_id=$1)+(SELECT count(*) FROM hhc_web.bulletin_watermark_investigation WHERE actor_id=$1)+(SELECT count(*) FROM hhc_web.bulletin_download_job WHERE account_id=$1)`, userID).Scan(&count); err != nil {
			return 0, 0, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.account_cleanup_operation SET record_count=$2,updated_at=now() WHERE idempotency_key=$1`, idempotencyKey, count); err != nil {
			return 0, 0, err
		}
	}
	if err = eraseWatermarkAccount(ctx, tx, userID, aggregateID); err != nil {
		return 0, 0, err
	}
	if err = ensureWatermarkAccountErased(ctx, tx, userID); err != nil {
		return 0, 0, err
	}
	var remaining int64
	if err = tx.QueryRowContext(ctx, `SELECT `+readerPrivateCountSQL+`+`+recordingInteractionPrivateCountSQL+`+(SELECT count(*) FROM hhc_web.statement_dismissal WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.recording_watermark_receipt WHERE user_id=$1)+(SELECT count(*) FROM hhc_web.member_recording_capture_scope WHERE user_id=$1)+
		(SELECT count(*) FROM hhc_web.online_bulletin_reader_receipt WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.online_bulletin_reader_request WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.bulletin_watermark_receipt WHERE user_id=$1)+
		(SELECT count(*) FROM hhc_web.bulletin_watermark_investigation WHERE actor_id=$1)+
		(SELECT count(*) FROM hhc_web.bulletin_download_job WHERE account_id=$1)+
		(SELECT count(*) FROM hhc_web.account_identity_projection WHERE account_user_id=$1)+
		(SELECT count(*) FROM hhc_web.outbox_event WHERE destination='asset-api' AND event_type='asset.owner.delete' AND status<>'delivered' AND ((aggregate_type='bulletin_download_job_account_cleanup' AND aggregate_id=$2) OR (aggregate_type='bulletin_download_job' AND aggregate_id=$1::uuid)))`, userID, aggregateID).Scan(&remaining); err != nil {
		return 0, 0, err
	}
	if remaining == 0 {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.account_cleanup_operation SET remaining_count=0,updated_at=now() WHERE idempotency_key=$1`, idempotencyKey); err != nil {
			return 0, 0, err
		}
	} else if storedRemaining.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.account_cleanup_operation SET remaining_count=NULL,updated_at=now() WHERE idempotency_key=$1`, idempotencyKey); err != nil {
			return 0, 0, err
		}
	}
	return count, remaining, nil
}

func watermarkCleanupSubjectRef(userID string) string {
	subjectHash := sha256.Sum256([]byte("website-watermark-account-cleanup:" + userID))
	return hex.EncodeToString(subjectHash[:])
}

func watermarkCleanupAggregateID(subjectRef string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(subjectRef)).String()
}

func ensureWatermarkAccountErased(ctx context.Context, tx *sql.Tx, userID string) error {
	var remaining int64
	if err := tx.QueryRowContext(ctx, `SELECT `+readerPrivateCountSQL+`+`+recordingInteractionPrivateCountSQL+`+(SELECT count(*) FROM hhc_web.statement_dismissal WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.recording_watermark_receipt WHERE user_id=$1)+(SELECT count(*) FROM hhc_web.member_recording_capture_scope WHERE user_id=$1)+
		(SELECT count(*) FROM hhc_web.online_bulletin_reader_receipt WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.online_bulletin_reader_request WHERE account_id=$1)+(SELECT count(*) FROM hhc_web.bulletin_watermark_receipt WHERE user_id=$1)+
		(SELECT count(*) FROM hhc_web.bulletin_watermark_investigation WHERE actor_id=$1)+
		(SELECT count(*) FROM hhc_web.bulletin_download_job WHERE account_id=$1)+
		(SELECT count(*) FROM hhc_web.account_identity_projection WHERE account_user_id=$1)`, userID).Scan(&remaining); err != nil {
		return err
	}
	if remaining != 0 {
		return errors.New("watermark account cleanup postcondition failed")
	}
	return nil
}

const watermarkAccountAssetDeletesQuery = `SELECT derived_asset_id FROM hhc_web.bulletin_download_job WHERE account_id=$1 AND derived_asset_id IS NOT NULL
UNION
SELECT e.payload_json->>'assetId' FROM hhc_web.outbox_event e
JOIN hhc_web.bulletin_download_job j ON j.id=e.aggregate_id
WHERE j.account_id=$1 AND e.destination='asset-api' AND e.event_type='asset.owner.delete'
  AND e.aggregate_type='bulletin_download_job' AND e.status<>'delivered'`

func eraseWatermarkAccount(ctx context.Context, tx *sql.Tx, userID, subjectRef string) error {
	rows, err := tx.QueryContext(ctx, watermarkAccountAssetDeletesQuery, userID)
	if err != nil {
		return err
	}
	var assets []string
	for rows.Next() {
		var asset string
		if err = rows.Scan(&asset); err != nil {
			rows.Close()
			return err
		}
		assets = append(assets, asset)
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = enqueueAssetDeletes(ctx, tx, "bulletin_download_job_account_cleanup", subjectRef, 1, assets, time.Now().UTC()); err != nil {
		return err
	}
	for _, query := range []string{
		`DELETE FROM hhc_web.member_recording_comment_reaction WHERE account_id=$1`,
		`DELETE FROM hhc_web.member_recording_comment_rank WHERE account_id=$1`,
		`DELETE FROM hhc_web.member_recording_like WHERE account_id=$1`,
		`DELETE FROM hhc_web.member_recording_playback_session WHERE account_id=$1`,
		`DELETE FROM hhc_web.member_recording_progress WHERE account_id=$1`,
		`DELETE FROM hhc_web.member_recording_playback_visit WHERE account_id=$1`,
		`DELETE FROM hhc_web.member_recording_viewer WHERE account_id=$1`,
		`DELETE FROM hhc_web.cms_audit_event WHERE actor=$1 AND action LIKE 'recording.comment.%'`,
		`DELETE FROM hhc_web.member_recording_comment_report WHERE account_id=$1`,
		`UPDATE hhc_web.member_recording_comment SET account_id=NULL,mutation_id=NULL,request_hash=NULL,body='',deleted=true,revision=revision+1,edited_at=now() WHERE account_id=$1`,
		`DELETE FROM hhc_web.statement_dismissal WHERE account_id=$1`,
		`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE account_id=$1`,
		`DELETE FROM hhc_web.online_bulletin_reader_receipt WHERE account_id=$1`,
		`DELETE FROM hhc_web.recording_watermark_receipt WHERE user_id=$1`,
		`DELETE FROM hhc_web.member_recording_capture_scope WHERE user_id=$1`,
		`DELETE FROM hhc_web.cms_audit_event WHERE action='recording_watermark.lookup' AND actor=$1`,
		`DELETE FROM hhc_web.bulletin_watermark_investigation WHERE actor_id=$1`,
		`DELETE FROM hhc_web.bulletin_download_job WHERE account_id=$1`,
		`DELETE FROM hhc_web.bulletin_watermark_receipt WHERE user_id=$1`,
		`DELETE FROM hhc_web.account_identity_projection WHERE account_user_id=$1`,
		`DELETE FROM hhc_web.cms_audit_event WHERE action='bulletin_watermark.lookup' AND actor=$1`,
	} {
		if _, err = tx.ExecContext(ctx, query, userID); err != nil {
			return err
		}
	}
	return nil
}

// ListWatermarkCandidates returns only a complete bounded eligible set. It does not authorize disclosure.
// The worker must recheck eligibility and commit its audit at result disclosure.
func (r *Repository) ListWatermarkCandidates(ctx context.Context, scope bulletinwatermark.CandidateScope, now time.Time, limit int) ([]bulletinwatermark.Receipt, error) {
	return listWatermarkCandidates(ctx, r.db, scope, now, limit)
}

func listWatermarkCandidates(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, scope bulletinwatermark.CandidateScope, now time.Time, limit int) ([]bulletinwatermark.Receipt, error) {
	validHash := func(s string) bool { return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == "" }
	scope.Series = bulletinwatermark.NormalizeSeries(scope.Series)
	if uuid.Validate(scope.IssueID) != nil || !validWatermarkAssetID(scope.SourceAssetID) || !bulletinwatermark.ValidEdition(scope.Series, scope.Locale) || scope.Revision < 1 || scope.RendererVersion == "" || !validHash(scope.SourceSHA256) || !validHash(scope.ModelSHA256) || limit < 1 || limit > 64 {
		return nil, bulletinwatermark.ErrInvalidFingerprint
	}
	rows, err := db.QueryContext(ctx, `SELECT `+watermarkColumns+` FROM hhc_web.bulletin_watermark_receipt
 WHERE issue_id=$1 AND series=$2 AND locale=$3 AND revision=$4 AND source_asset_id=$5 AND source_sha256=$6 AND renderer_version=$7 AND model_sha256=$8
 AND fingerprint_payload IS NOT NULL AND expires_at>$9
 AND NOT EXISTS (SELECT 1 FROM hhc_web.bulletin_watermark_subject s WHERE s.subject_hash=encode(sha256(convert_to('bulletin-watermark-subject:'||user_id::text,'UTF8')),'hex'))
 ORDER BY id LIMIT $10`, scope.IssueID, scope.Series, scope.Locale, scope.Revision, scope.SourceAssetID, scope.SourceSHA256, scope.RendererVersion, scope.ModelSHA256, now, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bulletinwatermark.Receipt{}
	for rows.Next() {
		v, err := scanWatermark(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
		if len(out) > limit {
			return nil, bulletinwatermark.ErrCandidateLimit
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
