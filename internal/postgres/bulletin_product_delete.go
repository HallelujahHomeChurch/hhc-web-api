package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

type bulletinDeletionKey struct{ issue, series, locale string }
type bulletinDeletionRanks map[bulletinDeletionKey]int

func (r bulletinDeletionRanks) eligibility(issue, series, locale string) *bulletins.DeletionEligibility {
	rank := r[bulletinDeletionKey{issue, series, locale}]
	return &bulletins.DeletionEligibility{Allowed: rank == 0, Reason: [4]string{"", "retained_history", "cleanup_pending", "published"}[rank]}
}

// Publication and reader history stay distinct from cancellable extraction.
// Reads and mutations share this predicate, and mutations hold the issue lock.
func bulletinDeletionReasons(ctx context.Context, query bulletinQueryer, issues []string) (bulletinDeletionRanks, error) {
	rows, err := query.QueryContext(ctx, `
 SELECT v.issue_id::text,v.series,v.locale,CASE
 WHEN v.status='published' OR EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version p WHERE (p.issue_id,p.series,p.locale)=(v.issue_id,v.series,v.locale)) OR d.published_revision IS NOT NULL THEN 3
 WHEN i.status='unpublishing' OR v.status IN('unpublishing','unpublish_failed') OR COALESCE(v.public_grant_id,'')<>'' OR COALESCE(v.retiring_grant_id,'')<>'' THEN 2
 WHEN i.status='publishing' AND NOT (v.status='publishing' AND v.auto_publish AND EXISTS(
  SELECT 1 FROM hhc_web.online_bulletin_update_job u JOIN hhc_web.publication_workflow w ON w.id=u.pdf_workflow_id
  WHERE u.document_id=d.id AND w.workflow_type='bulletin_publish' AND w.aggregate_version=i.version AND w.asset_id=v.pdf_asset_id AND w.status NOT IN('completed','failed','cancelled')
 )) THEN 2
 WHEN v.published_at IS NOT NULL OR EXISTS(SELECT 1 FROM hhc_web.publication_workflow w WHERE (w.resource_id,w.series,w.locale)=(v.issue_id,v.series,v.locale) AND w.workflow_type='bulletin_publish' AND w.resource_type='bulletin' AND w.status='completed')
   OR EXISTS(SELECT 1 FROM hhc_web.online_bulletin_publication p WHERE p.document_id=d.id)
   OR EXISTS(SELECT 1 FROM hhc_web.online_bulletin_conversion_job c JOIN hhc_web.online_bulletin_publication p ON p.document_id=c.document_id WHERE c.source_document_id=d.id) THEN 1
 ELSE 0 END
 FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id
 LEFT JOIN hhc_web.online_bulletin_document d ON (v.issue_id,v.series,v.locale)=(d.issue_id,d.series,d.content_locale)
 WHERE i.id=ANY($1::uuid[])
 UNION ALL
 SELECT i.id::text,'','',CASE WHEN i.published_at IS NOT NULL THEN 1 ELSE 0 END
 FROM hhc_web.bulletin_issue i WHERE i.id=ANY($1::uuid[])`, issues)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ranks := make(bulletinDeletionRanks)
	for rows.Next() {
		var edition bulletinDeletionKey
		var rank int
		if err = rows.Scan(&edition.issue, &edition.series, &edition.locale, &rank); err != nil {
			return nil, err
		}
		ranks[edition] = max(ranks[edition], rank)
		issue := bulletinDeletionKey{issue: edition.issue}
		ranks[issue] = max(ranks[issue], rank)
	}
	return ranks, rows.Err()
}

func checkBulletinDeletion(ctx context.Context, tx *sql.Tx, issue, series, locale string) error {
	ranks, err := bulletinDeletionReasons(ctx, tx, []string{issue})
	if err != nil {
		return err
	}
	switch ranks[bulletinDeletionKey{issue, series, locale}] {
	case 3:
		return bulletins.ErrDeletePublished
	case 2:
		return bulletins.ErrDeleteCleanupPending
	case 1:
		return bulletins.ErrDeleteRetainedHistory
	default:
		return nil
	}
}

