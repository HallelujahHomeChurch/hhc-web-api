package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func (r *Repository) BulletinTemplateSettings(ctx context.Context) (onlinebulletins.TemplateSettings, error) {
	return loadBulletinTemplateSettings(ctx, r.db)
}

func loadBulletinTemplateSettings(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (onlinebulletins.TemplateSettings, error) {
	var value onlinebulletins.TemplateSettings
	var raw []byte
	err := query.QueryRowContext(ctx, `SELECT version,text_json FROM hhc_web.bulletin_template_settings WHERE id=true`).Scan(&value.Version, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return value, bulletins.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	if json.Unmarshal(raw, &value.TemplateText) != nil || value.Version < 1 {
		return value, bulletins.ErrInvalid
	}
	if _, ok := onlinebulletins.NormalizeTemplateText(value.TemplateText); !ok {
		return value, bulletins.ErrInvalid
	}
	return value, nil
}

func (r *Repository) SaveBulletinTemplateSettings(ctx context.Context, input onlinebulletins.TemplateText, expected int64, actor string, now time.Time) (onlinebulletins.TemplateSettings, error) {
	var result onlinebulletins.TemplateSettings
	input, ok := onlinebulletins.NormalizeTemplateText(input)
	if !ok || expected < 1 || actor == "" || now.IsZero() {
		return result, bulletins.ErrInvalid
	}
	if r.auditOutbox == nil {
		return result, bulletins.ErrAuditUnavailable
	}
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return result, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_template_settings WHERE id=true FOR UPDATE`).Scan(&current)
	if err != nil {
		return result, err
	}
	if current != expected {
		return result, bulletins.ErrPrecondition
	}
	result = onlinebulletins.TemplateSettings{Version: current + 1, TemplateText: input}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_template_settings SET version=$1,text_json=$2 WHERE id=true`, result.Version, mustJSON(input)); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_template_settings_revision(version,text_json,created_by,created_at) VALUES($1,$2,$3,$4)`, result.Version, mustJSON(input), actor, now); err != nil {
		return result, err
	}
	// This is a private component of the existing church site-settings singleton.
	if err = r.enqueueBulletinAudit(ctx, tx, "cms.site_settings.update", "singleton", actor, now); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (r *Repository) BulletinTemplateSettingsHistory(ctx context.Context) ([]onlinebulletins.TemplateSettingsRevision, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT version,text_json,created_by,created_at FROM hhc_web.bulletin_template_settings_revision ORDER BY version DESC LIMIT 50`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []onlinebulletins.TemplateSettingsRevision{}
	for rows.Next() {
		var value onlinebulletins.TemplateSettingsRevision
		var raw []byte
		var created time.Time
		if err = rows.Scan(&value.Version, &raw, &value.CreatedBy, &created); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &value.TemplateText); err != nil {
			return nil, err
		}
		value.CreatedAt = created.UTC().Format(time.RFC3339Nano)
		values = append(values, value)
	}
	return values, rows.Err()
}
