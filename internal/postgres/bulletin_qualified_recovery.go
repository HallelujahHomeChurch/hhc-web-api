package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"reflect"
	"strings"
	"time"
)

func recoveryBlocked(code string) error {
	return &onlinebulletins.RecoveryBlockedError{Blockers: []string{code}}
}
func (r *Repository) RecoverOnlineBulletinPublication(ctx context.Context, issue string, input onlinebulletins.RecoveryInput, key, actor string, now time.Time) (onlinebulletins.RecoveryResult, error) {
	var result onlinebulletins.RecoveryResult
	if !r.explicitOnlinePublication {
		return result, bulletins.ErrOnlinePublicationUnavailable
	}
	digest, err := hex.DecodeString(input.LayoutValidationHash)
	if err != nil || len(digest) != 32 || strings.ToLower(input.LayoutValidationHash) != input.LayoutValidationHash || input.CanonicalVersion < 1 || input.OnlineVersion < 1 || input.Revision < 1 || len(key) < 1 || len(key) > 128 || strings.TrimSpace(key) != key || now.IsZero() {
		return result, bulletins.ErrInvalid
	}
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor || !auditclient.ValidActorRequest(actor, p.RequestID) {
		return result, bulletins.ErrAuditUnavailable
	}
	if err = r.validateBulletinAudit(ctx, actor); err != nil {
		return result, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	// Canonical issue -> edition -> document precedes intent/event/workflow locks.
	var canonical int64
	var asset, checksum string
	var metadata onlinebulletins.CanonicalMetadata
	var pdfPublished bool
	err = tx.QueryRowContext(ctx, `SELECT i.version,v.pdf_asset_id,COALESCE(v.pdf_sha256,''),v.title,v.subtitle,i.issue_number,i.issue_date::text,
 v.status='published' AND EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version p WHERE p.issue_id=i.id AND p.series=v.series AND p.locale=v.locale AND p.asset_id=v.pdf_asset_id AND p.title=v.title AND p.subtitle=v.subtitle)
 FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant' WHERE i.id=$1 AND i.issue_number>=1739 FOR UPDATE OF i,v`, issue).Scan(&canonical, &asset, &checksum, &metadata.Title, &metadata.Subtitle, &metadata.IssueNumber, &metadata.Date, &pdfPublished)

	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	// Bind the key to all submitted CAS/proof fields, including online If-Match.
	inputJSON := mustJSON(struct {
		CanonicalVersion int64  `json:"canonicalVersion"`
		OnlineVersion    int64  `json:"onlineVersion"`
		Revision         int64  `json:"revision"`
		Hash             string `json:"layoutValidationHash"`
	}{input.CanonicalVersion, input.OnlineVersion, input.Revision, input.LayoutValidationHash})
	var savedIssue string
	var savedInput, savedResult []byte
	err = tx.QueryRowContext(ctx, `SELECT issue_id::text,input_json,result_json FROM hhc_web.online_bulletin_recovery_receipt WHERE actor_id=$1 AND operation_key=$2`, actor, key).Scan(&savedIssue, &savedInput, &savedResult)
	if err == nil {
		var same bool
		if err = tx.QueryRowContext(ctx, `SELECT $1::jsonb=$2::jsonb`, savedInput, inputJSON).Scan(&same); err != nil {
			return result, err
		}
		if !same || savedIssue != issue {
			return result, bulletins.ErrConflict
		}
		if json.Unmarshal(savedResult, &result) != nil {
			return result, bulletins.ErrConflict
		}
		result.Replayed = true
		return result, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	var doc string
	var version int64
	var local, incoming sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT id::text,version,local_revision,incoming_revision FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series='general' AND content_locale='zh-Hant' FOR UPDATE`, issue).Scan(&doc, &version, &local, &incoming)
	if errors.Is(err, sql.ErrNoRows) {
		return result, recoveryBlocked("missing_local")
	}
	if err != nil {
		return result, err
	}
	if !local.Valid {
		return result, recoveryBlocked("missing_local")
	}
	if canonical != input.CanonicalVersion || version != input.OnlineVersion || local.Int64 != input.Revision {
		return result, bulletins.ErrPrecondition
	}
	if !pdfPublished {
		return result, recoveryBlocked("pdf_not_published")
	}
	if incoming.Valid {
		return result, recoveryBlocked("review_required")
	}
	var raw, issues []byte
	var sourceChecksum, hash, proof, renderer, extractor string
	err = tx.QueryRowContext(ctx, `SELECT document_json,review_issues,source_asset_checksum,content_hash,COALESCE(layout_validation_hash,''),renderer_artifact_sha256,extractor_version FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, doc, input.Revision).Scan(&raw, &issues, &sourceChecksum, &hash, &proof, &renderer, &extractor)
	if err != nil {
		return result, err
	}
	var document onlinebulletins.Document
	var findings []onlinebulletins.ReviewIssue
	if json.Unmarshal(raw, &document) != nil || json.Unmarshal(issues, &findings) != nil {
		return result, recoveryBlocked("incompatible_proof")
	}
	if len(findings) != 0 {
		return result, recoveryBlocked("review_required")
	}
	if checksum == "" || sourceChecksum != checksum || document.SourceAssetChecksum != checksum {
		return result, recoveryBlocked("source_changed")
	}
	validation, err := onlinebulletins.ValidateDocument(document)
	if err != nil || len(validation) != 0 || proof == "" || proof != input.LayoutValidationHash || document.LayoutManifest.LayoutValidationHash != proof || document.LayoutManifest.ContentHash != hash || document.LayoutManifest.RendererArtifactSha256 != renderer || extractor != onlinebulletins.RecognitionExtractorVersion() {
		return result, recoveryBlocked("incompatible_proof")
	}
	// Qualification uses the existing immutable semantic update and its exact
	// measured result. Revoked automatic authority is deliberately not renewed.
	var snapshot, inputRaw, evidenceRaw, mappingsRaw, layoutMetadata, templateJSON []byte
	var snapshotHash string
	var sourceAsset, semanticChecksum string
	err = tx.QueryRowContext(ctx, `SELECT e.snapshot_json,u.input_json,u.evidence_json,u.mappings_json,l.canonical_metadata,e.source_asset_id,e.source_asset_checksum,e.snapshot_sha256,e.template_snapshot_json
 FROM hhc_web.online_bulletin_layout_job l JOIN hhc_web.online_bulletin_update_job u ON u.document_id=l.document_id AND u.result_revision=l.source_revision AND u.status='ready' AND u.protocol_sha256=$3
 JOIN hhc_web.online_bulletin_extraction_job e ON e.id=u.extraction_job_id AND e.status='review_ready' AND e.extractor_version=$4 AND NOT e.superseded
 WHERE l.document_id=$1 AND l.result_revision=$2 AND l.status='ready' ORDER BY l.created_at DESC LIMIT 1 FOR UPDATE OF l,u,e`, doc, input.Revision, onlinebulletins.UpdateProtocolSHA256(), onlinebulletins.RecognitionExtractorVersion()).Scan(&snapshot, &inputRaw, &evidenceRaw, &mappingsRaw, &layoutMetadata, &sourceAsset, &semanticChecksum, &snapshotHash, &templateJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return result, recoveryBlocked("semantic_proof_missing")
	}
	if err != nil {
		return result, err
	}
	if sourceAsset != asset || semanticChecksum != checksum {
		return result, recoveryBlocked("source_changed")
	}
	var source onlineExtractionSnapshot
	var semantic onlinebulletins.UpdateInput
	var evidence onlinebulletins.UpdateEvidence
	var mappings []onlinebulletins.SentenceMapping
	var measuredMetadata onlinebulletins.CanonicalMetadata
	if json.Unmarshal(snapshot, &source) != nil || source.Candidate.Document == nil || !onlinebulletins.ValidRecognitionEvidence(source.Candidate.Recognition, *source.Candidate.Document) || json.Unmarshal(inputRaw, &semantic) != nil || json.Unmarshal(evidenceRaw, &evidence) != nil || !onlinebulletins.ValidUpdateEvidence(semantic, evidence) || json.Unmarshal(mappingsRaw, &mappings) != nil {
		return result, recoveryBlocked("semantic_proof_missing")
	}
	snapshotDigest := sha256.Sum256(mustJSON(source))
	var frozen *onlinebulletins.TemplateSettings
	if json.Unmarshal(templateJSON, &frozen) != nil || hex.EncodeToString(snapshotDigest[:]) != snapshotHash || source.Canonical != metadata || source.Candidate.Document.IssueID != issue || source.Candidate.Document.Series != "general" || source.Candidate.Document.ContentLocale != "zh-Hant" || source.Candidate.Document.SourceAssetChecksum != checksum || source.Candidate.Evidence.SourceSHA256 != checksum || !reflect.DeepEqual(source.Candidate.Document.TemplateSnapshot, frozen) || semantic.Canonical.IssueID != issue || semantic.Canonical.Series != "general" || semantic.Canonical.ContentLocale != "zh-Hant" || semantic.Incoming.SourceAssetChecksum != checksum || semantic.Evidence.SourceSHA256 != checksum || !reflect.DeepEqual(semantic.Recognition, source.Candidate.Recognition) || !reflect.DeepEqual(semantic.Evidence, source.Candidate.Evidence) {
		return result, recoveryBlocked("semantic_proof_missing")
	}
	if json.Unmarshal(layoutMetadata, &measuredMetadata) != nil || measuredMetadata != metadata || semantic.Canonical.Metadata != metadata {
		return result, recoveryBlocked("stale_metadata")
	}
	receiptID := platform.NewID()
	// Supersede prior automatic intent before granting this exact synchronous act.
	if err = r.changeBulletinIntent(ctx, tx, issue, "general", "zh-Hant", "superseded", "recovery_started", actor, now, receiptID); err != nil {
		return result, err
	}
	if err = r.cancelOnlineAutomaticIntent(ctx, tx, doc, now); err != nil {
		return result, err
	}
	if err = r.changeBulletinIntent(ctx, tx, issue, "general", "zh-Hant", "authorized", "recovery_authorized", actor, now, receiptID); err != nil {
		return result, err
	}
	var confirmed onlinebulletins.ConfirmedRevision
	var confirmation bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1 AND revision=$2)`, doc, input.Revision).Scan(&confirmation); err != nil {
		return result, err
	}
	if confirmation {
		confirmed = onlinebulletins.ConfirmedRevision{DocumentID: doc, Revision: input.Revision, Version: version}
	} else {
		confirmed, err = r.confirmOnlineBulletinTx(ctx, tx, issue, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: canonical, OnlineVersion: version, Revision: input.Revision, LayoutValidationHash: proof, PageCount: len(document.Pages), AcceptedIssues: []onlinebulletins.ReviewIssue{}}, actor, now, nil)
		if err != nil {
			if errors.Is(err, bulletins.ErrConflict) {
				return result, recoveryBlocked("layout_not_ready")
			}
			return result, err
		}
	}
	decisions := []onlinebulletins.SentenceMapping{}
	for _, m := range mappings {
		if !strings.HasPrefix(m.FromSentenceID, "canonical-") && (m.Status == "removed" || m.Status == "split" || m.Status == "merged") {
			decisions = append(decisions, m)
		}
	}
	publication, err := r.publishOnlineBulletinTx(ctx, tx, issue, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: canonical, OnlineVersion: confirmed.Version, Revision: input.Revision, Mappings: decisions}, actor, now, nil)
	if err != nil {
		if errors.Is(err, bulletins.ErrConflict) {
			return result, recoveryBlocked("incompatible_proof")
		}
		return result, err
	}
	if err = r.changeBulletinIntent(ctx, tx, issue, "general", "zh-Hant", "completed", "recovery_published", actor, now, receiptID); err != nil {
		return result, err
	}
	result = onlinebulletins.RecoveryResult{Receipt: onlinebulletins.RecoveryReceipt{ID: receiptID, OperationKey: key, DocumentID: doc, Revision: input.Revision, CanonicalVersion: canonical, OnlineVersion: input.OnlineVersion, LayoutValidationHash: proof, Outcome: "published", CreatedAt: now}, Publication: publication}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_recovery_receipt(id,issue_id,actor_id,operation_key,input_json,result_json,request_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, receiptID, issue, actor, key, inputJSON, mustJSON(result), p.RequestID, now); err != nil {
		return result, mapConflict(err)
	}
	return result, tx.Commit()
}
