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

func (r *Repository) ReserveTranslation(ctx context.Context, reservation translation.Reservation) error {
	if strings.TrimSpace(reservation.Actor) == "" || strings.TrimSpace(reservation.ResourceType) == "" || strings.TrimSpace(reservation.ResourceID) == "" || strings.TrimSpace(reservation.TargetLocale) == "" || reservation.SourceVersion <= 0 || reservation.ActorMinuteLimit <= 0 || reservation.DeploymentMinuteLimit <= 0 || reservation.ActorDailyLimit <= 0 || reservation.DeploymentDailyLimit <= 0 || reservation.Cooldown <= 0 {
		return errInvalidTranslationReservation
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := reservation.Now.UTC()
	minute := now.Truncate(time.Minute)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for _, counter := range []struct {
		scope      string
		window     time.Time
		limit      int
		retryAfter time.Duration
	}{
		{"deployment:minute", minute, reservation.DeploymentMinuteLimit, minute.Add(time.Minute).Sub(now)},
		{"actor:minute:" + reservation.Actor, minute, reservation.ActorMinuteLimit, minute.Add(time.Minute).Sub(now)},
		{"deployment:day", day, reservation.DeploymentDailyLimit, day.AddDate(0, 0, 1).Sub(now)},
		{"actor:day:" + reservation.Actor, day, reservation.ActorDailyLimit, day.AddDate(0, 0, 1).Sub(now)},
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO hhc_web.translation_rate_limit AS counters(scope,window_start,count)
			VALUES($1,$2,1)
			ON CONFLICT(scope,window_start) DO UPDATE SET count=counters.count+1
			RETURNING count`, counter.scope, counter.window).Scan(&count); err != nil {
			return err
		}
		if count > counter.limit {
			return &translation.RateLimitError{RetryAfter: counter.retryAfter}
		}
	}
	var reserved bool
	var nextAllowed time.Time
	if err := tx.QueryRowContext(ctx, `
		WITH attempted AS (
			INSERT INTO hhc_web.translation_cooldown AS cooldown(actor,resource_type,resource_id,source_version,target_locale,next_allowed_at,updated_at)
			VALUES($1,$2,$3,$4,$5,$7,$6)
			ON CONFLICT(actor,resource_type,resource_id,source_version,target_locale) DO UPDATE
			SET next_allowed_at=EXCLUDED.next_allowed_at, updated_at=EXCLUDED.updated_at
			WHERE cooldown.next_allowed_at <= $6
			RETURNING true AS reserved,next_allowed_at
		)
		SELECT reserved,next_allowed_at FROM attempted
		UNION ALL
		SELECT false,next_allowed_at FROM hhc_web.translation_cooldown
		WHERE actor=$1 AND resource_type=$2 AND resource_id=$3 AND source_version=$4 AND target_locale=$5
		  AND NOT EXISTS (SELECT 1 FROM attempted)
		LIMIT 1`, reservation.Actor, reservation.ResourceType, reservation.ResourceID, reservation.SourceVersion, reservation.TargetLocale, now, now.Add(reservation.Cooldown)).Scan(&reserved, &nextAllowed); err != nil {
		return err
	}
	if !reserved {
		return &translation.RateLimitError{RetryAfter: nextAllowed.Sub(now)}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.translation_rate_limit WHERE window_start < $1`, day.AddDate(0, 0, -2)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.translation_cooldown WHERE next_allowed_at < $1`, now.Add(-24*time.Hour)); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ReleaseTranslation(ctx context.Context, reservation translation.Reservation) error {
	if strings.TrimSpace(reservation.Actor) == "" || strings.TrimSpace(reservation.ResourceType) == "" || strings.TrimSpace(reservation.ResourceID) == "" || strings.TrimSpace(reservation.TargetLocale) == "" || reservation.SourceVersion <= 0 || reservation.Now.IsZero() || reservation.Cooldown <= 0 {
		return errInvalidTranslationReservation
	}
	now := reservation.Now.UTC()
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM hhc_web.translation_cooldown
		WHERE actor=$1 AND resource_type=$2 AND resource_id=$3 AND source_version=$4 AND target_locale=$5
		  AND updated_at=$6 AND next_allowed_at=$7`,
		reservation.Actor, reservation.ResourceType, reservation.ResourceID, reservation.SourceVersion, reservation.TargetLocale, now, now.Add(reservation.Cooldown))
	return err
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
