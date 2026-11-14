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

func (r *Repository) WithSimplifiedPublicationRecovery(enabled bool) *Repository {
	r.simplifiedPublicationRecovery = enabled
	return r
}

// New human authority publishes only saved qualified evidence. The original
// automatic authorizer and terminal reason are immutable historical facts.
func (r *Repository) RecoverSimplifiedOnlineBulletinPublication(ctx context.Context, issue string, input onlinebulletins.SimplifiedRecoveryInput, key, actor string, now time.Time) (onlinebulletins.SimplifiedRecoveryResult, error) {
	var result onlinebulletins.SimplifiedRecoveryResult
	if uuid.Validate(issue) != nil || !onlinebulletins.ValidSimplifiedRecoveryInput(input) || !onlinebulletins.ValidSimplifiedRecoveryKey(key) || now.IsZero() {
		return result, bulletins.ErrInvalid
	}
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor || !auditclient.ValidActorRequest(actor, p.RequestID) {
		return result, bulletins.ErrAuditUnavailable
	}
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return result, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	fingerprint := mustJSON(struct {
		Kind, IssueID, Series, Locale, TargetDocumentID string
		CanonicalVersion, OnlineVersion                 int64
		Source                                          onlinebulletins.SimplifiedRecoverySource
		PublicationOperationID                          string
		Revision                                        int64
		LayoutValidationHash                            string
	}{"simplified_publication_recovery", issue, "general", "zh-Hans", input.TargetDocumentID, input.CanonicalVersion, input.OnlineVersion, input.Source, input.PublicationOperationID, input.Revision, input.LayoutValidationHash})
	lookup := func() (bool, error) {
		var savedIssue string
		var raw []byte
		var same bool
		e := tx.QueryRowContext(ctx, `SELECT issue_id::text,input_json=$3::jsonb,result_json FROM hhc_web.online_bulletin_recovery_receipt WHERE actor_id=$1 AND operation_key=$2`, actor, key, fingerprint).Scan(&savedIssue, &same, &raw)
		if errors.Is(e, sql.ErrNoRows) {
			return false, nil
		}
		if e != nil {
			return false, e
		}
		if !same || savedIssue != issue || json.Unmarshal(raw, &result) != nil || result.Receipt.AuthorizationID == "" || result.Receipt.PublicationOperationID != input.PublicationOperationID {
			return false, bulletins.ErrConflict
		}
		result.Replayed = true
		return true, nil
	}
	if found, e := lookup(); e != nil {
		return result, e
	} else if found {
		return result, tx.Commit()
	}
	// Serialize new issue commands, then recheck receipts for concurrent waiters.
	var canonical int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_issue WHERE id=$1 AND issue_number>=1739 FOR UPDATE`, issue).Scan(&canonical); errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	} else if err != nil {
		return result, err
	}
	if found, e := lookup(); e != nil {
		return result, e
	} else if found {
		return result, tx.Commit()
	}
	if !r.simplifiedPublicationRecovery {
		return result, bulletins.ErrOnlinePublicationUnavailable
	}
	var sourceID, hash string
	var sourceVersion int64
	var published sql.NullInt64
	var sourceMetadata []byte
	err = tx.QueryRowContext(ctx, `SELECT d.id::text,d.version,d.published_revision,COALESCE(r.content_hash,''),p.canonical_metadata FROM hhc_web.online_bulletin_document d LEFT JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.published_revision LEFT JOIN hhc_web.online_bulletin_publication p ON p.document_id=d.id AND p.revision=d.published_revision WHERE d.issue_id=$1 AND d.series='general' AND d.content_locale='zh-Hant' FOR UPDATE OF d`, issue).Scan(&sourceID, &sourceVersion, &published, &hash, &sourceMetadata)
	if errors.Is(err, sql.ErrNoRows) {
		return result, recoveryBlocked("source_changed")
	}
	if err != nil {
		return result, err
	}
	if canonical != input.CanonicalVersion || sourceVersion != input.Source.OnlineVersion {
		return result, bulletins.ErrPrecondition
	}
	if sourceID != input.Source.DocumentID || !published.Valid || published.Int64 != input.Source.Revision || hash != input.Source.ContentHash {
		return result, recoveryBlocked("source_changed")
	}
	var target string
	var version int64
	var local, incoming, targetPublished sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id::text,version,local_revision,incoming_revision,published_revision FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series='general' AND content_locale='zh-Hans' FOR UPDATE`, issue).Scan(&target, &version, &local, &incoming, &targetPublished)
	if errors.Is(err, sql.ErrNoRows) {
		return result, recoveryBlocked("missing_local")
	}
	if err != nil {
		return result, err
	}
	if !local.Valid {
		return result, recoveryBlocked("missing_local")
	}
	if target != input.TargetDocumentID || version != input.OnlineVersion || local.Int64 != input.Revision {
		return result, bulletins.ErrPrecondition
	}
	if targetPublished.Valid && targetPublished.Int64 == input.Revision {
		return result, recoveryBlocked("review_required")
	}
	if incoming.Valid {
		return result, recoveryBlocked("review_required")
	}
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.outbox_event WHERE id=$1 AND aggregate_id=$2 AND aggregate_version=$3 AND event_type=$4 FOR UPDATE`, input.PublicationOperationID, sourceID, input.Source.Revision, simplifiedReleaseEvent).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return result, recoveryBlocked("publication_intent_missing")
	}
	if err != nil {
		return result, err
	}
	var original simplifiedReleasePayload
	var frozen onlinebulletins.CanonicalMetadata
	if json.Unmarshal(payload, &original) != nil || json.Unmarshal(sourceMetadata, &frozen) != nil || original.IssueID != issue || original.SourceRevision != input.Source.Revision || original.SourceMetadata != frozen || !auditclient.ValidActorRequest(original.ActorID, original.RequestID) {
		return result, recoveryBlocked("publication_intent_missing")
	}
	var jobID, converter string
	var converted, layoutMetadata, raw, findings []byte
	var proof, contentHash, renderer string
	err = tx.QueryRowContext(ctx, `SELECT c.id::text,c.converter_version,c.converted_metadata,l.canonical_metadata,r.document_json,r.review_issues,r.layout_validation_hash,r.content_hash,r.renderer_artifact_sha256
 FROM hhc_web.online_bulletin_conversion_job c JOIN hhc_web.online_bulletin_layout_job l ON l.document_id=c.document_id AND l.source_revision=c.result_revision AND l.status='ready' JOIN hhc_web.online_bulletin_revision r ON r.document_id=l.document_id AND r.revision=l.result_revision
 WHERE c.document_id=$1 AND c.source_document_id=$2 AND c.source_revision=$3 AND c.source_content_hash=$4 AND c.source_metadata=$5::jsonb AND c.status='review_ready' AND c.publication_event_id=$6 AND l.result_revision=$7 AND l.measurement_json->>'layoutValidationHash'=r.layout_validation_hash AND l.renderer_artifact_sha256=r.renderer_artifact_sha256 ORDER BY l.created_at DESC,l.id DESC LIMIT 1 FOR UPDATE OF c,l`, target, sourceID, input.Source.Revision, input.Source.ContentHash, sourceMetadata, input.PublicationOperationID, input.Revision).Scan(&jobID, &converter, &converted, &layoutMetadata, &raw, &findings, &proof, &contentHash, &renderer)
	if errors.Is(err, sql.ErrNoRows) {
		return result, recoveryBlocked("layout_not_ready")
	}
	if err != nil {
		return result, err
	}
	if converter != onlinebulletins.SimplifiedConverterVersion {
		return result, recoveryBlocked("incompatible_proof")
	}
	qualifiedMetadata, e := conversionAssessmentMetadataTx(ctx, tx, jobID)
	if e != nil {
		return result, e
	}
	if qualifiedMetadata == nil {
		return result, recoveryBlocked("semantic_proof_missing")
	}
	var document onlinebulletins.Document
	var issues []onlinebulletins.ReviewIssue
	var metadata, convertedMetadata, currentMetadata onlinebulletins.CanonicalMetadata
	if json.Unmarshal(raw, &document) != nil || json.Unmarshal(findings, &issues) != nil || json.Unmarshal(layoutMetadata, &metadata) != nil || json.Unmarshal(converted, &convertedMetadata) != nil {
		return result, recoveryBlocked("incompatible_proof")
	}
	if len(issues) != 0 {
		return result, recoveryBlocked("review_required")
	}
	err = tx.QueryRowContext(ctx, `SELECT v.title,v.subtitle,i.issue_number,i.issue_date::text FROM hhc_web.bulletin_version v JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id WHERE v.issue_id=$1 AND v.series='general' AND v.locale='zh-Hans' FOR UPDATE OF v`, issue).Scan(&currentMetadata.Title, &currentMetadata.Subtitle, &currentMetadata.IssueNumber, &currentMetadata.Date)
	if err != nil {
		return result, err
	}
	if currentMetadata != metadata || metadata != *qualifiedMetadata {
		return result, recoveryBlocked("stale_metadata")
	}
	validation, e := onlinebulletins.ValidateDocument(document)
	if e != nil || len(validation) != 0 || proof != input.LayoutValidationHash || document.IssueID != issue || document.Series != "general" || document.ContentLocale != "zh-Hans" || document.LayoutManifest.LayoutValidationHash != proof || document.LayoutManifest.ContentHash != contentHash || document.LayoutManifest.RendererArtifactSha256 != renderer {
		return result, recoveryBlocked("incompatible_proof")
	}
	mappings, e := simplifiedReleaseMappings(ctx, tx, target, sourceID, input.Source.Revision, document)
	if errors.Is(e, onlinebulletins.ErrMapping) {
		return result, recoveryBlocked("mapping_review_required")
	}
	if e != nil {
		return result, e
	}
	receiptID, authorizationID := platform.NewID(), platform.NewID()
	// Stop only live automatic authority; terminal historical causes stay intact.
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='failed',last_error='publication_authority_superseded',claimed_until=NULL,simplified_claim_token=NULL,updated_at=$2 WHERE aggregate_id=$1 AND event_type='bulletin.online.publish_simplified' AND status IN('pending','processing')`, sourceID, now); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_simplified_authorization(id,operation_id,receipt_id,actor_id,request_id,created_at) VALUES($1,$2,$3,$4,$5,$6)`, authorizationID, input.PublicationOperationID, receiptID, actor, p.RequestID, now); err != nil {
		return result, err
	}
	var existingConfirmation bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1 AND revision=$2 AND layout_validation_hash=$3 AND canonical_metadata=$4::jsonb AND page_count=$5 AND accepted_issues='[]'::jsonb)`, target, input.Revision, proof, layoutMetadata, len(document.Pages)).Scan(&existingConfirmation); err != nil {
		return result, err
	}
	confirmed := onlinebulletins.ConfirmedRevision{DocumentID: target, Version: version, Revision: input.Revision}
	if !existingConfirmation {
		confirmed, e = r.confirmOnlineBulletinTx(ctx, tx, issue, "general", "zh-Hans", onlinebulletins.ConfirmInput{CanonicalVersion: canonical, OnlineVersion: version, Revision: input.Revision, LayoutValidationHash: proof, PageCount: len(document.Pages), AcceptedIssues: []onlinebulletins.ReviewIssue{}}, actor, now, &metadata)
		if e != nil {
			return result, e
		}
	}

	publishedResult, e := r.publishOnlineBulletinTx(ctx, tx, issue, "general", "zh-Hans", onlinebulletins.PublishInput{CanonicalVersion: canonical, OnlineVersion: confirmed.Version, Revision: input.Revision, Mappings: mappings}, actor, now, &metadata)
	if e != nil {
		return result, e
	}
	result = onlinebulletins.SimplifiedRecoveryResult{Receipt: onlinebulletins.SimplifiedRecoveryReceipt{RecoveryReceipt: onlinebulletins.RecoveryReceipt{ID: receiptID, OperationKey: key, DocumentID: target, Revision: input.Revision, CanonicalVersion: canonical, OnlineVersion: input.OnlineVersion, LayoutValidationHash: proof, Outcome: "published", CreatedAt: now}, Source: input.Source, PublicationOperationID: input.PublicationOperationID, AuthorizationID: authorizationID}, Publication: publishedResult}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_recovery_receipt(id,issue_id,actor_id,operation_key,input_json,result_json,request_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, receiptID, issue, actor, key, fingerprint, mustJSON(result), p.RequestID, now); err != nil {
		return result, mapConflict(err)
	}
	return result, tx.Commit()
}
