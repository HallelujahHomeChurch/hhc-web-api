package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func recordProductionClaim(ctx context.Context, tx *sql.Tx, job onlinebulletins.ExtractionJob, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET production_started_at=CASE WHEN production_tracking_started AND (production_started_at IS NOT NULL OR attempts=1) THEN COALESCE(production_started_at,$2) END,production_tracking_started=production_tracking_started AND (production_started_at IS NOT NULL OR attempts=1),production_attempt_id=$3,production_current_page=NULL,production_heartbeat_at=$2 WHERE id=$1`, job.ID, now, platform.NewID())
	return err
}

func validProductionJob(job onlinebulletins.ExtractionJob, now time.Time) bool {
	return !now.IsZero() && job.ExtractorVersion == onlinebulletins.RecognitionExtractorVersion()
}

func (r *Repository) BeginRecognitionProgress(ctx context.Context, job onlinebulletins.ExtractionJob, total int, now time.Time) error {
	if !validProductionJob(job, now) || total < 4 || total > 40 {
		return onlinebulletins.ErrExtractionJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockRecognitionCheckpointLease(ctx, tx, job, now); err != nil {
		return err
	}
	// A changed metadata/protocol scope cannot inherit receipts from an old run.
	if _, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.online_bulletin_production_page WHERE job_id=$1 AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_extraction_job j WHERE j.id=$1 AND (j.production_canonical IS DISTINCT FROM $2::jsonb OR j.production_protocol_sha256 IS DISTINCT FROM $3 OR j.production_total_pages IS DISTINCT FROM $4))`, job.ID, mustJSON(job.Canonical.Metadata), onlinebulletins.RecognitionProtocolSHA256(), total); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET production_total_pages=$2,production_canonical=$3,production_protocol_sha256=$4,production_phase='recognition',production_current_page=NULL,production_subphase='source',production_heartbeat_at=$5,production_reused_snapshot_sha256=NULL WHERE id=$1`, job.ID, total, mustJSON(job.Canonical.Metadata), onlinebulletins.RecognitionProtocolSHA256(), now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func progressInput(job onlinebulletins.ExtractionJob, input onlinebulletins.RecognitionPage) (string, string, error) {
	if input.Canonical == nil || *input.Canonical != job.Canonical.Metadata || input.SourceSHA256 != job.SourceAssetChecksum || input.Page < 1 || input.Page > input.TotalPages || input.TotalPages > 40 {
		return "", "", onlinebulletins.ErrExtractionJob
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", "", onlinebulletins.ErrExtractionJob
	}
	sum := sha256.Sum256(raw)
	previous := ""
	if input.Continuation != nil {
		previous = input.Continuation.PreviousSHA256
	}
	return hex.EncodeToString(sum[:]), previous, nil
}

func (r *Repository) StartRecognitionPage(ctx context.Context, job onlinebulletins.ExtractionJob, input onlinebulletins.RecognitionPage, now time.Time) error {
	if !validProductionJob(job, now) {
		return onlinebulletins.ErrExtractionJob
	}
	hash, previous, err := progressInput(job, input)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockRecognitionCheckpointLease(ctx, tx, job, now); err != nil {
		return err
	}
	// Invalidation commits before loading an immutable checkpoint. A mismatch can
	// therefore fail closed without leaving a falsely verified dependent suffix.
	if _, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.online_bulletin_production_page WHERE job_id=$1 AND source_page>=$2 AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_production_page p WHERE p.job_id=$1 AND p.source_page=$2 AND (p.input_sha256<>$3 OR p.previous_sha256<>$4))`, job.ID, input.Page, hash, previous); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET production_phase='recognition',production_current_page=$2,production_subphase='recognition',production_heartbeat_at=$3 WHERE id=$1 AND production_total_pages=$4 AND production_canonical=$5::jsonb AND production_protocol_sha256=$6`, job.ID, input.Page, now, input.TotalPages, mustJSON(job.Canonical.Metadata), onlinebulletins.RecognitionProtocolSHA256())
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return onlinebulletins.ErrExtractionJob
	}
	return tx.Commit()
}

func productionPrefix(ctx context.Context, tx *sql.Tx, job onlinebulletins.ExtractionJob) (int, error) {
	var count, reused int
	err := tx.QueryRowContext(ctx, `SELECT verified_pages,reused_pages FROM hhc_web.online_bulletin_production_prefix($1,$2,$3,$4,$5)`, job.ID, onlinebulletins.RecognitionProtocolSHA256(), onlinebulletins.RecognitionDeployment, onlinebulletins.RecognitionModelVersion, mustJSON(job.Canonical.Metadata)).Scan(&count, &reused)
	return count, err
}

func (r *Repository) CompleteRecognitionPage(ctx context.Context, job onlinebulletins.ExtractionJob, input onlinebulletins.RecognitionPage, result onlinebulletins.RecognitionResult, reused bool, now time.Time) error {
	if !validProductionJob(job, now) {
		return onlinebulletins.ErrExtractionJob
	}
	if _, _, err := progressInput(job, input); err != nil {
		return err
	}
	proof, err := onlinebulletins.VerifiedRecognitionProgress(input, result)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockRecognitionCheckpointLease(ctx, tx, job, now); err != nil {
		return err
	}
	before, err := productionPrefix(ctx, tx, job)
	if err != nil {
		return err
	}
	var matched bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_recognition_checkpoint c JOIN hhc_web.online_bulletin_extraction_job j ON j.id=c.job_id WHERE c.job_id=$1 AND c.source_page=$2 AND c.phase='verified' AND c.input_sha256=$3 AND c.protocol_sha256=$4 AND c.deployment=$5 AND c.model_version=$6 AND c.checkpoint_json->'canonical'=$7::jsonb AND c.checkpoint_json->'result'=$8::jsonb AND COALESCE(c.checkpoint_json->>'errorCode','')='' AND (c.checkpoint_json#>>'{totalUsage,Requests}')::integer>=2 AND j.production_total_pages=$9 AND j.production_canonical=$7::jsonb AND j.production_protocol_sha256=$4)`, job.ID, input.Page, proof.InputSHA256, onlinebulletins.RecognitionProtocolSHA256(), onlinebulletins.RecognitionDeployment, onlinebulletins.RecognitionModelVersion, mustJSON(job.Canonical.Metadata), mustJSON(result), input.TotalPages).Scan(&matched); err != nil {
		return err
	}
	if !matched {
		return onlinebulletins.ErrExtractionJob
	}
	if input.Page > 1 {
		var next string
		err = tx.QueryRowContext(ctx, `SELECT next_sha256 FROM hhc_web.online_bulletin_production_page WHERE job_id=$1 AND source_page=$2`, job.ID, input.Page-1).Scan(&next)
		if err != nil || next != proof.PreviousSHA256 || before < input.Page-1 {
			return onlinebulletins.ErrExtractionJob
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.online_bulletin_production_page WHERE job_id=$1 AND source_page>=$2 AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_production_page p WHERE p.job_id=$1 AND p.source_page=$2 AND (p.input_sha256<>$3 OR p.next_sha256<>$4))`, job.ID, input.Page, proof.InputSHA256, proof.NextSHA256); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_production_page(job_id,source_page,input_sha256,semantic_sha256,previous_sha256,next_sha256,reused,verified_at,result_sha256) VALUES($1,$2,$3,$4,$5,$6,$7,$8,encode(sha256(convert_to(($9::jsonb)::text,'UTF8')),'hex')) ON CONFLICT(job_id,source_page) DO UPDATE SET input_sha256=EXCLUDED.input_sha256,semantic_sha256=EXCLUDED.semantic_sha256,result_sha256=EXCLUDED.result_sha256,previous_sha256=EXCLUDED.previous_sha256,next_sha256=EXCLUDED.next_sha256,reused=EXCLUDED.reused,verified_at=EXCLUDED.verified_at`, job.ID, input.Page, proof.InputSHA256, proof.SemanticSHA256, proof.PreviousSHA256, proof.NextSHA256, reused, now, mustJSON(result)); err != nil {
		return err
	}
	after, err := productionPrefix(ctx, tx, job)
	if err != nil {
		return err
	}
	subphase := "verified"
	if reused {
		subphase = "checkpoint_reuse"
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET production_current_page=NULL,production_subphase=$2,production_phase=CASE WHEN $3=production_total_pages THEN 'reconciliation' ELSE 'recognition' END,production_heartbeat_at=$4,production_last_progress_at=CASE WHEN $3>$5 THEN $4 ELSE production_last_progress_at END WHERE id=$1`, job.ID, subphase, after, now, before)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func recordProductionSnapshotReuse(ctx context.Context, tx *sql.Tx, job onlinebulletins.ExtractionJob, total int, hash string, now time.Time) error {
	if total < 4 || total > 40 || !recognitionCheckpointHash.MatchString(hash) {
		return onlinebulletins.ErrExtractionJob
	}
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET production_total_pages=$2,production_canonical=$3,production_protocol_sha256=$4,production_current_page=NULL,production_phase='reconciliation',production_subphase='snapshot_reuse',production_heartbeat_at=$5,production_last_progress_at=$5,production_reused_snapshot_sha256=$6 WHERE id=$1`, job.ID, total, mustJSON(job.Canonical.Metadata), onlinebulletins.RecognitionProtocolSHA256(), now, hash)
	return err
}
