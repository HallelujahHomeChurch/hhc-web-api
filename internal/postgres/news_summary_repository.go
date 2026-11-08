package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
)

func newsSourceHash(locale, title, body string) string {
	encoded, _ := json.Marshal([]string{locale, title, body})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

// The caller holds the content entry lock; all state and outbox changes are
// committed with the draft. Entry versions fence A -> B -> A and locale re-adds.
func prepareNewsSummaries(ctx context.Context, tx *sql.Tx, id string, version int64, values []content.Translation, actor string, now time.Time, retryFailed bool) error {
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM hhc_web.news_item WHERE entry_id=$1`, id).Scan(&kind); err != nil {
		return err
	}
	if kind == content.KindStatement {
		_, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.news_summary_state WHERE entry_id=$1`, id)
		return err
	}
	locales := make([]string, 0, len(values))
	for i := range values {
		value := &values[i]
		locales = append(locales, value.Locale)
		var summary string
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT summary FROM hhc_web.content_translation WHERE entry_id=$1 AND locale=$2),'')`, id, value.Locale).Scan(&summary); err != nil {
			return err
		}
		value.Summary = summary
		hash := newsSourceHash(value.Locale, value.Title, value.Body)
		var oldHash, status string
		var sourceVersion int64
		err := tx.QueryRowContext(ctx, `SELECT source_hash,source_version,status FROM hhc_web.news_summary_state WHERE entry_id=$1 AND locale=$2`, id, value.Locale).Scan(&oldHash, &sourceVersion, &status)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if oldHash == hash && (status != "failed" || !retryFailed) {
			continue
		}
		if oldHash != hash {
			sourceVersion = version
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.news_summary_state(entry_id,locale,source_hash,source_version,status) VALUES($1,$2,$3,$4,'pending') ON CONFLICT(entry_id,locale) DO UPDATE SET source_hash=EXCLUDED.source_hash,source_version=EXCLUDED.source_version,status='pending',error_code=''`, id, value.Locale, hash, sourceVersion); err != nil {
			return err
		}
		payload, _ := json.Marshal(newsSummaryPayload{Locale: value.Locale, SourceHash: hash, SourceVersion: sourceVersion, Actor: actor})
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'hhc-web-api','news.summary.generate','news',$2,$3,$4,$5,'pending',$6,$6,$6) ON CONFLICT(destination,idempotency_key) DO UPDATE SET status='pending',claimed_until=NULL,last_error='',next_attempt_at=EXCLUDED.next_attempt_at,updated_at=EXCLUDED.updated_at,payload_json=EXCLUDED.payload_json WHERE outbox_event.status='failed'`, platform.NewID(), id, sourceVersion, payload, fmt.Sprintf("news-summary:%s:%s:%d", id, value.Locale, sourceVersion), now); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.news_summary_state WHERE entry_id=$1 AND NOT(locale=ANY($2::text[]))`, id, locales)
	return err
}

type newsSummaryPayload struct {
	Locale        string `json:"locale"`
	SourceHash    string `json:"sourceHash"`
	SourceVersion int64  `json:"sourceVersion"`
	Actor         string `json:"actor"`
}

func (r *Repository) LoadNewsSummary(ctx context.Context, event publication.Event) (translation.SummaryRequest, bool, error) {
	var payload newsSummaryPayload
	if json.Unmarshal(event.Payload, &payload) != nil {
		return translation.SummaryRequest{}, false, translation.ErrInvalidRequest
	}
	request := translation.SummaryRequest{ResourceID: event.AggregateID, Locale: payload.Locale, Actor: payload.Actor, SourceVersion: payload.SourceVersion}
	err := r.db.QueryRowContext(ctx, `SELECT t.title,t.body FROM hhc_web.news_summary_state s JOIN hhc_web.content_translation t ON t.entry_id=s.entry_id AND t.locale=s.locale JOIN hhc_web.outbox_event e ON e.id=$5 WHERE s.entry_id=$1 AND s.locale=$2 AND s.source_hash=$3 AND s.source_version=$4 AND s.status='pending' AND e.status='processing' AND e.attempts=$6`, event.AggregateID, payload.Locale, payload.SourceHash, payload.SourceVersion, event.ID, event.Attempts).Scan(&request.Title, &request.Body)
	if err == sql.ErrNoRows {
		return request, false, nil
	}
	return request, err == nil, err
}

// Finish/retry/fail are fenced by the monotonic claim attempt and lease. Lock
// entry before event to match save's lock order. Derived writes do not touch the
// user's version, author, timestamps, revisions or public projections.
func (r *Repository) FinishNewsSummary(ctx context.Context, event publication.Event, summary, errorCode string, next, now time.Time) error {
	var payload newsSummaryPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `SELECT id FROM hhc_web.content_entry WHERE id=$1 FOR UPDATE`, event.AggregateID); err != nil {
		return err
	}
	status := "delivered"
	if errorCode != "" {
		status = "failed"
	}
	if !next.IsZero() {
		status = "pending"
	}
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status=$3,claimed_until=NULL,last_error=$4,next_attempt_at=$5,updated_at=$6,payload_json=CASE WHEN $4<>'' THEN jsonb_set(payload_json,'{summaryFailures}',to_jsonb(COALESCE((payload_json->>'summaryFailures')::integer,0)+1)) ELSE payload_json END WHERE id=$1 AND attempts=$2 AND status='processing' AND claimed_until>$6`, event.ID, event.Attempts, status, errorCode, next, now)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return err
	}
	if status == "pending" {
		return tx.Commit()
	}
	desired := "ready"
	if errorCode != "" {
		desired = "failed"
	}
	// An empty successful result only completes an obsolete event.
	if summary != "" || errorCode != "" {
		result, err = tx.ExecContext(ctx, `UPDATE hhc_web.news_summary_state SET status=$5,error_code=$6 WHERE entry_id=$1 AND locale=$2 AND source_hash=$3 AND source_version=$4 AND status='pending'`, event.AggregateID, payload.Locale, payload.SourceHash, payload.SourceVersion, desired, errorCode)
		if err != nil {
			return err
		}
		rows, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if rows > 0 && desired == "ready" {
			if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.content_translation SET summary=$3 WHERE entry_id=$1 AND locale=$2`, event.AggregateID, payload.Locale, summary); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
