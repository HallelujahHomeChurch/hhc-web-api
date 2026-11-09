package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

// Reuse immutable source proof, never the previous asset's authority or lease.
// Authored state and annotations are reassessed by the new update job.
func (r *Repository) LoadReusableRecognition(ctx context.Context, job onlinebulletins.ExtractionJob, now time.Time) (*onlinebulletins.DocumentCandidate, []onlinebulletins.ReviewIssue, error) {
	if !r.llmBulletinExtraction || job.ExtractorVersion != onlinebulletins.RecognitionExtractorVersion() || job.TemplateSnapshot == nil || now.IsZero() {
		return nil, nil, onlinebulletins.ErrExtractionJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	if err = lockRecognitionCheckpointLease(ctx, tx, job, now); err != nil {
		return nil, nil, err
	}
	var id, hash string
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT id::text,snapshot_json,snapshot_sha256 FROM hhc_web.online_bulletin_extraction_job WHERE id<>$1 AND issue_id=$2 AND series=$3 AND content_locale=$4 AND source_asset_checksum=$5 AND extractor_version=$6 AND template_snapshot_json=$7::jsonb AND status='review_ready' AND snapshot_json->'canonicalMetadata'=$8::jsonb ORDER BY created_at DESC,id DESC LIMIT 1`, job.ID, job.Canonical.IssueID, job.Canonical.Series, job.Canonical.ContentLocale, job.SourceAssetChecksum, job.ExtractorVersion, mustJSON(job.TemplateSnapshot), mustJSON(job.Canonical.Metadata)).Scan(&id, &raw, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, tx.Commit()
	}
	if err != nil {
		return nil, nil, err
	}
	var snapshot onlineExtractionSnapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return nil, nil, tx.Commit()
	}
	digest := sha256.Sum256(mustJSON(snapshot))
	candidate := snapshot.Candidate
	if hex.EncodeToString(digest[:]) != hash || candidate.Document == nil || !onlinebulletins.ValidRecognitionEvidence(candidate.Recognition, *candidate.Document) || candidate.Document.IssueID != job.Canonical.IssueID || candidate.Document.Series != job.Canonical.Series || candidate.Document.ContentLocale != job.Canonical.ContentLocale || candidate.Document.SourceAssetChecksum != job.SourceAssetChecksum || candidate.Evidence.SourceSHA256 != job.SourceAssetChecksum || !reflect.DeepEqual(candidate.Document.TemplateSnapshot, job.TemplateSnapshot) {
		return nil, nil, tx.Commit()
	}
	for _, issue := range snapshot.Issues {
		if issue.Code != "layout_validation_required" {
			return nil, nil, tx.Commit()
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET recognition_reused_from=$2 WHERE id=$1`, job.ID, id); err != nil {
		return nil, nil, err
	}
	return &candidate, snapshot.Issues, tx.Commit()
}
