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
)

// Exact extraction -> layout -> Local lineage excludes authored edits and Incoming.
const recognitionReleaseFrom = ` FROM hhc_web.online_bulletin_extraction_job e
 JOIN hhc_web.online_bulletin_document d ON d.id=e.document_id
 JOIN hhc_web.bulletin_issue i ON i.id=e.issue_id
 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant'
 JOIN hhc_web.online_bulletin_layout_job l ON l.document_id=d.id AND l.source_revision=e.result_revision AND l.result_revision=d.local_revision AND l.status='ready'
 JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision
 WHERE e.extractor_version=$1 AND e.status='review_ready' AND NOT e.superseded
 AND e.authorizer_id IS NOT NULL AND e.authorizer_request_id IS NOT NULL
 AND d.series='general' AND d.content_locale='zh-Hant' AND d.incoming_revision IS NULL
 AND d.published_revision IS DISTINCT FROM d.local_revision
 AND v.status='published' AND v.pdf_asset_id=e.source_asset_id AND v.pdf_sha256=e.source_asset_checksum
 AND r.source_asset_checksum=e.source_asset_checksum AND r.review_issues='[]'::jsonb
 AND e.snapshot_json->'canonicalMetadata'=l.canonical_metadata
 AND l.canonical_metadata=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)
 AND EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version p WHERE p.issue_id=i.id AND p.series=v.series AND p.locale=v.locale AND p.asset_id=v.pdf_asset_id AND p.title=v.title AND p.subtitle=v.subtitle)`

func (r *Repository) AutoPublishNextRecognition(ctx context.Context, now time.Time) (bool, error) {
	if !r.llmBulletinExtraction {
		return false, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var issue, actor, request string
	var canonicalVersion, onlineVersion, revision int64
	var raw, snapshot []byte
	err = tx.QueryRowContext(ctx, `SELECT i.id::text,e.authorizer_id::text,e.authorizer_request_id,i.version,d.version,d.local_revision,r.document_json,e.snapshot_json`+recognitionReleaseFrom+` ORDER BY e.created_at,e.id LIMIT 1 FOR UPDATE OF i,v,d,e SKIP LOCKED`, onlinebulletins.RecognitionExtractorVersion()).Scan(&issue, &actor, &request, &canonicalVersion, &onlineVersion, &revision, &raw, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var document onlinebulletins.Document
	var evidence onlineExtractionSnapshot
	if !auditclient.ValidActorRequest(actor, request) || json.Unmarshal(raw, &document) != nil || json.Unmarshal(snapshot, &evidence) != nil || evidence.Candidate.Document == nil || !onlinebulletins.ValidRecognitionEvidence(evidence.Candidate.Recognition, *evidence.Candidate.Document) || len(evidence.Issues) != 1 || evidence.Issues[0].Code != "layout_validation_required" {
		return false, bulletins.ErrConflict
	}
	ctx = auditclient.WithProvenance(ctx, actor, request)
	confirmed, err := r.confirmOnlineBulletinTx(ctx, tx, issue, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: canonicalVersion, OnlineVersion: onlineVersion, Revision: revision, LayoutValidationHash: document.LayoutManifest.LayoutValidationHash, PageCount: len(document.Pages), AcceptedIssues: []onlinebulletins.ReviewIssue{}}, actor, now, nil)
	if err != nil {
		return false, err
	}
	_, err = r.publishOnlineBulletinTx(ctx, tx, issue, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: canonicalVersion, OnlineVersion: confirmed.Version, Revision: revision}, actor, now, nil)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}
