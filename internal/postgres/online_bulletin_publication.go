package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func (r *Repository) PublishOnlineBulletin(ctx context.Context, issueID, series, locale string, input onlinebulletins.PublishInput, actor string, now time.Time) (onlinebulletins.ConfirmedRevision, error) {
	if err := r.validatePublishOnlineBulletinInput(ctx, input, actor, now); err != nil {
		return onlinebulletins.ConfirmedRevision{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return onlinebulletins.ConfirmedRevision{}, err
	}
	defer tx.Rollback()
	result, err := r.publishOnlineBulletinTx(ctx, tx, issueID, series, locale, input, actor, now, nil)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (r *Repository) publishOnlineBulletinTx(ctx context.Context, tx *sql.Tx, issueID, series, locale string, input onlinebulletins.PublishInput, actor string, now time.Time, metadataOverride *onlinebulletins.CanonicalMetadata) (onlinebulletins.ConfirmedRevision, error) {
	var result onlinebulletins.ConfirmedRevision
	if err := r.validatePublishOnlineBulletinInput(ctx, input, actor, now); err != nil {
		return result, err
	}

	var canonicalVersion int64
	var published sql.NullInt64
	var canonical onlinebulletins.CanonicalMetadata
	var documentJSON, issuesJSON []byte
	var proof, contentHash, renderer string
	var pdfPublished bool
	err := tx.QueryRowContext(ctx, `SELECT d.id::text,d.version,d.local_revision,d.published_revision,i.version,v.title,v.subtitle,i.issue_number,i.issue_date::text,r.document_json,r.review_issues,COALESCE(r.layout_validation_hash,''),r.content_hash,r.renderer_artifact_sha256,
	 v.status='published' AND EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version p WHERE p.issue_id=v.issue_id AND p.series=v.series AND p.locale=v.locale AND p.asset_id=v.pdf_asset_id AND p.title=v.title AND p.subtitle=v.subtitle)
	 FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
	 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=d.series AND v.locale=d.content_locale
	 JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision
	 WHERE d.issue_id=$1 AND d.series=$2 AND d.content_locale=$3 AND i.issue_number>=1739 FOR UPDATE OF i,v,d`, issueID, series, locale).Scan(&result.DocumentID, &result.Version, &result.Revision, &published, &canonicalVersion, &canonical.Title, &canonical.Subtitle, &canonical.IssueNumber, &canonical.Date, &documentJSON, &issuesJSON, &proof, &contentHash, &renderer, &pdfPublished)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if canonicalVersion != input.CanonicalVersion || result.Version != input.OnlineVersion || result.Revision != input.Revision {
		return result, bulletins.ErrPrecondition
	}
	if (locale == "zh-Hant" && !pdfPublished) || proof == "" {
		return result, bulletins.ErrConflict
	}
	var document onlinebulletins.Document
	var issues []onlinebulletins.ReviewIssue
	if json.Unmarshal(documentJSON, &document) != nil || json.Unmarshal(issuesJSON, &issues) != nil {
		return result, bulletins.ErrConflict
	}
	validation, err := onlinebulletins.ValidateDocument(document)
	if err != nil || len(validation) != 0 || document.LayoutManifest.LayoutValidationHash != proof || document.LayoutManifest.ContentHash != contentHash || document.LayoutManifest.RendererArtifactSha256 != renderer {
		return result, bulletins.ErrConflict
	}
	metadataPending := metadataOverride != nil && canonical != *metadataOverride
	if metadataOverride != nil {
		canonical = *metadataOverride
	}
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		return result, err
	}
	var acceptedJSON, measurement []byte
	err = tx.QueryRowContext(ctx, `SELECT c.accepted_issues,j.measurement_json FROM hhc_web.online_bulletin_confirmation c
	 JOIN hhc_web.online_bulletin_layout_job j ON j.document_id=c.document_id AND j.result_revision=c.revision AND j.status='ready' AND j.canonical_metadata=c.canonical_metadata AND j.measurement_json->>'layoutValidationHash'=c.layout_validation_hash
	 WHERE c.document_id=$1 AND c.revision=$2 AND c.canonical_metadata=$3::jsonb AND c.layout_validation_hash=$4 AND c.page_count=$5 AND j.renderer_artifact_sha256=$6`, result.DocumentID, result.Revision, canonicalJSON, proof, len(document.Pages), renderer).Scan(&acceptedJSON, &measurement)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrConflict
	}
	if err != nil {
		return result, err
	}
	var accepted []onlinebulletins.ReviewIssue
	if json.Unmarshal(acceptedJSON, &accepted) != nil || onlinebulletins.ValidateReview(issues, accepted) != nil {
		return result, bulletins.ErrConflict
	}
	// Rebuild the exact saved input, then verify the worker's immutable proof.
	// A matching hash supplied by an editor is never publication authority.
	document.LayoutManifest.ContentHash, document.LayoutManifest.LayoutValidationHash = "", ""
	submission, _, err := onlinebulletins.MarshalLayoutSubmission(document, canonical)
	if err != nil {
		return result, err
	}
	if _, err = onlinebulletins.VerifyComposedLayout(onlinebulletins.LayoutResult{Document: document, SubmissionJSON: submission, MeasurementJSON: measurement, ContentHash: contentHash, LayoutValidationHash: proof}, document, canonical); err != nil {
		return result, bulletins.ErrConflict
	}
	if published.Valid && published.Int64 == result.Revision {
		if len(input.Mappings) != 0 {
			return result, bulletins.ErrInvalid
		}
		if locale == "zh-Hant" && r.auditOutbox != nil {
			if err = enqueueSimplifiedRelease(ctx, tx, result, issueID, canonical, actor, now); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	// Retain continuity across an unpublish/republish gap; never start from a
	// draft counter or lose the last published endpoint when the pointer is null.
	var last int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision),0) FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, result.DocumentID).Scan(&last); err != nil {
		return result, err
	}
	if last > result.Revision {
		return result, bulletins.ErrConflict
	}
	var mappings []onlinebulletins.SentenceMapping
	if last > 0 && last < result.Revision {
		var previousJSON []byte
		if err = tx.QueryRowContext(ctx, `SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, result.DocumentID, last).Scan(&previousJSON); err != nil {
			return result, err
		}
		var previous onlinebulletins.Document
		if json.Unmarshal(previousJSON, &previous) != nil {
			return result, bulletins.ErrConflict
		}
		mappings, err = onlinebulletins.PublicationMappings(previous, document, input.Mappings)
		if err != nil {
			return result, bulletins.ErrInvalid
		}
		for _, mapping := range mappings {
			if mapping.Status == "requires_review" {
				return result, bulletins.ErrConflict
			}
		}
	} else if len(input.Mappings) != 0 {
		return result, bulletins.ErrInvalid
	}
	if last != result.Revision {
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_publication(document_id,revision,canonical_metadata,created_by,created_at) VALUES($1,$2,$3,$4,$5)`, result.DocumentID, result.Revision, canonicalJSON, actor, now); err != nil {
			return result, err
		}
	}
	if len(mappings) > 0 {
		checkpoint, err := json.Marshal(mappings)
		if err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_mapping(document_id,from_revision,to_revision,from_sentence_id,status,to_sentence_ids)
		 SELECT $1,$2,$3,m."fromSentenceId",m.status,m."toSentenceIds" FROM jsonb_to_recordset($4::jsonb) AS m("fromSentenceId" text,status text,"toSentenceIds" jsonb)`, result.DocumentID, last, result.Revision, checkpoint); err != nil {
			return result, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET published_revision=$2,draft_status='published',metadata_sync_pending=$4,version=version+1,updated_at=$3 WHERE id=$1`, result.DocumentID, result.Revision, now, metadataPending); err != nil {
		return result, err
	}
	if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.online.publish", result.DocumentID, actor, now); err != nil {
		return result, err
	}
	result.Version++
	if locale == "zh-Hant" && r.auditOutbox != nil {
		if err = enqueueSimplifiedRelease(ctx, tx, result, issueID, canonical, actor, now); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (r *Repository) UnpublishOnlineBulletin(ctx context.Context, issueID, series, locale string, canonicalVersion, onlineVersion int64, actor string, now time.Time) (onlinebulletins.UnpublishedRevision, error) {
	var result onlinebulletins.UnpublishedRevision
	if canonicalVersion < 1 || onlineVersion < 1 || actor == "" || now.IsZero() {
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
	var canonical int64
	var published sql.NullInt64
	var automatic bool
	err = tx.QueryRowContext(ctx, `SELECT d.id::text,d.version,i.version,d.published_revision,v.auto_publish FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=d.series AND v.locale=d.content_locale WHERE d.issue_id=$1 AND d.series=$2 AND d.content_locale=$3 FOR UPDATE OF i,v,d`, issueID, series, locale).Scan(&result.DocumentID, &result.Version, &canonical, &published, &automatic)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if canonical != canonicalVersion || result.Version != onlineVersion {
		return result, bulletins.ErrPrecondition
	}
	if err = cancelOnlineAutomaticIntent(ctx, tx, result.DocumentID, now); err != nil {
		return result, err
	}
	if !published.Valid {
		if automatic {
			if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET version=version+1,updated_at=$2 WHERE id=$1`, result.DocumentID, now); err != nil {
				return result, err
			}
			result.Version++
			if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.online.unpublish", result.DocumentID, actor, now); err != nil {
				return result, err
			}
		}
		return result, tx.Commit()
	}
	if err = r.clearOnlinePublication(ctx, tx, result.DocumentID, actor, now); err != nil {
		return result, err
	}
	result.Version++
	return result, tx.Commit()
}

// Both independent and explicitly paired unpublish use the caller's existing
// transaction and locked document. Failure cannot leave half an action visible.
func (r *Repository) clearOnlinePublication(ctx context.Context, tx *sql.Tx, documentID, actor string, now time.Time) error {
	if err := cancelOnlineAutomaticIntent(ctx, tx, documentID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event e SET status='failed',claimed_until=NULL,last_error='publication_withdrawn',updated_at=$2 WHERE e.event_type='bulletin.online.publish_simplified' AND e.status IN ('pending','processing','failed') AND (e.aggregate_id=$1 OR e.payload_json->>'issueId'=(SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1 AND content_locale='zh-Hans'))`, documentID, now); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET published_revision=NULL,draft_status=CASE WHEN draft_status='published' THEN 'ready' ELSE draft_status END,version=version+1,updated_at=$2 WHERE id=$1`, documentID, now); err != nil {
		return err
	}
	return r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.online.unpublish", documentID, actor, now)
}

func (r *Repository) validatePublishOnlineBulletinInput(ctx context.Context, input onlinebulletins.PublishInput, actor string, now time.Time) error {
	if input.CanonicalVersion < 1 || input.OnlineVersion < 1 || input.Revision < 1 || len(input.Mappings) > 65536 || actor == "" || now.IsZero() {
		return bulletins.ErrInvalid
	}
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return err
	}

	return nil
}
