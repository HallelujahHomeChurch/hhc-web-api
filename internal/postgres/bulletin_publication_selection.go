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
		if err = r.setAutomaticBulletinIntentReason(ctx, tx, issue, input.Series, input.Locale, false, false, actor, now, "pdf_only"); err != nil {
			return bulletins.Workflow{}, err
		}
		if enabled {
			outcome = "cancelled"
		}
		if err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_issue WHERE id=$1`, issue).Scan(&canonical); err != nil {
			return bulletins.Workflow{}, err
		}
	}
	workflow, err := r.startBulletinPublishTx(ctx, tx, issue, input.Series, input.Locale, canonical, input.NotifySubscribers, actor, now)
	if err != nil {
		return workflow, err
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
