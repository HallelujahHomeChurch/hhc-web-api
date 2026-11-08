package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/google/uuid"
)

func (r *Repository) QueueOnlineBulletinConversion(ctx context.Context, issueID string, input onlinebulletins.QueueSimplifiedInput, actor string, now time.Time) (onlinebulletins.QueuedExtraction, error) {
	var result onlinebulletins.QueuedExtraction
	if uuid.Validate(issueID) != nil || input.CanonicalVersion < 1 || input.SourceOnlineVersion < 1 || input.SourceRevision < 1 || input.TargetOnlineVersion < 0 || actor == "" || now.IsZero() {
		return result, bulletins.ErrInvalid
	}
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return result, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if recovered, e := r.retrySimplifiedReleaseTx(ctx, tx, issueID, input, actor, now); e != nil {
		return result, e
	} else if recovered != nil {
		return *recovered, tx.Commit()
	}
	result, err = r.queueOnlineBulletinConversionTx(ctx, tx, issueID, input, actor, now, "")
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

// Clean conversions still require an immutable, current-metadata worker proof.
const simplifiedSourceReadySQL = `(
 EXISTS(SELECT 1 FROM hhc_web.online_bulletin_confirmation c WHERE c.document_id=d.id AND c.revision=d.local_revision
 AND c.layout_validation_hash=r.layout_validation_hash AND c.canonical_metadata=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text))
 OR (r.review_issues='[]'::jsonb AND r.layout_validation_hash<>'' AND EXISTS(
 SELECT 1 FROM hhc_web.online_bulletin_layout_job l WHERE l.document_id=d.id AND l.result_revision=d.local_revision
 AND l.status='ready' AND l.measurement_json->>'layoutValidationHash'=r.layout_validation_hash
 AND l.renderer_artifact_sha256=r.renderer_artifact_sha256
 AND l.canonical_metadata=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text))))`

func (r *Repository) queueOnlineBulletinConversionTx(ctx context.Context, tx *sql.Tx, issueID string, input onlinebulletins.QueueSimplifiedInput, actor string, now time.Time, publicationEventID string) (onlinebulletins.QueuedExtraction, error) {
	var result onlinebulletins.QueuedExtraction
	var err error
	var canonicalVersion, sourceVersion, sourceRevision int64
	var sourceID, sourceHash string
	var sourceMetadata onlinebulletins.CanonicalMetadata
	var confirmed bool
	err = tx.QueryRowContext(ctx, simplifiedConversionSourceSQL, issueID, publicationEventID != "").Scan(&canonicalVersion, &sourceID, &sourceVersion, &sourceRevision, &sourceHash, &sourceMetadata.Title, &sourceMetadata.Subtitle, &sourceMetadata.IssueNumber, &sourceMetadata.Date, &confirmed)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if sourceVersion != input.SourceOnlineVersion || sourceRevision != input.SourceRevision {
		return result, bulletins.ErrPrecondition
	}
	if !confirmed {
		return result, bulletins.ErrConflict
	}
	var incoming sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id::text,version,incoming_revision FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series='general' AND content_locale='zh-Hans' FOR UPDATE`, issueID).Scan(&result.DocumentID, &result.Version, &incoming)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	sourceJSON, _ := json.Marshal(sourceMetadata)
	if result.DocumentID != "" {
		err = tx.QueryRowContext(ctx, `SELECT id::text,status,attempts,last_error_code FROM hhc_web.online_bulletin_conversion_job WHERE document_id=$1 AND source_document_id=$2 AND source_revision=$3 AND source_content_hash=$4 AND converter_version=$5 AND source_metadata=$6::jsonb ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, result.DocumentID, sourceID, sourceRevision, sourceHash, onlinebulletins.SimplifiedConverterVersion, sourceJSON).Scan(&result.Job.ID, &result.Job.Status, &result.Job.Attempts, &result.Job.ErrorCode)
		if err == nil && publicationEventID != "" && result.Job.Status == "failed" {
			input.Retry = true
		}
		if err == nil && publicationEventID != "" && result.Job.Status == "review_ready" {
			var failed bool
			if e := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT status='failed' FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1),false)`, result.DocumentID).Scan(&failed); e != nil {
				return result, e
			}
			input.Retry = failed
		}
		// Re-run a legacy Incoming result through the derived-only workflow.
		if err == nil && !input.Retry && !(incoming.Valid && result.Job.Status == "review_ready") {
			if result.Job.Status == "superseded" {
				return result, bulletins.ErrConflict
			}
			if err = bindConversionPublicationTx(ctx, tx, result.Job.ID, publicationEventID); err != nil {
				return result, err
			}
			return result, nil
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
	}
	if canonicalVersion != input.CanonicalVersion || result.Version != input.TargetOnlineVersion {
		return result, bulletins.ErrPrecondition
	}
	if input.Retry && result.Job.Status == "review_ready" && !incoming.Valid {
		var revision int64
		var raw []byte
		metadata := sourceMetadata
		err = tx.QueryRowContext(ctx, `SELECT d.local_revision,r.document_json,v.title,v.subtitle
		 FROM hhc_web.online_bulletin_document d
		 JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision
		 JOIN hhc_web.bulletin_version v ON v.issue_id=d.issue_id AND v.series=d.series AND v.locale=d.content_locale
		 JOIN hhc_web.online_bulletin_conversion_job c ON c.id=$2 AND c.document_id=d.id AND c.result_revision=d.local_revision
		 WHERE d.id=$1 AND (SELECT status FROM hhc_web.online_bulletin_layout_job WHERE document_id=d.id AND source_revision=d.local_revision ORDER BY created_at DESC,id DESC LIMIT 1)='failed'
		 FOR UPDATE OF v`, result.DocumentID, result.Job.ID).Scan(&revision, &raw, &metadata.Title, &metadata.Subtitle)
		if errors.Is(err, sql.ErrNoRows) {
			return result, bulletins.ErrConflict
		}
		if err != nil {
			return result, err
		}
		var document onlinebulletins.Document
		if json.Unmarshal(raw, &document) != nil {
			return result, onlinebulletins.ErrLayoutJob
		}
		if _, err = queueOnlineLayout(ctx, tx, result.DocumentID, revision, document, metadata, now); err != nil {
			return result, err
		}
		if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.online.update", result.DocumentID, actor, now); err != nil {
			return result, err
		}
		if err = bindConversionPublicationTx(ctx, tx, result.Job.ID, publicationEventID); err != nil {
			return result, err
		}
		result.Version++
		return result, nil
	}
	if input.Retry && (result.Job.ID == "" || result.Job.Status != "failed") {
		return result, bulletins.ErrConflict
	}
	var targetMetadata onlinebulletins.CanonicalMetadata
	targetMetadata.IssueNumber, targetMetadata.Date = sourceMetadata.IssueNumber, sourceMetadata.Date
	initializes := false
	err = tx.QueryRowContext(ctx, `SELECT title,subtitle FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series='general' AND locale='zh-Hans' FOR UPDATE`, issueID).Scan(&targetMetadata.Title, &targetMetadata.Subtitle)
	if errors.Is(err, sql.ErrNoRows) {
		targetMetadata = sourceMetadata
		initializes = true
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_version(id,issue_id,series,locale,title,subtitle,pdf_asset_id,pdf_file_name,created_by,updated_by,created_at,updated_at) VALUES($1,$2,'general','zh-Hans',$3,$4,'','',$5,$5,$6,$6)`, platform.NewID(), issueID, targetMetadata.Title, targetMetadata.Subtitle, actor, now); err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, issueID, actor, now); err != nil {
			return result, err
		}
		canonicalVersion++
	} else if err != nil {
		return result, err
	}
	if result.DocumentID == "" {
		result.DocumentID, result.Version = platform.NewID(), 1
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_document(id,issue_id,series,content_locale,created_at,updated_at) VALUES($1,$2,'general','zh-Hans',$3,$3)`, result.DocumentID, issueID, now); err != nil {
			return result, err
		}
	} else {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET version=version+1,updated_at=$2 WHERE id=$1`, result.DocumentID, now); err != nil {
			return result, err
		}
		result.Version++
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_conversion_job SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$2 WHERE document_id=$1 AND status IN ('queued','converting')`, result.DocumentID, now); err != nil {
		return result, err
	}
	targetJSON, _ := json.Marshal(targetMetadata)
	if result.Job.ID == "" {
		result.Job.ID = platform.NewID()
		_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_conversion_job(id,issue_id,document_id,source_document_id,source_revision,source_online_version,source_content_hash,source_metadata,requested_canonical_version,canonical_version,target_online_version,target_metadata,initializes_metadata,converter_version,created_by,available_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16,$16)`, result.Job.ID, issueID, result.DocumentID, sourceID, sourceRevision, sourceVersion, sourceHash, sourceJSON, input.CanonicalVersion, canonicalVersion, result.Version, targetJSON, initializes, onlinebulletins.SimplifiedConverterVersion, actor, now)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_conversion_job SET status='queued',attempts=0,last_error_code='',canonical_version=$2,target_online_version=$3,initializes_metadata=initializes_metadata AND target_metadata=$4::jsonb,target_metadata=$4,available_at=$5,updated_at=$5,source_online_version=$6 WHERE id=$1`, result.Job.ID, canonicalVersion, result.Version, targetJSON, now, sourceVersion)
	}
	if err != nil {
		return result, err
	}
	if err = bindConversionPublicationTx(ctx, tx, result.Job.ID, publicationEventID); err != nil {
		return result, err
	}
	if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.online.update", result.DocumentID, actor, now); err != nil {
		return result, err
	}
	if err = enqueueOnlineBulletinWake(ctx, tx, result.Job.ID, now); err != nil {
		return result, err
	}
	result.Job.Status, result.Job.Attempts, result.Job.ErrorCode = "queued", 0, ""
	return result, nil
}

const simplifiedConversionSourceSQL = `SELECT i.version,d.id::text,d.version,r.revision,r.content_hash,
 CASE WHEN $2 THEN p.canonical_metadata->>'title' ELSE v.title END,
 CASE WHEN $2 THEN p.canonical_metadata->>'subtitle' ELSE v.subtitle END,
 CASE WHEN $2 THEN (p.canonical_metadata->>'issueNumber')::int ELSE i.issue_number END,
 CASE WHEN $2 THEN p.canonical_metadata->>'date' ELSE i.issue_date::text END,
 CASE WHEN $2 THEN p.revision IS NOT NULL ELSE ` + simplifiedSourceReadySQL + ` END
 FROM hhc_web.bulletin_issue i
 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant'
 JOIN hhc_web.online_bulletin_document d ON d.issue_id=i.id AND d.series=v.series AND d.content_locale=v.locale
 JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=CASE WHEN $2 THEN d.published_revision ELSE d.local_revision END
 LEFT JOIN hhc_web.online_bulletin_publication p ON p.document_id=d.id AND p.revision=d.published_revision
 WHERE i.id=$1 AND i.issue_number>=1739 FOR UPDATE OF i,v,d`

// A write-scope retry resumes the already authorized publication intent; it
// cannot create new publish authority or replace the original publisher.
func (r *Repository) retrySimplifiedReleaseTx(ctx context.Context, tx *sql.Tx, issueID string, input onlinebulletins.QueueSimplifiedInput, actor string, now time.Time) (*onlinebulletins.QueuedExtraction, error) {
	var sourceID string
	var version, canonical int64
	var revision sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT d.id::text,d.version,d.published_revision,i.version FROM hhc_web.bulletin_issue i JOIN hhc_web.online_bulletin_document d ON d.issue_id=i.id AND d.series='general' AND d.content_locale='zh-Hant' WHERE i.id=$1 FOR UPDATE OF i,d`, issueID).Scan(&sourceID, &version, &revision, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !revision.Valid || revision.Int64 != input.SourceRevision {
		return nil, nil
	}
	var id, status, code string
	var raw []byte
	err = tx.QueryRowContext(ctx, `SELECT id::text,payload_json,status,COALESCE(last_error,'') FROM hhc_web.outbox_event WHERE event_type='bulletin.online.publish_simplified' AND aggregate_id=$1 AND aggregate_version=$2 ORDER BY created_at DESC,id DESC LIMIT 1 FOR UPDATE`, sourceID, revision.Int64).Scan(&id, &raw, &status, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if (status != "pending" && status != "processing" && status != "failed") || code == "source_publication_changed" || code == "publication_withdrawn" {
		return nil, nil
	}
	if input.SourceOnlineVersion != version || input.CanonicalVersion != canonical {
		return nil, bulletins.ErrPrecondition
	}
	var p simplifiedReleasePayload
	if json.Unmarshal(raw, &p) != nil || !auditclient.ValidActorRequest(p.ActorID, p.RequestID) {
		return nil, bulletins.ErrConflict
	}
	// Queueing recovers failed conversion/layout jobs, and reuses completed proofs.
	input.Retry = false
	authorized := auditclient.WithProvenance(ctx, p.ActorID, p.RequestID)
	queued, err := r.queueOnlineBulletinConversionTx(authorized, tx, issueID, input, p.ActorID, now, id)
	if err != nil {
		return nil, err
	}
	p.JobID = queued.Job.ID
	raw, err = json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='pending',payload_json=$2,attempts=0,claimed_until=NULL,last_error='',next_attempt_at=$3,updated_at=$3 WHERE id=$1`, id, raw, now); err != nil {
		return nil, err
	}
	if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.online.update", queued.DocumentID, actor, now); err != nil {
		return nil, err
	}
	return &queued, nil
}

func bindConversionPublicationTx(ctx context.Context, tx *sql.Tx, jobID, eventID string) error {
	if eventID == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_conversion_job SET
 status=CASE WHEN status='converting' AND publication_event_id IS DISTINCT FROM $2::uuid THEN 'queued' ELSE status END,
 attempts=CASE WHEN status='converting' AND publication_event_id IS DISTINCT FROM $2::uuid THEN 0 ELSE attempts END,
 lease_token=CASE WHEN status='converting' AND publication_event_id IS DISTINCT FROM $2::uuid THEN NULL ELSE lease_token END,
 lease_until=CASE WHEN status='converting' AND publication_event_id IS DISTINCT FROM $2::uuid THEN NULL ELSE lease_until END,
 publication_event_id=$2 WHERE id=$1`, jobID, eventID)
	if err != nil {
		return err
	}
	return enqueueOnlineBulletinWake(ctx, tx, jobID, time.Now().UTC())
}
