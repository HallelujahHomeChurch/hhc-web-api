package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

// Caller holds the current issue/edition locks and checked canonical If-Match.
// This preserves a provable current automatic chain; it never joins or repairs
// an already-owned PDF workflow. Historical outbox failures are not authority.
// Do not acquire extraction/update locks after i/v: background recognition can
// change later; preserving its plan does not promise that recognition succeeds.
func (r *Repository) preflightEarlyBulletinPDF(ctx context.Context, tx *sql.Tx, issue, series, locale string) error {
	if series != "general" || locale != "zh-Hant" || !r.llmBulletinExtraction {
		return bulletins.ErrConflict
	}
	var extraction, document, status, authorizer, request string
	err := tx.QueryRowContext(ctx, `SELECT e.id::text,d.id::text,e.status,COALESCE(e.authorizer_id::text,''),e.authorizer_request_id
 FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=$2 AND v.locale=$3
 JOIN hhc_web.online_bulletin_document d ON d.issue_id=i.id AND d.series=v.series AND d.content_locale=v.locale
 JOIN hhc_web.online_bulletin_extraction_job e ON e.document_id=d.id
 WHERE i.id=$1 AND i.status NOT IN('publishing','unpublishing') AND v.status='draft' AND v.auto_publish
 AND e.auto_publish AND NOT e.superseded AND e.status IN('queued','parsing','review_ready') AND e.extractor_version=$4
 AND e.authorizer_request_id=v.auto_publish_request AND e.source_asset_id=v.pdf_asset_id AND e.source_asset_checksum=v.pdf_sha256
 AND COALESCE(e.requested_metadata,e.snapshot_json->'canonicalMetadata')=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)
 AND (v.online_intent_epoch=0 OR e.publication_intent_id=v.online_intent_id AND e.publication_intent_epoch=v.online_intent_epoch AND (SELECT t.status IN('authorized','running') FROM hhc_web.online_bulletin_intent_transition t WHERE t.intent_id=v.online_intent_id ORDER BY t.sequence DESC LIMIT 1))
 ORDER BY e.created_at DESC,e.id DESC LIMIT 1`, issue, series, locale, onlinebulletins.RecognitionExtractorVersion()).Scan(&extraction, &document, &status, &authorizer, &request)
	if errors.Is(err, sql.ErrNoRows) {
		return bulletins.ErrConflict
	}
	if err != nil {
		return err
	}
	if !auditclient.ValidActorRequest(authorizer, request) {
		return bulletins.ErrConflict
	}
	var healthy bool
	if status != "review_ready" {
		err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_update_job WHERE extraction_job_id=$1)`, extraction).Scan(&healthy)
	} else {
		// Queued/planning plans must still see their exact Local version. Ready
		// plans must still own Local (or its measured result), without failed input
		// or any PDF workflow binding. Only this extraction's update is considered.
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_update_job u
 JOIN hhc_web.online_bulletin_document d ON d.id=u.document_id
 JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=d.series AND v.locale=d.content_locale
 WHERE u.extraction_job_id=$1 AND u.protocol_sha256=$2 AND u.pdf_workflow_id IS NULL AND i.version>=u.canonical_version
 AND u.input_json->'canonical'->'metadata'=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)
 AND ((u.status IN('queued','planning') AND d.version=u.online_version)
 OR (u.status='ready' AND u.result_revision IS NOT NULL AND d.incoming_revision IS NULL
 AND NOT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_layout_job f WHERE f.document_id=d.id AND f.source_revision=u.result_revision AND f.status='failed')
 AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_layout_job l LEFT JOIN hhc_web.online_bulletin_revision local ON local.document_id=d.id AND local.revision=d.local_revision
 WHERE l.document_id=d.id AND l.source_revision=u.result_revision
 AND l.canonical_metadata=u.input_json->'canonical'->'metadata'
 AND ((d.local_revision=u.result_revision AND l.status IN('queued','validating'))
 OR (l.status='ready' AND l.result_revision=d.local_revision AND local.source_asset_checksum=v.pdf_sha256 AND local.review_issues='[]'::jsonb))))))`, extraction, onlinebulletins.UpdateProtocolSHA256()).Scan(&healthy)
	}
	if err != nil {
		return err
	}
	if !healthy {
		return bulletins.ErrConflict
	}
	return nil
}
