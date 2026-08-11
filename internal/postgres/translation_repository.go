package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
)

var errInvalidTranslationReservation = errors.New("invalid translation reservation")

func (r *Repository) ReserveTranslation(ctx context.Context, actor string, now time.Time, actorLimit, deploymentLimit int) error {
	if strings.TrimSpace(actor) == "" || actorLimit <= 0 || deploymentLimit <= 0 {
		return errInvalidTranslationReservation
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	window := now.UTC().Truncate(time.Minute)
	for _, counter := range []struct {
		scope string
		limit int
	}{{"deployment", deploymentLimit}, {"actor:" + actor, actorLimit}} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO hhc_web.translation_rate_limit AS counters(scope,window_start,count)
			VALUES($1,$2,1)
			ON CONFLICT(scope,window_start) DO UPDATE SET count=counters.count+1
			RETURNING count`, counter.scope, window).Scan(&count); err != nil {
			return err
		}
		if count > counter.limit {
			return translation.ErrRateLimited
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.translation_rate_limit WHERE window_start < $1`, window.Add(-time.Hour)); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RecordTranslationAudit(ctx context.Context, event translation.AuditEvent) error {
	payload, err := json.Marshal(struct {
		SourceVersion  int64  `json:"sourceVersion"`
		SourceLocale   string `json:"sourceLocale"`
		TargetLocale   string `json:"targetLocale"`
		Provider       string `json:"provider"`
		Deployment     string `json:"deployment"`
		PromptVersion  string `json:"promptVersion"`
		CharacterCount int    `json:"characterCount"`
		DurationMS     int64  `json:"durationMs"`
		Outcome        string `json:"outcome"`
	}{
		SourceVersion: event.SourceVersion, SourceLocale: event.SourceLocale, TargetLocale: event.TargetLocale,
		Provider: event.Provider, Deployment: event.Deployment, PromptVersion: event.PromptVersion,
		CharacterCount: event.CharacterCount, DurationMS: event.Duration.Milliseconds(), Outcome: event.Outcome,
	})
	if err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO hhc_web.cms_audit_event(id,action,resource_type,resource_id,actor,payload_json,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, platform.NewID(), event.Action, event.ResourceType, event.ResourceID, event.Actor, payload, event.CreatedAt)
	return err
}
