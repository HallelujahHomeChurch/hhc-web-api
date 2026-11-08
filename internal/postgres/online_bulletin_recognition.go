package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

var recognitionCheckpointHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validRecognitionCheckpoint(checkpoint onlinebulletins.RecognitionCheckpoint) bool {
	return checkpoint.Page >= 1 && checkpoint.Page <= 40 && (checkpoint.Phase == "recognition" || checkpoint.Phase == "verification" || checkpoint.Phase == "verified") && recognitionCheckpointHash.MatchString(checkpoint.InputSHA256) && recognitionCheckpointHash.MatchString(checkpoint.ProtocolSHA256) && len(checkpoint.Deployment) > 0 && len(checkpoint.Deployment) <= 128 && len(checkpoint.ModelVersion) > 0 && len(checkpoint.ModelVersion) <= 128 && (checkpoint.ErrorCode == "" || extractionErrorCode.MatchString(checkpoint.ErrorCode)) && checkpoint.Usage.InputTokens >= 0 && checkpoint.Usage.OutputTokens >= 0 && checkpoint.Usage.ReasoningTokens >= 0 && checkpoint.Usage.ReasoningTokens <= checkpoint.Usage.OutputTokens
}

func lockRecognitionCheckpointLease(ctx context.Context, tx *sql.Tx, job onlinebulletins.ExtractionJob, now time.Time) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT j.id::text FROM hhc_web.online_bulletin_extraction_job j
 JOIN hhc_web.bulletin_version v ON v.issue_id=j.issue_id AND v.series=j.series AND v.locale=j.content_locale
 JOIN hhc_web.bulletin_issue i ON i.id=j.issue_id
 WHERE j.id=$1 AND j.lease_token=$2 AND j.lease_until>$3 AND j.status='parsing' AND NOT j.superseded
 AND j.source_asset_id=$4 AND j.source_asset_checksum=$5 AND v.pdf_asset_id=j.source_asset_id AND v.pdf_sha256=j.source_asset_checksum
 AND jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)=$6::jsonb FOR UPDATE OF j`, job.ID, job.LeaseToken, now, job.SourceAssetID, job.SourceAssetChecksum, mustJSON(job.Canonical.Metadata)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return onlinebulletins.ErrExtractionLease
	}
	return err
}

func (r *Repository) LoadRecognitionCheckpoint(ctx context.Context, job onlinebulletins.ExtractionJob, key onlinebulletins.RecognitionCheckpoint, now time.Time) (*onlinebulletins.RecognitionCheckpoint, error) {
	if !validRecognitionCheckpoint(key) || now.IsZero() {
		return nil, onlinebulletins.ErrExtractionJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = lockRecognitionCheckpointLease(ctx, tx, job, now); err != nil {
		return nil, err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT checkpoint_json FROM hhc_web.online_bulletin_recognition_checkpoint WHERE job_id=$1 AND source_page=$2 AND phase='verified'`, job.ID, key.Page).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	var saved onlinebulletins.RecognitionCheckpoint
	if json.Unmarshal(raw, &saved) != nil || saved.Page != key.Page || saved.InputSHA256 != key.InputSHA256 || saved.ProtocolSHA256 != key.ProtocolSHA256 || saved.Deployment != key.Deployment || saved.ModelVersion != key.ModelVersion || saved.Canonical != job.Canonical.Metadata || !recognitionCheckpointHash.MatchString(saved.Result.SemanticSHA256) || !validRecognitionCheckpoint(saved) {
		return nil, onlinebulletins.ErrExtractionJob
	}
	return &saved, tx.Commit()
}

func (r *Repository) SaveRecognitionCheckpoint(ctx context.Context, job onlinebulletins.ExtractionJob, checkpoint onlinebulletins.RecognitionCheckpoint, now time.Time) error {
	if checkpoint.Canonical != job.Canonical.Metadata || !validRecognitionCheckpoint(checkpoint) || now.IsZero() || checkpoint.Phase == "verified" && (checkpoint.ErrorCode != "" || !recognitionCheckpointHash.MatchString(checkpoint.Result.SemanticSHA256) || checkpoint.Result.Page != checkpoint.Page || len(checkpoint.Result.UnassignedIDs) != 0) {
		return onlinebulletins.ErrExtractionJob
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil || len(raw) > 4<<20 {
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
	result, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_recognition_checkpoint(id,job_id,source_page,phase,input_sha256,protocol_sha256,deployment,model_version,checkpoint_json,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(job_id,source_page) WHERE phase='verified' DO NOTHING`, platform.NewID(), job.ID, checkpoint.Page, checkpoint.Phase, checkpoint.InputSHA256, checkpoint.ProtocolSHA256, checkpoint.Deployment, checkpoint.ModelVersion, raw, now)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		var same bool
		if err = tx.QueryRowContext(ctx, `SELECT checkpoint_json=$3::jsonb FROM hhc_web.online_bulletin_recognition_checkpoint WHERE job_id=$1 AND source_page=$2 AND phase='verified'`, job.ID, checkpoint.Page, raw).Scan(&same); err != nil {
			return err
		}
		if !same {
			return onlinebulletins.ErrExtractionJob
		}
	}
	return tx.Commit()
}
