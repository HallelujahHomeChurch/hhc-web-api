package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

// The authenticated HTTP boundary checks publisher capability. Only its audited
// request can authorize this exact source; cancellation also fences in-flight work.
func (r *Repository) setAutomaticBulletinIntent(ctx context.Context, tx *sql.Tx, issue, series, locale string, enabled, notify bool, actor string, now time.Time) error {
	if notify && !enabled || enabled && (series != "general" || locale != "zh-Hant" || !r.llmBulletinExtraction || !r.automaticBulletinExtraction) {
		return bulletins.ErrInvalid
	}
	request := ""
	if enabled {
		p, ok := auditclient.ProvenanceFromContext(ctx)
		if !ok || p.ActorType != "user" || p.ActorID != actor || !auditclient.ValidActorRequest(actor, p.RequestID) {
			return bulletins.ErrAuditUnavailable
		}
		request = p.RequestID
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET auto_publish=$4,auto_notify=$5,auto_publish_request=$6 WHERE issue_id=$1 AND series=$2 AND locale=$3`, issue, series, locale, enabled, notify, request); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET auto_publish=false WHERE issue_id=$1 AND series=$2 AND content_locale=$3 AND auto_publish`, issue, series, locale); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$4 WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series=$2 AND content_locale=$3) AND status IN('queued','planning')`, issue, series, locale, now)
	return err
}

func cancelOnlineAutomaticIntent(ctx context.Context, tx *sql.Tx, documentID string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version v SET auto_publish=false,auto_notify=false,auto_publish_request='' FROM hhc_web.online_bulletin_document d WHERE d.id=$1 AND v.issue_id=d.issue_id AND v.series=d.series AND v.locale=d.content_locale`, documentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET auto_publish=false WHERE document_id=$1 AND auto_publish`, documentID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$2 WHERE document_id=$1 AND status IN('queued','planning')`, documentID, now)
	return err
}
