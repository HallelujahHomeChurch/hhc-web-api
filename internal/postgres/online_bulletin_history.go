package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func (r *Repository) OnlineBulletinHistory(ctx context.Context, issueID, series, locale string, before int64, limit int) (onlinebulletins.RevisionPage, error) {
	page := onlinebulletins.RevisionPage{Items: []onlinebulletins.RevisionSummary{}}
	if before < 0 || limit < 1 || limit > 50 {
		return page, bulletins.ErrInvalid
	}
	err := r.db.QueryRowContext(ctx, `SELECT id::text FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series=$2 AND content_locale=$3`, issueID, series, locale).Scan(&page.DocumentID)
	if errors.Is(err, sql.ErrNoRows) {
		return page, bulletins.ErrNotFound
	}
	if err != nil {
		return page, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT revision,created_by,created_at,source_asset_checksum,layout_validation_hash IS NOT NULL FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND ($2::bigint=0 OR revision<$2) ORDER BY revision DESC LIMIT $3`, page.DocumentID, before, limit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var item onlinebulletins.RevisionSummary
		if err = rows.Scan(&item.Revision, &item.CreatedBy, &item.CreatedAt, &item.SourceAssetChecksum, &item.LayoutValidated); err != nil {
			return page, err
		}
		page.Items = append(page.Items, item)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		next := page.Items[len(page.Items)-1].Revision
		page.NextBefore = &next
	}
	return page, nil
}

func (r *Repository) OnlineBulletinRevision(ctx context.Context, issueID, series, locale string, revision int64) (onlinebulletins.HistoricalRevision, error) {
	var result onlinebulletins.HistoricalRevision
	if revision < 1 {
		return result, bulletins.ErrInvalid
	}
	var document, issues, canonical []byte
	// Preview history with its frozen metadata, never today's CMS title/date.
	// Source asset IDs, extraction evidence and worker leases remain private.
	err := r.db.QueryRowContext(ctx, `SELECT d.id::text,r.revision,r.document_json,r.review_issues,r.created_by,r.created_at,
	 COALESCE((SELECT p.canonical_metadata FROM hhc_web.online_bulletin_publication p WHERE p.document_id=d.id AND p.revision=r.revision),
	 (SELECT j.canonical_metadata FROM hhc_web.online_bulletin_layout_job j WHERE j.document_id=d.id AND (j.source_revision=r.revision OR j.result_revision=r.revision) ORDER BY j.created_at DESC,j.id DESC LIMIT 1),
	 (SELECT e.snapshot_json->'canonicalMetadata' FROM hhc_web.online_bulletin_extraction_job e WHERE e.document_id=d.id AND e.result_revision=r.revision AND e.snapshot_json IS NOT NULL ORDER BY e.created_at DESC,e.id DESC LIMIT 1))
	 FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=$4
	 WHERE d.issue_id=$1 AND d.series=$2 AND d.content_locale=$3`, issueID, series, locale, revision).Scan(&result.DocumentID, &result.Revision, &document, &issues, &result.CreatedBy, &result.CreatedAt, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if json.Unmarshal(document, &result.Document) != nil || json.Unmarshal(issues, &result.ReviewIssues) != nil || json.Unmarshal(canonical, &result.CanonicalMetadata) != nil {
		return result, bulletins.ErrConflict
	}
	return result, nil
}
