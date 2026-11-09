package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

// Only an authorized, independently accepted update and its exact measured Local
// may continue. Draft/unpublish cancels the request; raw extraction is insufficient.
const recognitionReleaseFrom = ` FROM hhc_web.online_bulletin_update_job u
 JOIN hhc_web.online_bulletin_extraction_job e ON e.id=u.extraction_job_id
 JOIN hhc_web.online_bulletin_document d ON d.id=u.document_id
 JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=d.series AND v.locale=d.content_locale
 JOIN hhc_web.online_bulletin_layout_job l ON l.document_id=d.id AND l.source_revision=u.result_revision AND l.result_revision=d.local_revision AND l.status='ready'
 JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision
 WHERE u.status='ready' AND u.protocol_sha256=$2 AND e.extractor_version=$1
 AND e.status='review_ready' AND NOT e.superseded AND e.auto_publish AND v.auto_publish
 AND e.authorizer_id IS NOT NULL AND e.authorizer_request_id=v.auto_publish_request
 AND d.series='general' AND d.content_locale='zh-Hant' AND d.incoming_revision IS NULL
 AND d.published_revision IS DISTINCT FROM d.local_revision
 AND v.pdf_asset_id=e.source_asset_id AND v.pdf_sha256=e.source_asset_checksum
 AND r.source_asset_checksum=e.source_asset_checksum AND r.review_issues='[]'::jsonb
 AND u.input_json->'canonical'->'metadata'=l.canonical_metadata
 AND l.canonical_metadata=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)
 AND ((u.pdf_workflow_id IS NULL AND i.version=u.canonical_version)
   OR EXISTS(SELECT 1 FROM hhc_web.publication_workflow w WHERE w.id=u.pdf_workflow_id AND w.aggregate_version=i.version AND w.asset_id=v.pdf_asset_id AND w.status='completed'))
 AND (v.status='draft' AND u.pdf_workflow_id IS NULL OR v.status='published' AND EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version p WHERE p.issue_id=i.id AND p.series=v.series AND p.locale=v.locale AND p.asset_id=v.pdf_asset_id AND p.title=v.title AND p.subtitle=v.subtitle))`

func (r *Repository) AutoPublishNextRecognition(ctx context.Context, now time.Time) (bool, error) {
	if !r.llmBulletinExtraction {
		return false, nil
	}
	if now.IsZero() {
		return false, bulletins.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var issue, actor, request, id, pdfStatus string
	var canonicalVersion, onlineVersion, revision int64
	var raw, snapshot, inputJSON, evidenceJSON, mappingsJSON []byte
	var notify bool
	err = tx.QueryRowContext(ctx, `SELECT i.id::text,e.authorizer_id::text,e.authorizer_request_id,i.version,d.version,d.local_revision,r.document_json,e.snapshot_json,u.input_json,u.evidence_json,u.mappings_json,u.id::text,v.status,v.auto_notify`+recognitionReleaseFrom+` ORDER BY u.created_at,u.id LIMIT 1 FOR UPDATE OF i,v,d,e,u SKIP LOCKED`, onlinebulletins.RecognitionExtractorVersion(), onlinebulletins.UpdateProtocolSHA256()).Scan(&issue, &actor, &request, &canonicalVersion, &onlineVersion, &revision, &raw, &snapshot, &inputJSON, &evidenceJSON, &mappingsJSON, &id, &pdfStatus, &notify)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var document onlinebulletins.Document
	var source onlineExtractionSnapshot
	var input onlinebulletins.UpdateInput
	var evidence onlinebulletins.UpdateEvidence
	var mappings []onlinebulletins.SentenceMapping
	if !auditclient.ValidActorRequest(actor, request) || json.Unmarshal(raw, &document) != nil || json.Unmarshal(snapshot, &source) != nil || source.Candidate.Document == nil || !onlinebulletins.ValidRecognitionEvidence(source.Candidate.Recognition, *source.Candidate.Document) || json.Unmarshal(inputJSON, &input) != nil || json.Unmarshal(evidenceJSON, &evidence) != nil || !onlinebulletins.ValidUpdateEvidence(input, evidence) || json.Unmarshal(mappingsJSON, &mappings) != nil {
		return false, bulletins.ErrConflict
	}
	ctx = auditclient.WithProvenance(ctx, actor, request)
	if pdfStatus == "draft" {
		workflow, err := r.startBulletinPublishTx(ctx, tx, issue, "general", "zh-Hant", canonicalVersion, notify, actor, now)
		if err != nil {
			return false, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET pdf_workflow_id=$2,updated_at=$3 WHERE id=$1`, id, workflow.ID, now); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	confirmed, err := r.confirmOnlineBulletinTx(ctx, tx, issue, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: canonicalVersion, OnlineVersion: onlineVersion, Revision: revision, LayoutValidationHash: document.LayoutManifest.LayoutValidationHash, PageCount: len(document.Pages), AcceptedIssues: []onlinebulletins.ReviewIssue{}}, actor, now, nil)
	if err != nil {
		return false, err
	}
	decisions := []onlinebulletins.SentenceMapping{}
	for _, mapping := range mappings {
		if !strings.HasPrefix(mapping.FromSentenceID, "canonical-") && (mapping.Status == "removed" || mapping.Status == "split" || mapping.Status == "merged") {
			decisions = append(decisions, mapping)
		}
	}
	if _, err = r.publishOnlineBulletinTx(ctx, tx, issue, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: canonicalVersion, OnlineVersion: confirmed.Version, Revision: revision, Mappings: decisions}, actor, now, nil); err != nil {
		return false, err
	}
	if err = cancelOnlineAutomaticIntent(ctx, tx, confirmed.DocumentID, now); err != nil {
		return false, err
	}
	return true, tx.Commit()
}