// Delete only unpublished working data. The issue lock fences all publication
// writers; queue/lease deletion fences stale completions and leaves audit behind.
func deleteBulletinDrafts(ctx context.Context, tx *sql.Tx, issue, series, locale string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT id::text FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND ($2='' OR (series=$2 AND content_locale=$3)) ORDER BY id FOR UPDATE`, issue, series, locale)
	if err != nil {
		return err
	}
	docs := []string{}
	for rows.Next() {
		var document string
		if err = rows.Scan(&document); err != nil {
			rows.Close()
			return err
		}
		docs = append(docs, document)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if series == "" || series == "general" && locale == "zh-Hant" {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='failed',last_error='bulletin_deleted',claimed_until=NULL,updated_at=$2 WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction' AND status IN('pending','processing')`, issue, now); err != nil {
			return err
		}
	}
	if len(docs) == 0 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event e SET status='failed',last_error='bulletin_deleted',claimed_until=NULL,updated_at=$2
 WHERE e.status IN('pending','processing') AND (
   e.aggregate_id=ANY($1::uuid[]) AND e.event_type IN('bulletin.online.learn','bulletin.online.publish_simplified') OR e.event_type='bulletin.online.wake' AND (
    EXISTS(SELECT 1 FROM hhc_web.online_bulletin_extraction_job j WHERE j.id=e.aggregate_id AND j.document_id=ANY($1::uuid[])) OR
    EXISTS(SELECT 1 FROM hhc_web.online_bulletin_update_job j WHERE j.id=e.aggregate_id AND j.document_id=ANY($1::uuid[])) OR
    EXISTS(SELECT 1 FROM hhc_web.online_bulletin_layout_job j WHERE j.id=e.aggregate_id AND j.document_id=ANY($1::uuid[])) OR
    EXISTS(SELECT 1 FROM hhc_web.online_bulletin_conversion_job j WHERE j.id=e.aggregate_id AND (j.document_id=ANY($1::uuid[]) OR j.source_document_id=ANY($1::uuid[])))
   ))`, docs, now); err != nil {
		return err
	}
	for _, statement := range []string{
		`DELETE FROM hhc_web.online_bulletin_learning_example WHERE document_id=ANY($1::uuid[])`,
		`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=ANY($1::uuid[])`,
		`DELETE FROM hhc_web.online_bulletin_conversion_job WHERE document_id=ANY($1::uuid[]) OR source_document_id=ANY($1::uuid[])`,
		`DELETE FROM hhc_web.online_bulletin_layout_job WHERE document_id=ANY($1::uuid[])`,
		`DELETE FROM hhc_web.online_bulletin_extraction_job WHERE document_id=ANY($1::uuid[])`,
		`UPDATE hhc_web.online_bulletin_document SET base_revision=NULL,local_revision=NULL,incoming_revision=NULL WHERE id=ANY($1::uuid[])`,
		`DELETE FROM hhc_web.online_bulletin_revision WHERE document_id=ANY($1::uuid[])`,
		`DELETE FROM hhc_web.online_bulletin_document WHERE id=ANY($1::uuid[])`,
	} {
		if _, err = tx.ExecContext(ctx, statement, docs); err != nil {
			return err
		}
	}
	return nil
}

func lockBulletinIssueVersion(ctx context.Context, tx *sql.Tx, issue string, expected int64) error {
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, issue).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return bulletins.ErrNotFound
	}
	if err != nil {
		return err
	}
	if version != expected {
		return bulletins.ErrPrecondition
	}
	return nil
}

func (r *Repository) TrackBulletinUploadAsset(ctx context.Context, issue string, edition bulletins.BulletinEdition, asset, actor string, now time.Time) error {
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, issue).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		// A deleted issue cannot accept a just-created upload session.
		if err = enqueueAssetDeletes(ctx, tx, "bulletin", issue, 1, []string{asset}, now); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		return bulletins.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_upload_asset(issue_id,series,locale,asset_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, issue, edition.Series, edition.Locale, asset); err != nil {
		return err
	}
	return tx.Commit()
}

func deleteEditionBulletinAssets(ctx context.Context, tx *sql.Tx, issue, series, locale, source string, version int64, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
 SELECT asset_id FROM (
  SELECT $4::text AS asset_id UNION SELECT asset_id FROM hhc_web.bulletin_upload_asset WHERE issue_id=$1 AND series=$2 AND locale=$3
 ) assets WHERE asset_id<>'' AND NOT EXISTS(SELECT 1 FROM hhc_web.bulletin_version WHERE issue_id=$1 AND pdf_asset_id=assets.asset_id)
 AND NOT EXISTS(SELECT 1 FROM hhc_web.bulletin_upload_asset other WHERE other.issue_id=$1 AND (other.series,other.locale)<>($2,$3) AND other.asset_id=assets.asset_id)`, issue, series, locale, source)
	if err != nil {
		return err
	}
	defer rows.Close()
	assets := []string{}
	for rows.Next() {
		var asset string
		if err = rows.Scan(&asset); err != nil {
			return err
		}
		assets = append(assets, asset)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_upload_asset WHERE issue_id=$1 AND series=$2 AND locale=$3`, issue, series, locale); err != nil {
		return err
	}
	return enqueueAssetDeletes(ctx, tx, "bulletin", issue, version, assets, now)
}
