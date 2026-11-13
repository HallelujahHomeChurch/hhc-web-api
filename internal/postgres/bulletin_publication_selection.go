package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"time"
)

func (r *Repository) StartPublishSelection(ctx context.Context, issue string, input bulletins.PublishInput, expected int64, actor string, now time.Time) (bulletins.Workflow, error) {
	if input.OnlinePublication != "" && !r.explicitOnlinePublication {
		return bulletins.Workflow{}, bulletins.ErrOnlinePublicationUnavailable
	}
	if input.OnlinePublication != "" && input.OnlinePublication != "continue_automatic" && input.OnlinePublication != "pdf_only" {
		return bulletins.Workflow{}, bulletins.ErrInvalid
	}
	if input.OnlinePublication != "" && (input.Series != "general" || input.Locale != "zh-Hant") {
		return bulletins.Workflow{}, bulletins.ErrInvalid
	}
	if input.OnlinePublication == "pdf_only" && (input.OnlineVersion == nil || *input.OnlineVersion < 0) {
		return bulletins.Workflow{}, bulletins.ErrInvalid
	}
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return bulletins.Workflow{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return bulletins.Workflow{}, err
	}
	defer tx.Rollback()
	var canonical int64
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT i.version,v.auto_publish FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=$2 AND v.locale=$3 WHERE i.id=$1 FOR UPDATE OF i,v`, issue, input.Series, input.Locale).Scan(&canonical, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return bulletins.Workflow{}, bulletins.ErrNotFound
	}
	if err != nil {
		return bulletins.Workflow{}, err
	}
	if canonical != expected {
		return bulletins.Workflow{}, bulletins.ErrPrecondition
	}
	if enabled && input.OnlinePublication == "" {
		return bulletins.Workflow{}, bulletins.ErrAutomaticPublicationActive
	}
	if enabled && input.OnlinePublication == "continue_automatic" {
		if err = r.preflightEarlyBulletinPDF(ctx, tx, issue, input.Series, input.Locale); err != nil {
			return bulletins.Workflow{}, err
		}
	}
	var completed *bulletins.Workflow
	outcome := "absent"
	if enabled {
		outcome = "preserved"
	}
	if input.OnlinePublication == "pdf_only" {
		var online int64
		var doc string
		err = tx.QueryRowContext(ctx, `SELECT id::text,version FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series=$2 AND content_locale=$3 FOR UPDATE`, issue, input.Series, input.Locale).Scan(&doc, &online)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return bulletins.Workflow{}, err
		}
		if online != *input.OnlineVersion {
			return bulletins.Workflow{}, bulletins.ErrPrecondition
		}
		completed, err = completedPublishedBulletinPDF(ctx, tx, issue, input.Series, input.Locale)
		if err != nil {
			return bulletins.Workflow{}, err
		}
		// A completed publication needs only a deliberate plan change. Disabled
		// authority is already terminal/absent; do not invent another cancellation.
		if completed == nil || enabled {
			if err = r.setAutomaticBulletinIntentReason(ctx, tx, issue, input.Series, input.Locale, false, false, actor, now, "pdf_only"); err != nil {
				return bulletins.Workflow{}, err
			}
		}
		if completed != nil && enabled {
			if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, issue, actor, now); err != nil {
				return bulletins.Workflow{}, err
			}
			var editionID string
			if err = tx.QueryRowContext(ctx, `UPDATE hhc_web.bulletin_version SET version=version+1,updated_by=$4,updated_at=$5 WHERE issue_id=$1 AND series=$2 AND locale=$3 RETURNING id::text`, issue, input.Series, input.Locale, actor, now).Scan(&editionID); err != nil {
				return bulletins.Workflow{}, err
			}
			if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.version.update", editionID, actor, now); err != nil {
				return bulletins.Workflow{}, err
			}
		}
		if enabled {
			outcome = "cancelled"
		}
		if err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_issue WHERE id=$1`, issue).Scan(&canonical); err != nil {
			return bulletins.Workflow{}, err
		}
	}
	var workflow bulletins.Workflow
	if completed != nil {
		workflow = *completed
	} else {
		workflow, err = r.startBulletinPublishTx(ctx, tx, issue, input.Series, input.Locale, canonical, input.NotifySubscribers, actor, now)
		if err != nil {
			return workflow, err
		}
	}
	// Legacy response remains readable; known terminal history never implies an
	// active grant. The request itself cannot turn disabled authority on.
	if r.explicitOnlinePublication {
		disposition, err := loadBulletinDisposition(ctx, tx, issue, input.Series, input.Locale)
		if err != nil {
			return workflow, err
		}
		if disposition != nil && disposition.Status != "authorized" && disposition.Status != "running" && outcome != "cancelled" {
			outcome = "terminal"
		}
		workflow.OnlinePublication = &bulletins.OnlinePublicationOutcome{Outcome: outcome, Disposition: disposition}
	}
	return workflow, tx.Commit()
}

// Reuse only a real completed workflow which produced the current published PDF.
// The caller holds i/v/d and has checked both CAS values. No workflow/outbox lock
// is needed for terminal evidence; all publication writers serialize through i/v.
func completedPublishedBulletinPDF(ctx context.Context, tx *sql.Tx, issue, series, locale string) (*bulletins.Workflow, error) {
	var issueStatus, versionStatus string
	if err := tx.QueryRowContext(ctx, `SELECT i.status,v.status FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=$2 AND v.locale=$3 WHERE i.id=$1`, issue, series, locale).Scan(&issueStatus, &versionStatus); err != nil {
		return nil, err
	}
	if versionStatus != "published" {
		return nil, nil
	}
	if issueStatus == "publishing" || issueStatus == "unpublishing" {
		return nil, bulletins.ErrConflict
	}
	var workflow bulletins.Workflow
	err := tx.QueryRowContext(ctx, `SELECT w.id::text,w.status,w.aggregate_version,w.created_at
 FROM hhc_web.bulletin_version v JOIN hhc_web.bulletin_published_version p ON p.issue_id=v.issue_id AND p.series=v.series AND p.locale=v.locale
 JOIN hhc_web.publication_workflow w ON w.resource_id=p.issue_id AND w.resource_type='bulletin' AND w.workflow_type='bulletin_publish' AND w.series=p.series AND w.locale=p.locale AND w.asset_id=p.asset_id AND w.aggregate_version=p.version AND w.status='completed'
 WHERE v.issue_id=$1 AND v.series=$2 AND v.locale=$3 AND p.asset_id=v.pdf_asset_id AND p.title=v.title AND p.subtitle=v.subtitle`, issue, series, locale).Scan(&workflow.ID, &workflow.Status, &workflow.AggregateVersion, &workflow.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, bulletins.ErrConflict
	}
	if err != nil {
		return nil, err
	}
	return &workflow, nil
}
