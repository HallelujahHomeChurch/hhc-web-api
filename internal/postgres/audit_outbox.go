package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

var ErrAuditConflict = errors.New("audit event payload conflict")

type AuditOutbox struct{ db *sql.DB }

func NewAuditOutbox(db *sql.DB) *AuditOutbox { return &AuditOutbox{db: db} }
func (s *AuditOutbox) Enqueue(ctx context.Context, event auditclient.Event, now time.Time) (publication.Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return publication.Event{}, err
	}
	defer tx.Rollback()
	queued, err := s.EnqueueTx(ctx, tx, event, now)
	if err != nil {
		return publication.Event{}, err
	}
	return queued, tx.Commit()
}
func (s *AuditOutbox) EnqueueTx(ctx context.Context, tx *sql.Tx, event auditclient.Event, now time.Time) (publication.Event, error) {
	if tx == nil {
		return publication.Event{}, errors.New("audit transaction is required")
	}
	payload, _, err := event.Canonical()
	if err != nil {
		return publication.Event{}, err
	}
	id := platform.NewID()
	row, err := scanAuditOutbox(tx.QueryRowContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'audit-log','audit.append','audit_event',$1,0,$2,$3,'pending',$4,$4,$4) ON CONFLICT(destination,idempotency_key) DO NOTHING RETURNING id::text,event_type,aggregate_type,aggregate_id::text,aggregate_version,payload_json,attempts,created_at`, id, payload, event.EventID, now))
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return publication.Event{}, err
	}
	row, err = scanAuditOutbox(tx.QueryRowContext(ctx, `SELECT id::text,event_type,aggregate_type,aggregate_id::text,aggregate_version,payload_json,attempts,created_at FROM hhc_web.outbox_event WHERE destination='audit-log' AND idempotency_key=$1`, event.EventID))
	if err != nil {
		return publication.Event{}, err
	}
	stored, err := auditclient.Decode(row.Payload)
	if err != nil || row.ID != row.AggregateID || row.EventType != "audit.append" || row.AggregateVersion != 0 || stored.EventID != event.EventID {
		return publication.Event{}, ErrAuditConflict
	}
	if !auditclient.EquivalentForEnqueue(stored, event) {
		return publication.Event{}, ErrAuditConflict
	}
	return row, nil
}
func scanAuditOutbox(row scanner) (publication.Event, error) {
	var event publication.Event
	var aggregateType string
	err := row.Scan(&event.ID, &event.EventType, &aggregateType, &event.AggregateID, &event.AggregateVersion, &event.Payload, &event.Attempts, &event.CreatedAt)
	if err == nil && aggregateType != "audit_event" {
		return publication.Event{}, ErrAuditConflict
	}
	return event, err
}
func (s *AuditOutbox) Stats(ctx context.Context, now time.Time) (auditclient.PipelineStats, error) {
	var stats auditclient.PipelineStats
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE status='pending' OR (status='processing' AND claimed_until <= $1)),COALESCE(GREATEST(EXTRACT(EPOCH FROM ($1-min(created_at) FILTER (WHERE status='pending' OR (status='processing' AND claimed_until <= $1)))),0),0),count(*) FILTER (WHERE status='failed') FROM hhc_web.outbox_event WHERE destination='audit-log'`, now).Scan(&stats.PendingCount, &stats.OldestPendingSeconds, &stats.DeadLetterCount)
	return stats, err
}
