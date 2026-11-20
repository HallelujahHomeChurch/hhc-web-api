package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

const independentPDFEligibility = ` FROM hhc_web.bulletin_independent_release b JOIN hhc_web.online_bulletin_publication_intent h ON h.id=b.intent_id
 JOIN hhc_web.bulletin_version v ON v.online_intent_id=h.id AND v.online_intent_epoch=h.epoch
 JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id
 WHERE b.pdf_workflow_id IS NULL AND v.auto_publish
 AND h.issue_id=i.id AND h.series=v.series AND h.locale=v.locale AND h.source_asset_id=v.pdf_asset_id
 AND v.verified_pdf_asset_id=v.pdf_asset_id AND length(v.pdf_sha256)=64 AND (h.source_checksum='' OR h.source_checksum=v.pdf_sha256)
 AND h.request_id=v.auto_publish_request AND v.status='draft' AND i.status NOT IN('publishing','unpublishing')
 AND (SELECT status IN('authorized','running','failed') FROM hhc_web.online_bulletin_intent_transition WHERE intent_id=h.id ORDER BY sequence DESC LIMIT 1)`

// The issue lock serializes branch submission, revocation and projection writes.
// Empty issue picks up a deferred submission when another edition was publishing.
func (r *Repository) scheduleIndependentPDF(ctx context.Context, tx *sql.Tx, issue string, now time.Time) (bool, error) {
	var intent, actor, request, checksum string
	var version int64
	var notify bool
	err := tx.QueryRowContext(ctx, `SELECT i.id::text,h.id::text,h.actor_id,h.request_id,i.version,v.auto_notify,v.pdf_sha256
`+independentPDFEligibility+` AND (NULLIF($1,'')::uuid IS NULL OR i.id=NULLIF($1,'')::uuid)
 ORDER BY h.created_at,h.id LIMIT 1 FOR UPDATE OF i,v,b SKIP LOCKED`, issue).Scan(&issue, &intent, &actor, &request, &version, &notify, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx = auditclient.WithProvenance(ctx, actor, request)
	w, err := r.startBulletinPublishTx(ctx, tx, issue, "general", "zh-Hant", version, notify, actor, now)
	if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_independent_release SET pdf_workflow_id=$2 WHERE intent_id=$1`, intent, w.ID); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET payload_json=payload_json||jsonb_build_object('sourceChecksum',$2::text) WHERE event_type='bulletin.publish.ensure_asset' AND aggregate_id=$1 AND aggregate_version=$3`, issue, checksum, w.AggregateVersion)
	return true, err
}

// A publisher can finish the online branch manually under the same source
// lock. The PDF worker keeps its original authorization and notification choice.
func markIndependentOnline(ctx context.Context, tx *sql.Tx, issue, series, locale string) (bool, error) {
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_independent_release b SET online_completed=true FROM hhc_web.bulletin_version v WHERE v.issue_id=$1 AND v.series=$2 AND v.locale=$3 AND v.auto_publish AND b.intent_id=v.online_intent_id`, issue, series, locale)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

// Finish the shared authorization after both projections reached the member;
// the PDF projection must exist before a requested g0 can be queued.
func (r *Repository) finishIndependentRelease(ctx context.Context, tx *sql.Tx, issue, actor string, now time.Time) error {
	var doc string
	var notify bool
	var number sql.NullInt64
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT d.id::text,v.auto_notify,i.issue_number,i.version FROM hhc_web.bulletin_version v JOIN hhc_web.bulletin_independent_release b ON b.intent_id=v.online_intent_id JOIN hhc_web.online_bulletin_document d ON (d.issue_id,d.series,d.content_locale)=(v.issue_id,v.series,v.locale)
 JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id
 JOIN hhc_web.bulletin_published_version p ON (p.issue_id,p.series,p.locale,p.asset_id,p.title,p.subtitle)=(v.issue_id,v.series,v.locale,v.pdf_asset_id,v.title,v.subtitle)
 WHERE v.issue_id=$1 AND v.series='general' AND v.locale='zh-Hant' AND v.status='published' AND b.online_completed AND v.auto_publish`, issue).Scan(&doc, &notify, &number, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if notify {
		if err = enqueueBulletinPublicationNotification(ctx, tx, issue, "general", number, actor, version, now); err != nil {
			return err
		}
	}
	if err = r.changeBulletinIntent(ctx, tx, issue, "general", "zh-Hant", "completed", "automatic_published", actor, now, ""); err != nil {
		return err
	}
	return r.cancelOnlineAutomaticIntent(ctx, tx, doc, now)
}
