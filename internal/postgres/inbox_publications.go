package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"time"
)

func (r *Repository) WithInboxPublications(after time.Time) *Repository {
	r.inboxPublicationsAfter = after
	return r
}
func (r *RecordingRepository) WithInboxPublications(after time.Time) *RecordingRepository {
	r.inboxPublicationsAfter = after
	return r
}

func recordInboxPublication(ctx context.Context, tx *sql.Tx, after time.Time, kind, id, series, locale string, version int64, now time.Time) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.inbox_publication_source(kind,resource_id,series,locale,first_visible_at) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, kind, id, series, locale, now)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n == 0 || after.IsZero() || now.Before(after) {
		return err
	}
	event := publication.InboxPublication{EventID: platform.NewID(), Kind: kind, ResourceID: id, Series: series, Locale: locale, SourceVersion: version, OccurredAt: now.UTC()}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1::uuid,'engagement-inbox','inbox.publication','inbox_publication',$2,$3,$4,$1,'pending',$5,$5,$5)`, event.EventID, id, version, raw, now)
	return err
}
func (r *Repository) ClaimInboxPublication(ctx context.Context, now time.Time, lease time.Duration) (publication.Event, bool, error) {
	var e publication.Event
	err := r.db.QueryRowContext(ctx, `WITH candidate AS (SELECT id FROM hhc_web.outbox_event WHERE destination='engagement-inbox' AND event_type='inbox.publication' AND ((status='pending' AND next_attempt_at<=$1) OR (status='processing' AND claimed_until<=$1)) ORDER BY next_attempt_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE hhc_web.outbox_event e SET status='processing',attempts=e.attempts+1,claimed_until=$2,updated_at=$1 FROM candidate WHERE e.id=candidate.id RETURNING e.id::text,e.payload_json,e.claimed_until,e.attempts`, now, now.Add(lease)).Scan(&e.ID, &e.Payload, &e.ClaimedUntil, &e.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	return e, err == nil, err
}
func (r *Repository) FinishInboxPublication(ctx context.Context, e publication.Event, delivered bool, now time.Time) error {
	status, detail := "delivered", ""
	if !delivered {
		status, detail = "pending", "engagement_inbox_unavailable"
	}
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status=$3,claimed_until=NULL,next_attempt_at=$4,last_error=$5,updated_at=$6 WHERE id=$1 AND claimed_until=$2 AND destination='engagement-inbox' AND status='processing'`, e.ID, e.ClaimedUntil, status, now.Add(time.Minute), detail, now)
	return err
}
