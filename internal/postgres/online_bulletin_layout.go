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

// Queue only the current saved Local. The immutable source revision and canonical
// snapshot are the job's authority, not an Admin-supplied document or proof.
func (r *Repository) QueueOnlineBulletinLayout(ctx context.Context, documentID string, expectedVersion int64, now time.Time) (string, error) {
	if expectedVersion < 1 || now.IsZero() {
		return "", onlinebulletins.ErrLayoutJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var revision, version int64
	var documentJSON []byte
	var metadata onlinebulletins.CanonicalMetadata
	err = tx.QueryRowContext(ctx, `SELECT d.version,d.local_revision,r.document_json,v.title,v.subtitle,i.issue_number,i.issue_date::text
	 FROM hhc_web.online_bulletin_document d
	 JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
	 JOIN hhc_web.bulletin_version v ON v.issue_id=d.issue_id AND v.series=d.series AND v.locale=d.content_locale
	 JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision
	 WHERE d.id=$1 FOR UPDATE OF v,i,d`, documentID).Scan(&version, &revision, &documentJSON, &metadata.Title, &metadata.Subtitle, &metadata.IssueNumber, &metadata.Date)
	if errors.Is(err, sql.ErrNoRows) {
		return "", bulletins.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if version != expectedVersion {
		return "", bulletins.ErrPrecondition
	}
	var document onlinebulletins.Document
	if json.Unmarshal(documentJSON, &document) != nil {
		return "", onlinebulletins.ErrLayoutJob
	}
	id, err := queueOnlineLayout(ctx, tx, documentID, revision, document, metadata, now)
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func queueOnlineLayout(ctx context.Context, tx *sql.Tx, documentID string, revision int64, document onlinebulletins.Document, metadata onlinebulletins.CanonicalMetadata, now time.Time) (string, error) {
	if _, err := onlinebulletins.ValidateDocument(document); err != nil {
		return "", onlinebulletins.ErrLayoutJob
	}
	document.LayoutManifest.ContentHash, document.LayoutManifest.LayoutValidationHash = "", ""
	submission, contentHash, err := onlinebulletins.MarshalLayoutSubmission(document, metadata)
	if err != nil || len(submission) > onlinebulletins.MaxDocumentBytes {
		return "", onlinebulletins.ErrLayoutJob
	}
	canonical, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$2 WHERE document_id=$1 AND status IN ('queued','validating')`, documentID, now); err != nil {
		return "", err
	}
	id := platform.NewID()
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_layout_job(id,document_id,source_revision,canonical_metadata,content_hash,renderer_artifact_sha256,available_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$7,$7)`, id, documentID, revision, canonical, contentHash, document.LayoutManifest.RendererArtifactSha256, now); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET draft_status='reviewRequired',version=version+1,updated_at=$2 WHERE id=$1`, documentID, now); err != nil {
		return "", err
	}
	if err = enqueueOnlineBulletinWake(ctx, tx, id, now); err != nil {
		return "", err
	}
	return id, nil
}

func (r *Repository) ClaimOnlineBulletinLayout(ctx context.Context, now time.Time, lease time.Duration, maxAttempts int) (*onlinebulletins.LayoutJob, error) {
	if now.IsZero() || lease <= 0 || lease > 120*time.Second || maxAttempts < 1 || maxAttempts > 10 {
		return nil, onlinebulletins.ErrLayoutJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job SET status='failed',last_error_code='attempt_limit',lease_token=NULL,lease_until=NULL,updated_at=$1 WHERE attempts >= $2 AND (status='queued' OR (status='validating' AND lease_until <= $1))`, now, maxAttempts); err != nil {
		return nil, err
	}
	var job onlinebulletins.LayoutJob
	var canonical, document, repair []byte
	err = tx.QueryRowContext(ctx, `WITH next AS (
	 SELECT id FROM hhc_web.online_bulletin_layout_job WHERE attempts<$4 AND available_at<=$1
	 AND (status='queued' OR (status='validating' AND lease_until<=$1))
	 ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1
	), claimed AS (
	 UPDATE hhc_web.online_bulletin_layout_job j SET status='validating',attempts=j.attempts+1,lease_until=$2,lease_token=$3,last_error_code='',updated_at=$1 FROM next WHERE j.id=next.id RETURNING j.*
	) SELECT j.id::text,j.document_id::text,j.source_revision,j.lease_token::text,j.lease_until,j.attempts,j.content_hash,j.renderer_artifact_sha256,j.canonical_metadata,r.document_json,j.repair_json,EXISTS(SELECT 1`+automaticLayoutSource+`)
	 FROM claimed j JOIN hhc_web.online_bulletin_revision r ON r.document_id=j.document_id AND r.revision=j.source_revision`, now, now.Add(lease), platform.NewID(), maxAttempts).Scan(&job.ID, &job.DocumentID, &job.Revision, &job.LeaseToken, &job.LeaseUntil, &job.Attempts, &job.ContentHash, &job.RendererArtifactSHA256, &canonical, &document, &repair, &job.Automatic)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(canonical, &job.Canonical) != nil || json.Unmarshal(document, &job.Document) != nil {
		return nil, onlinebulletins.ErrLayoutJob
	}
	job.Automatic = job.Automatic && r.llmBulletinExtraction
	if len(repair) > 0 && json.Unmarshal(repair, &job.Repair) != nil {
		return nil, onlinebulletins.ErrLayoutJob
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &job, nil
}

func (r *Repository) FailOnlineBulletinLayout(ctx context.Context, job onlinebulletins.LayoutJob, code string, issues []onlinebulletins.ReviewIssue, now time.Time, maxAttempts int) error {
	if !extractionErrorCode.MatchString(code) || now.IsZero() || maxAttempts < 1 || maxAttempts > 10 {
		return onlinebulletins.ErrLayoutJob
	}
	raw, err := marshalLayoutIssues(issues, job.Document.SourcePageCount)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job SET status=CASE WHEN attempts >= $5 THEN 'failed' ELSE 'queued' END,last_error_code=$3,review_issues=$6,available_at=$4+interval '1 minute',lease_token=NULL,lease_until=NULL,updated_at=$4 WHERE id=$1 AND lease_token=$2 AND status='validating' AND lease_until>$4`, job.ID, job.LeaseToken, code, now, maxAttempts, raw)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return onlinebulletins.ErrLayoutLease
	}
	return nil
}

func marshalLayoutIssues(issues []onlinebulletins.ReviewIssue, pageCount int) ([]byte, error) {
	if len(issues) > 64 {
		return nil, onlinebulletins.ErrLayoutJob
	}
	if issues == nil {
		issues = []onlinebulletins.ReviewIssue{}
	}
	for _, issue := range issues {
		if issue.Code != "layout_validation_failed" || !issue.Blocking || issue.SourcePage < 0 || issue.SourcePage > pageCount {
			return nil, onlinebulletins.ErrLayoutJob
		}
	}
	raw, err := json.Marshal(issues)
	if err != nil || len(raw) > 32<<10 {
		return nil, onlinebulletins.ErrLayoutJob
	}
	return raw, nil
}

func (r *Repository) CompleteOnlineBulletinLayout(ctx context.Context, job onlinebulletins.LayoutJob, result onlinebulletins.LayoutResult, now time.Time) error {
	if now.IsZero() {
		return onlinebulletins.ErrLayoutJob
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current onlinebulletins.CanonicalMetadata
	var local int64
	err = tx.QueryRowContext(ctx, `SELECT v.title,v.subtitle,i.issue_number,i.issue_date::text,d.local_revision
	 FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
	 JOIN hhc_web.bulletin_version v ON v.issue_id=d.issue_id AND v.series=d.series AND v.locale=d.content_locale
	 WHERE d.id=$1 FOR UPDATE OF v,i,d`, job.DocumentID).Scan(&current.Title, &current.Subtitle, &current.IssueNumber, &current.Date, &local)
	if err != nil {
		return err
	}
	var status, hash, renderer, extractor string
	var token sql.NullString
	var until sql.NullTime
	var completed sql.NullInt64
	var metadataJSON, documentJSON, issuesJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT j.status,j.lease_token::text,j.lease_until,j.result_revision,j.content_hash,j.renderer_artifact_sha256,j.canonical_metadata,r.document_json,r.review_issues,r.extractor_version
	 FROM hhc_web.online_bulletin_layout_job j JOIN hhc_web.online_bulletin_revision r ON r.document_id=j.document_id AND r.revision=j.source_revision
	 WHERE j.id=$1 AND j.document_id=$2 AND j.source_revision=$3 FOR UPDATE OF j`, job.ID, job.DocumentID, job.Revision).Scan(&status, &token, &until, &completed, &hash, &renderer, &metadataJSON, &documentJSON, &issuesJSON, &extractor)
	if errors.Is(err, sql.ErrNoRows) {
		return onlinebulletins.ErrLayoutLease
	}
	if err != nil {
		return err
	}
	var metadata onlinebulletins.CanonicalMetadata
	var original onlinebulletins.Document
	var issues []onlinebulletins.ReviewIssue
	if json.Unmarshal(metadataJSON, &metadata) != nil || json.Unmarshal(documentJSON, &original) != nil || json.Unmarshal(issuesJSON, &issues) != nil {
		return onlinebulletins.ErrLayoutJob
	}
	original.LayoutManifest.ContentHash, original.LayoutManifest.LayoutValidationHash = "", ""
	_, inputHash, _ := onlinebulletins.MarshalLayoutSubmission(original, metadata)
	checked, err := onlinebulletins.VerifyComposedLayout(result, original, metadata)
	if err != nil || inputHash != hash || checked.Document.LayoutManifest.RendererArtifactSha256 != renderer {
		return onlinebulletins.ErrLayoutJob
	}
	if status == "ready" && completed.Valid {
		var savedProof string
		if err = tx.QueryRowContext(ctx, `SELECT layout_validation_hash FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, job.DocumentID, completed.Int64).Scan(&savedProof); err != nil {
			return err
		}
		if savedProof != checked.LayoutValidationHash {
			return onlinebulletins.ErrLayoutJob
		}
		return tx.Commit()
	}
	if status != "validating" || token.String != job.LeaseToken || !until.Valid || !until.Time.After(now) {
		return onlinebulletins.ErrLayoutLease
	}
	// A derived release measures its published source snapshot, even when a
	// later un-published canonical edit changes the shared date/issue fields.
	if current != metadata && current.Title == metadata.Title && current.Subtitle == metadata.Subtitle {
		var frozen bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_conversion_job c JOIN hhc_web.online_bulletin_document s ON s.id=c.source_document_id AND s.published_revision=c.source_revision JOIN hhc_web.online_bulletin_publication p ON p.document_id=s.id AND p.revision=s.published_revision AND p.canonical_metadata=c.source_metadata JOIN hhc_web.outbox_event e ON e.id=c.publication_event_id AND e.status IN ('pending','processing') AND e.event_type='bulletin.online.publish_simplified' WHERE c.document_id=$1 AND c.result_revision=$2 AND c.status='review_ready')`, job.DocumentID, job.Revision).Scan(&frozen)
		if err != nil {
			return err
		}
		if frozen {
			current.IssueNumber = metadata.IssueNumber
			current.Date = metadata.Date
		}
	}
	if local != job.Revision || current != metadata {
		_, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job SET status='superseded',last_error_code='stale_layout_input',lease_token=NULL,lease_until=NULL,updated_at=$2 WHERE id=$1`, job.ID, now)
		if err != nil {
			return err
		}
		return tx.Commit()
	}
	kept := make([]onlinebulletins.ReviewIssue, 0, len(issues))
	for _, issue := range issues {
		if issue.Code != "layout_validation_failed" && issue.Code != "layout_validation_required" {
			kept = append(kept, issue)
		}
	}
	validation, err := onlinebulletins.ValidateDocument(checked.Document)
	if err != nil {
		return onlinebulletins.ErrLayoutJob
	}
	seen := map[onlinebulletins.ReviewIssue]bool{}
	issues = []onlinebulletins.ReviewIssue{}
	for _, issue := range append(kept, validation...) {
		if !seen[issue] {
			issues = append(issues, issue)
			seen[issue] = true
		}
	}
	draftStatus := "ready"
	for _, issue := range issues {
		if issue.Blocking {
			draftStatus = "reviewRequired"
		}
	}
	checked.Document.LayoutManifest.ContentHash = checked.ContentHash
	checked.Document.LayoutManifest.LayoutValidationHash = checked.LayoutValidationHash
	documentJSON, err = json.Marshal(checked.Document)
	if err != nil || len(documentJSON) > onlinebulletins.MaxDocumentBytes {
		return onlinebulletins.ErrLayoutJob
	}
	issuesJSON, err = json.Marshal(issues)
	if err != nil {
		return err
	}
	var next int64
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(revision),0)+1 FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, job.DocumentID).Scan(&next); err != nil {
		return err
	}
	doc := checked.Document
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,layout_validation_hash,review_issues,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'bulletin-layout-validator')`, job.DocumentID, doc.IssueID, doc.Series, doc.ContentLocale, next, documentJSON, doc.SourceAssetChecksum, doc.TemplateVersion, extractor, checked.ContentHash, doc.LayoutManifest.RendererVersion, renderer, checked.LayoutValidationHash, issuesJSON)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET local_revision=$2,draft_status=$3,version=version+1,updated_at=$4 WHERE id=$1`, job.DocumentID, next, draftStatus, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job SET status='ready',result_revision=$2,measurement_json=$3,lease_token=NULL,lease_until=NULL,updated_at=$4 WHERE id=$1`, job.ID, next, checked.MeasurementJSON, now); err != nil {
		return err
	}
	return tx.Commit()
}
