package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

var extractionErrorCode = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// Caller holds the issue lock; the FK remains the final concurrent-delete guard.
func rejectOnlineHistoryDeletion(ctx context.Context, tx *sql.Tx, issueID, series, locale string) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND ($2='' OR (series=$2 AND content_locale=$3)))`, issueID, series, locale).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return bulletins.ErrOnlineDocumentExists
	}
	return nil
}

func (r *Repository) ClaimOnlineBulletinExtraction(ctx context.Context, now time.Time, lease time.Duration, maxAttempts int) (*onlinebulletins.ExtractionJob, error) {
	if now.IsZero() || lease <= 0 || lease > onlinebulletins.RecognitionJobTimeout || maxAttempts < 1 || maxAttempts > 10 {
		return nil, onlinebulletins.ErrExtractionJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET status='failed',last_error_code='attempt_limit',lease_token=NULL,lease_until=NULL,updated_at=$1 WHERE NOT superseded AND attempts >= $2 AND (status='queued' OR (status='parsing' AND lease_until <= $1))`, now, maxAttempts); err != nil {
		return nil, err
	}
	var job onlinebulletins.ExtractionJob
	var templateSnapshot, learningRules []byte
	err = tx.QueryRowContext(ctx, `
		WITH next AS (
		 SELECT id FROM hhc_web.online_bulletin_extraction_job
		 WHERE NOT superseded AND attempts < $4 AND available_at <= $1
		 AND (status='queued' OR (status='parsing' AND lease_until <= $1))
		 ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
		), claimed AS (
		 UPDATE hhc_web.online_bulletin_extraction_job j SET status='parsing',attempts=j.attempts+1,
		 lease_until=CASE WHEN j.extractor_version=$5 THEN $2 ELSE LEAST($2,$1+interval '120 seconds') END,lease_token=$3,last_error_code='',updated_at=$1 FROM next WHERE j.id=next.id RETURNING j.*
		)
		SELECT j.id::text,j.document_id::text,j.lease_token::text,j.lease_until,j.source_asset_id,j.source_asset_checksum,
		j.template_version,j.extractor_version,j.attempts,j.issue_id::text,j.series,j.content_locale,
		v.title,v.subtitle,i.issue_number,i.issue_date::text,j.template_snapshot_json,j.learning_rules_json
		FROM claimed j JOIN hhc_web.bulletin_issue i ON i.id=j.issue_id
		JOIN hhc_web.bulletin_version v ON v.issue_id=j.issue_id AND v.series=j.series AND v.locale=j.content_locale`,
		now, now.Add(lease), platform.NewID(), maxAttempts, onlinebulletins.RecognitionExtractorVersion()).Scan(&job.ID, &job.DocumentID, &job.LeaseToken, &job.LeaseUntil, &job.SourceAssetID, &job.SourceAssetChecksum, &job.TemplateVersion, &job.ExtractorVersion, &job.Attempts, &job.Canonical.IssueID, &job.Canonical.Series, &job.Canonical.ContentLocale, &job.Canonical.Metadata.Title, &job.Canonical.Metadata.Subtitle, &job.Canonical.Metadata.IssueNumber, &job.Canonical.Metadata.Date, &templateSnapshot, &learningRules)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if len(templateSnapshot) > 0 {
		if json.Unmarshal(templateSnapshot, &job.TemplateSnapshot) != nil || job.TemplateSnapshot == nil || job.TemplateSnapshot.Version < 1 {
			return nil, onlinebulletins.ErrExtractionJob
		}
		if normalized, ok := onlinebulletins.NormalizeTemplateText(job.TemplateSnapshot.TemplateText); !ok || normalized != job.TemplateSnapshot.TemplateText {
			return nil, onlinebulletins.ErrExtractionJob
		}
	}
	job.LearningRules, err = onlinebulletins.DecodeLearningRules(learningRules)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *Repository) FailOnlineBulletinExtraction(ctx context.Context, job onlinebulletins.ExtractionJob, code string, now time.Time, maxAttempts int) error {
	if !extractionErrorCode.MatchString(code) || now.IsZero() || maxAttempts < 1 || maxAttempts > 10 {
		return onlinebulletins.ErrExtractionJob
	}
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job
		SET status=CASE WHEN extractor_version=$5 AND $3='extraction_timeout' AND attempts<$6 THEN 'queued' ELSE 'failed' END,
		last_error_code=$3,available_at=CASE WHEN extractor_version=$5 AND $3='extraction_timeout' AND attempts<$6 THEN $4::timestamptz+interval '10 seconds' ELSE $4 END,lease_token=NULL,lease_until=NULL,updated_at=$4
		WHERE id=$1 AND lease_token=$2 AND status='parsing' AND lease_until>$4 AND NOT superseded`, job.ID, job.LeaseToken, code, now, onlinebulletins.RecognitionExtractorVersion(), maxAttempts)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return onlinebulletins.ErrExtractionLease
	}
	return nil
}

// The candidate is produced only by the privacy-filtered extractor, never by
// an Admin request body. Unsupported sources retain evidence without inventing
// a publishable document or overwriting an existing Local revision.
func (r *Repository) CompleteOnlineBulletinExtraction(ctx context.Context, job onlinebulletins.ExtractionJob, candidate onlinebulletins.DocumentCandidate, issues []onlinebulletins.ReviewIssue, now time.Time) error {
	if now.IsZero() || candidate.Evidence.SourceSHA256 != job.SourceAssetChecksum {
		return onlinebulletins.ErrExtractionJob
	}
	if job.ExtractorVersion == onlinebulletins.RecognitionExtractorVersion() && (candidate.Document == nil || !onlinebulletins.ValidRecognitionEvidence(candidate.Recognition, *candidate.Document)) {
		return onlinebulletins.ErrExtractionJob
	}
	var documentJSON []byte
	var contentHash string
	if candidate.Document != nil {
		document := *candidate.Document
		if (document.LayoutManifest.RendererVersion == "v9" || document.LayoutManifest.RendererVersion == "v10" || document.LayoutManifest.RendererVersion == "v11") && (document.TemplateSnapshot == nil || job.TemplateSnapshot == nil || *document.TemplateSnapshot != *job.TemplateSnapshot) {
			return onlinebulletins.ErrExtractionJob
		}
		if document.IssueID != job.Canonical.IssueID || document.Series != job.Canonical.Series || document.ContentLocale != job.Canonical.ContentLocale || document.SourceAssetChecksum != job.SourceAssetChecksum || document.TemplateVersion != job.TemplateVersion {
			return onlinebulletins.ErrExtractionJob
		}
		document.LayoutManifest.ContentHash = ""
		document.LayoutManifest.LayoutValidationHash = ""
		validation, err := onlinebulletins.ValidateDocument(document)
		if err != nil {
			return onlinebulletins.ErrExtractionJob
		}
		issues = append(append([]onlinebulletins.ReviewIssue{}, issues...), validation...)
		candidate.Document = &document
		documentJSON, err = json.Marshal(document)
		if err != nil || len(documentJSON) > onlinebulletins.MaxDocumentBytes {
			return onlinebulletins.ErrExtractionJob
		}
		_, contentHash, _ = onlinebulletins.MarshalLayoutSubmission(document, job.Canonical.Metadata)
	}
	if issues == nil {
		issues = []onlinebulletins.ReviewIssue{}
	}
	seen := make(map[onlinebulletins.ReviewIssue]bool, len(issues))
	unique := make([]onlinebulletins.ReviewIssue, 0, len(issues))
	for _, issue := range issues {
		if !seen[issue] {
			unique = append(unique, issue)
			seen[issue] = true
		}
	}
	issues = unique
	if len(issues) > 4096 {
		return onlinebulletins.ErrExtractionJob
	}
	issueJSON, err := json.Marshal(issues)
	if err != nil {
		return onlinebulletins.ErrExtractionJob
	}
	snapshot, err := json.Marshal(onlineExtractionSnapshot{candidate, job.Canonical.Metadata, issues})
	if err != nil || len(snapshot) > 16<<20 {
		return onlinebulletins.ErrExtractionJob
	}
	digest := sha256.Sum256(snapshot)
	snapshotHash := hex.EncodeToString(digest[:])
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current onlinebulletins.CanonicalMetadata
	var asset, checksum string
	err = tx.QueryRowContext(ctx, `SELECT v.title,v.subtitle,i.issue_number,i.issue_date::text,v.pdf_asset_id,v.pdf_sha256 FROM hhc_web.bulletin_version v JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id WHERE v.issue_id=$1 AND v.series=$2 AND v.locale=$3 FOR UPDATE OF v,i`, job.Canonical.IssueID, job.Canonical.Series, job.Canonical.ContentLocale).Scan(&current.Title, &current.Subtitle, &current.IssueNumber, &current.Date, &asset, &checksum)
	if err != nil {
		return err
	}
	var status string
	var token, savedHash sql.NullString
	var until sql.NullTime
	var templateSnapshot, learningRules []byte
	err = tx.QueryRowContext(ctx, `SELECT status,lease_token::text,lease_until,snapshot_sha256,template_snapshot_json,learning_rules_json FROM hhc_web.online_bulletin_extraction_job WHERE id=$1 AND document_id=$2 AND source_asset_id=$3 AND source_asset_checksum=$4 AND NOT superseded FOR UPDATE`, job.ID, job.DocumentID, job.SourceAssetID, job.SourceAssetChecksum).Scan(&status, &token, &until, &savedHash, &templateSnapshot, &learningRules)
	if errors.Is(err, sql.ErrNoRows) {
		return onlinebulletins.ErrExtractionLease
	}
	if err != nil {
		return err
	}
	frozenRules, decodeErr := onlinebulletins.DecodeLearningRules(learningRules)
	if decodeErr != nil || !reflect.DeepEqual(frozenRules, job.LearningRules) || !onlinebulletins.ValidLearningApplications(candidate, frozenRules) {
		return onlinebulletins.ErrExtractionJob
	}
	if candidate.Document != nil && (candidate.Document.LayoutManifest.RendererVersion == "v9" || candidate.Document.LayoutManifest.RendererVersion == "v10" || candidate.Document.LayoutManifest.RendererVersion == "v11") {
		var frozen onlinebulletins.TemplateSettings
		if json.Unmarshal(templateSnapshot, &frozen) != nil || frozen != *candidate.Document.TemplateSnapshot {
			return onlinebulletins.ErrExtractionJob
		}
	}
	if status == "review_ready" && savedHash.String == snapshotHash {
		return tx.Commit()
	}
	if status != "parsing" || token.String != job.LeaseToken || !until.Valid || !until.Time.After(now) || asset != job.SourceAssetID || checksum != job.SourceAssetChecksum || current != job.Canonical.Metadata {
		return onlinebulletins.ErrExtractionLease
	}
	var base, local sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT base_revision,local_revision FROM hhc_web.online_bulletin_document WHERE id=$1 FOR UPDATE`, job.DocumentID).Scan(&base, &local); err != nil {
		return err
	}
	var revision any
	if candidate.Document != nil {
		if base.Valid {
			var raw []byte
			if err = tx.QueryRowContext(ctx, `SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, job.DocumentID, base.Int64).Scan(&raw); err != nil {
				return err
			}
			var previous onlinebulletins.Document
			if err = json.Unmarshal(raw, &previous); err != nil {
				return err
			}
			normalized, identities, reconcileErr := onlinebulletins.ReconcileExtraction(previous, *candidate.Document)
			if reconcileErr != nil {
				return reconcileErr
			}
			candidate.Document = &normalized
			documentJSON, err = json.Marshal(normalized)
			if err != nil || len(documentJSON) > onlinebulletins.MaxDocumentBytes {
				return onlinebulletins.ErrExtractionJob
			}
			// Keep the raw snapshot/hash above for source evidence and retry
			// idempotency. Only the authored revision receives stable identities.
			for i := range issues {
				if id, exists := identities.Components[issues[i].ComponentID]; exists {
					issues[i].ComponentID = id
				}
				if id, exists := identities.Blocks[issues[i].BlockID]; exists {
					issues[i].BlockID = id
				}
				if id, exists := identities.Sentences[issues[i].SentenceID]; exists {
					issues[i].SentenceID = id
				}
			}
			issueJSON, err = json.Marshal(issues)
			if err != nil {
				return err
			}
			_, contentHash, _ = onlinebulletins.MarshalLayoutSubmission(normalized, job.Canonical.Metadata)
		}
		var next int64
		if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(revision),0)+1 FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, job.DocumentID).Scan(&next); err != nil {
			return err
		}
		revision = next
		_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,review_issues,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'bulletin-extractor')`, job.DocumentID, job.Canonical.IssueID, job.Canonical.Series, job.Canonical.ContentLocale, next, documentJSON, job.SourceAssetChecksum, job.TemplateVersion, job.ExtractorVersion, contentHash, candidate.Document.LayoutManifest.RendererVersion, candidate.Document.LayoutManifest.RendererArtifactSha256, issueJSON)
		if err != nil {
			return err
		}
		if !local.Valid {
			_, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET base_revision=$2,local_revision=$2,updated_at=$3 WHERE id=$1`, job.DocumentID, next, now)
			if err == nil {
				_, err = queueOnlineLayout(ctx, tx, job.DocumentID, next, *candidate.Document, job.Canonical.Metadata, now)
			}
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET incoming_revision=$2,version=version+1,updated_at=$3 WHERE id=$1`, job.DocumentID, next, now)
		}
		if err != nil {
			return err
		}
	} else if !local.Valid {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET draft_status='reviewRequired',version=version+1,updated_at=$2 WHERE id=$1`, job.DocumentID, now); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET status='review_ready',snapshot_json=$2,snapshot_sha256=$3,result_revision=$4,lease_token=NULL,lease_until=NULL,updated_at=$5 WHERE id=$1`, job.ID, snapshot, snapshotHash, revision, now)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_recognition_checkpoint SET checkpoint_json=checkpoint_json-'result' WHERE job_id=$1`, job.ID); err != nil {
		return err
	}
	return tx.Commit()
}
