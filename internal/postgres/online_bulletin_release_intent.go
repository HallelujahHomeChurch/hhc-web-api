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
	reason := "explicit_draft"
	if enabled {
		reason = "automatic_authorized"
	}
	return r.setAutomaticBulletinIntentReason(ctx, tx, issue, series, locale, enabled, notify, actor, now, reason)
}
func (r *Repository) setAutomaticBulletinIntentReason(ctx context.Context, tx *sql.Tx, issue, series, locale string, enabled, notify bool, actor string, now time.Time, reason string) error {
	if notify && !enabled || enabled && (series != "general" || locale != "zh-Hant" || !r.llmBulletinExtraction || !r.automaticBulletinExtraction) {
		return bulletins.ErrInvalid
	}
	request := ""
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if ok && p.ActorType == "user" && p.ActorID == actor && auditclient.ValidActorRequest(actor, p.RequestID) {
		request = p.RequestID
	} else if enabled {
		return bulletins.ErrAuditUnavailable
	}
	status := "cancelled"
	if enabled {
		status = "authorized"
	}
	if err := r.changeBulletinIntent(ctx, tx, issue, series, locale, status, reason, actor, now, ""); err != nil {
		return err
	}
	if err := cancelAutomaticPDFPublication(ctx, tx, issue, series, locale, now); err != nil {
		return err
	}
	if enabled {
		// The new source owner composes after semantic selection; older draft layout must not advance Local meanwhile.
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$4 WHERE document_id IN(SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series=$2 AND content_locale=$3) AND status IN('queued','validating')`, issue, series, locale, now); err != nil {
			return err
		}
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

func (r *Repository) cancelOnlineAutomaticIntent(ctx context.Context, tx *sql.Tx, documentID string, now time.Time) error {
	var issue, series, locale string
	if err := tx.QueryRowContext(ctx, `SELECT issue_id::text,series,content_locale FROM hhc_web.online_bulletin_document WHERE id=$1`, documentID).Scan(&issue, &series, &locale); err != nil {
		return err
	}
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if ok {
		if err := r.changeBulletinIntent(ctx, tx, issue, series, locale, "cancelled", "manual_state_change", p.ActorID, now, ""); err != nil {
			return err
		}
	}
	if err := cancelAutomaticPDFPublication(ctx, tx, issue, series, locale, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version v SET auto_publish=false,auto_notify=false,auto_publish_request='' FROM hhc_web.online_bulletin_document d WHERE d.id=$1 AND v.issue_id=d.issue_id AND v.series=d.series AND v.locale=d.content_locale`, documentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET auto_publish=false WHERE document_id=$1 AND auto_publish`, documentID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$2 WHERE document_id=$1 AND status IN('queued','planning')`, documentID, now)
	return err
}

// Only workflows created by this automatic flow are cancelled. Already released
// projections and independently requested PDF workflows retain their authority.
func cancelAutomaticPDFPublication(ctx context.Context, tx *sql.Tx, issue, series, locale string, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `UPDATE hhc_web.publication_workflow w SET status='cancelled',error_code='AUTOMATIC_RELEASE_CANCELLED',error_detail='automatic_release_cancelled',updated_at=$4
 FROM hhc_web.online_bulletin_update_job u JOIN hhc_web.online_bulletin_document d ON d.id=u.document_id
 JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=d.series AND v.locale=d.content_locale
 WHERE u.pdf_workflow_id=w.id AND d.issue_id=$1 AND d.series=$2 AND d.content_locale=$3
 AND w.workflow_type='bulletin_publish' AND w.aggregate_version=i.version AND i.status='publishing' AND v.status='publishing' AND w.status NOT IN ('completed','failed','cancelled') RETURNING w.id::text`, issue, series, locale, now)
	if err != nil {
		return err
	}
	var workflows []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		workflows = append(workflows, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(workflows) == 0 {
		return nil
	}
	for _, id := range workflows {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='failed',claimed_until=NULL,last_error='automatic_release_cancelled',updated_at=$2 WHERE event_type='bulletin.publish.ensure_asset' AND payload_json->>'workflowId'=$1 AND status IN ('pending','processing')`, id, now); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='draft',version=version+1,updated_at=$4 WHERE issue_id=$1 AND series=$2 AND locale=$3 AND status='publishing'`, issue, series, locale, now); err != nil {
		return err
	}
	var version int64
	if err = tx.QueryRowContext(ctx, `UPDATE hhc_web.bulletin_issue SET version=version+1,updated_at=$2 WHERE id=$1 RETURNING version`, issue, now).Scan(&version); err != nil {
		return err
	}
	return recalculateBulletinIssueStatus(ctx, tx, issue, version, now)
}

func cancelPendingSimplifiedRelease(ctx context.Context, tx *sql.Tx, issueID string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='failed',claimed_until=NULL,last_error='source_publication_changed',updated_at=$2 WHERE event_type='bulletin.online.publish_simplified' AND payload_json->>'issueId'=$1 AND status IN ('pending','processing')`, issueID, now); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_conversion_job SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$2 WHERE issue_id=$1 AND publication_event_id IS NOT NULL AND status IN ('queued','converting')`, issueID, now)
	return err
}
