package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func (r *Repository) ClaimOnlineBulletinConversion(ctx context.Context, now time.Time, lease time.Duration, maxAttempts int) (*onlinebulletins.ConversionJob, error) {
	if now.IsZero() || lease <= 0 || lease > onlinebulletins.UpdateJobTimeout || maxAttempts < 1 || maxAttempts > 10 {
		return nil, onlinebulletins.ErrExtractionJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_conversion_job SET status='failed',last_error_code='attempt_limit',lease_token=NULL,lease_until=NULL,updated_at=$1 WHERE (attempts >= CASE WHEN publication_event_id IS NULL THEN $2 ELSE LEAST($2,3) END OR publication_event_id IS NOT NULL AND jsonb_array_length(assessment_json)>=6) AND (status='queued' OR (status='converting' AND lease_until<=$1))`, now, maxAttempts); err != nil {
		return nil, err
	}
	var job onlinebulletins.ConversionJob
	var document, source, target, assessment []byte
	err = tx.QueryRowContext(ctx, `WITH next AS (
 SELECT id FROM hhc_web.online_bulletin_conversion_job WHERE attempts<CASE WHEN publication_event_id IS NULL THEN $4 ELSE LEAST($4,3) END AND (publication_event_id IS NULL OR jsonb_array_length(assessment_json)<6) AND available_at<=$1 AND (status='queued' OR (status='converting' AND lease_until<=$1)) ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
 ), claimed AS (UPDATE hhc_web.online_bulletin_conversion_job j SET status='converting',attempts=j.attempts+1,lease_until=CASE WHEN publication_event_id IS NULL THEN LEAST($2,$1+interval '120 seconds') ELSE $2 END,lease_token=$3,last_error_code='',updated_at=$1 FROM next WHERE j.id=next.id RETURNING j.*)
 SELECT j.id::text,j.document_id::text,j.issue_id::text,j.lease_token::text,j.lease_until,j.attempts,j.converter_version,r.document_json,j.source_metadata,j.target_metadata,j.initializes_metadata,COALESCE(j.publication_event_id::text,''),j.assessment_json
 FROM claimed j JOIN hhc_web.online_bulletin_revision r ON r.document_id=j.source_document_id AND r.revision=j.source_revision`, now, now.Add(lease), platform.NewID(), maxAttempts).Scan(&job.ID, &job.DocumentID, &job.IssueID, &job.LeaseToken, &job.LeaseUntil, &job.Attempts, &job.ConverterVersion, &document, &source, &target, &job.InitializesMetadata, &job.PublicationEventID, &assessment)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(document, &job.Source) != nil || json.Unmarshal(source, &job.SourceMetadata) != nil || json.Unmarshal(target, &job.TargetMetadata) != nil {
		return nil, onlinebulletins.ErrExtractionJob
	}
	if json.Unmarshal(assessment, &job.AssessmentAttempts) != nil {
		return nil, onlinebulletins.ErrUpdatePlan
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *Repository) FailOnlineBulletinConversion(ctx context.Context, job onlinebulletins.ConversionJob, code string, issues []onlinebulletins.ReviewIssue, now time.Time, maxAttempts int) error {
	if !extractionErrorCode.MatchString(code) || now.IsZero() || maxAttempts < 1 || maxAttempts > 10 {
		return onlinebulletins.ErrExtractionJob
	}
	raw, err := marshalLayoutIssues(issues, job.Source.SourcePageCount)
	if err != nil {
		return onlinebulletins.ErrExtractionJob
	}
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_conversion_job SET status=CASE WHEN attempts >= $5 THEN 'failed' ELSE 'queued' END,last_error_code=$3,review_issues=$6,available_at=$4+interval '1 minute',lease_token=NULL,lease_until=NULL,updated_at=$4 WHERE id=$1 AND lease_token=$2 AND status='converting' AND lease_until>$4`, job.ID, job.LeaseToken, code, now, maxAttempts, raw)
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

func (r *Repository) CompleteOnlineBulletinConversion(ctx context.Context, job onlinebulletins.ConversionJob, draft onlinebulletins.Document, converted onlinebulletins.CanonicalMetadata, now time.Time) error {
	if now.IsZero() || draft.IssueID != job.IssueID || draft.Series != "general" || draft.ContentLocale != "zh-Hans" || draft.TemplateVersion != "v2" || draft.SourceAssetChecksum != job.Source.SourceAssetChecksum || draft.SourcePageCount != job.Source.SourcePageCount || converted.IssueNumber != job.SourceMetadata.IssueNumber || converted.Date != job.SourceMetadata.Date || converted.Title == "" {
		return onlinebulletins.ErrExtractionJob
	}
	draft.LayoutManifest.ContentHash, draft.LayoutManifest.LayoutValidationHash = "", ""
	issues, err := onlinebulletins.ValidateDocument(draft)
	if err != nil {
		return onlinebulletins.ErrExtractionJob
	}
	raw, err := json.Marshal(draft)
	if err != nil {
		return err
	}
	convertedJSON, err := json.Marshal(converted)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var canonicalVersion, sourceVersion, sourceRevision, targetVersion int64
	var sourceMetadata, targetMetadata onlinebulletins.CanonicalMetadata
	var sourceID, sourceHash string
	var sourceConfirmed bool
	err = tx.QueryRowContext(ctx, simplifiedConversionSourceSQL, job.IssueID, job.PublicationEventID != "").Scan(&canonicalVersion, &sourceID, &sourceVersion, &sourceRevision, &sourceHash, &sourceMetadata.Title, &sourceMetadata.Subtitle, &sourceMetadata.IssueNumber, &sourceMetadata.Date, &sourceConfirmed)
	if errors.Is(err, sql.ErrNoRows) && job.PublicationEventID != "" {
		return onlinebulletins.ErrConversionStale
	}
	if err != nil {
		return err
	}
	var local sql.NullInt64
	targetMetadata.IssueNumber, targetMetadata.Date = sourceMetadata.IssueNumber, sourceMetadata.Date
	err = tx.QueryRowContext(ctx, `SELECT d.version,d.local_revision,v.title,v.subtitle FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_version v ON v.issue_id=d.issue_id AND v.series=d.series AND v.locale=d.content_locale WHERE d.id=$1 AND d.issue_id=$2 AND d.series='general' AND d.content_locale='zh-Hans' FOR UPDATE OF d,v`, job.DocumentID, job.IssueID).Scan(&targetVersion, &local, &targetMetadata.Title, &targetMetadata.Subtitle)
	if err != nil {
		return err
	}
	var expectedSourceVersion, expectedSourceRevision, expectedCanonical, expectedTarget int64
	var savedSource, savedTarget, assessmentJSON []byte
	var expectedSourceID, expectedHash, status, converter, actor, publicationEventID string
	var token sql.NullString
	var until sql.NullTime
	var initializes bool
	err = tx.QueryRowContext(ctx, `SELECT source_document_id::text,source_revision,source_online_version,source_content_hash,source_metadata,canonical_version,target_online_version,target_metadata,status,lease_token::text,lease_until,initializes_metadata,converter_version,created_by,COALESCE(publication_event_id::text,''),assessment_json FROM hhc_web.online_bulletin_conversion_job WHERE id=$1 AND document_id=$2 FOR UPDATE`, job.ID, job.DocumentID).Scan(&expectedSourceID, &expectedSourceRevision, &expectedSourceVersion, &expectedHash, &savedSource, &expectedCanonical, &expectedTarget, &savedTarget, &status, &token, &until, &initializes, &converter, &actor, &publicationEventID, &assessmentJSON)
	if err != nil {
		return err
	}
	if status == "review_ready" {
		var same bool
		if err = tx.QueryRowContext(ctx, `SELECT r.document_json=$2::jsonb AND j.converted_metadata=$3::jsonb FROM hhc_web.online_bulletin_conversion_job j JOIN hhc_web.online_bulletin_revision r ON r.document_id=j.document_id AND r.revision=j.result_revision WHERE j.id=$1`, job.ID, raw, convertedJSON).Scan(&same); err != nil {
			return err
		}
		if !same {
			return bulletins.ErrConflict
		}
		return tx.Commit()
	}
	if publicationEventID != job.PublicationEventID || status != "converting" || token.String != job.LeaseToken || !until.Valid || !until.Time.After(now) {
		return onlinebulletins.ErrExtractionLease
	}
	var expectedSource, expectedMetadata onlinebulletins.CanonicalMetadata
	if json.Unmarshal(savedSource, &expectedSource) != nil || json.Unmarshal(savedTarget, &expectedMetadata) != nil {
		return onlinebulletins.ErrExtractionJob
	}
	// Publication changes the counter, not the immutable, confirmed conversion source.
	if sourceID != expectedSourceID || sourceRevision != expectedSourceRevision || sourceVersion < expectedSourceVersion || sourceHash != expectedHash || !sourceConfirmed || (job.PublicationEventID == "" && canonicalVersion != expectedCanonical) || targetVersion != expectedTarget || sourceMetadata != expectedSource || targetMetadata != expectedMetadata || converter != onlinebulletins.SimplifiedConverterVersion {
		return errors.Join(bulletins.ErrPrecondition, onlinebulletins.ErrConversionStale)
	}
	if job.PublicationEventID != "" {
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.outbox_event WHERE id=$1 AND event_type='bulletin.online.publish_simplified' AND status IN ('pending','processing'))`, job.PublicationEventID).Scan(&active); err != nil {
			return err
		}
		if !active {
			return onlinebulletins.ErrConversionStale
		}
	}
	useConvertedMetadata := initializes && !local.Valid
	if r.llmBulletinExtraction && publicationEventID != "" {
		input, err := onlinebulletins.SimplifiedAssessmentInput(job.Source, sourceMetadata, draft, converted)
		if err != nil {
			return err
		}
		if !initializes {
			input.LocalMetadata = &expectedMetadata
		}
		var attempts []onlinebulletins.UpdateAttempt
		if json.Unmarshal(assessmentJSON, &attempts) != nil {
			return onlinebulletins.ErrUpdatePlan
		}
		proof, err := onlinebulletins.CompletedUpdateEvidence(input, successfulAssessmentAttempts(attempts))
		if err != nil {
			return err
		}
		useConvertedMetadata = proof.Plan.MetadataChoice != "local"
	}
	if useConvertedMetadata {
		targetMetadata = converted
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET title=$2,subtitle=$3,version=version+1,updated_at=$4 WHERE issue_id=$1 AND series='general' AND locale='zh-Hans'`, job.IssueID, converted.Title, converted.Subtitle, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET version=version+1,updated_at=$2 WHERE id=$1`, job.IssueID, now); err != nil {
			return err
		}
	}
	issues = append(issues, onlinebulletins.ReviewIssue{Code: "layout_validation_required", Blocking: true})
	issueJSON, _ := json.Marshal(issues)
	_, contentHash, _ := onlinebulletins.MarshalLayoutSubmission(draft, targetMetadata)
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(revision),0)+1 FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, job.DocumentID).Scan(&revision); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,review_issues,created_by,created_at) VALUES($1,$2,'general','zh-Hans',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, job.DocumentID, job.IssueID, revision, raw, draft.SourceAssetChecksum, draft.TemplateVersion, converter, contentHash, draft.LayoutManifest.RendererVersion, draft.LayoutManifest.RendererArtifactSha256, issueJSON, actor, now); err != nil {
		return err
	}
	// Each conversion replaces only the working copy. Immutable history and the
	// published revision remain available until a separate publication succeeds.
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET base_revision=$2,local_revision=$2,incoming_revision=NULL WHERE id=$1`, job.DocumentID, revision); err != nil {
		return err
	}
	if _, err = queueOnlineLayout(ctx, tx, job.DocumentID, revision, draft, targetMetadata, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_conversion_job SET status='review_ready',result_revision=$2,converted_metadata=$3,lease_token=NULL,lease_until=NULL,updated_at=$4 WHERE id=$1`, job.ID, revision, convertedJSON, now); err != nil {
		return err
	}
	return tx.Commit()
}
