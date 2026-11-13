package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

// Serialize on the canonical issue before locking the event and workflow, as
// cancellation does. A late worker cannot rewrite a terminal result or take a
// newer claim's work.
func lockBulletinPublicationClaim(ctx context.Context, tx *sql.Tx, event publication.Event, now time.Time) (bool, error) {
	var ignored string
	if err := tx.QueryRowContext(ctx, `SELECT id::text FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, event.AggregateID).Scan(&ignored); err != nil {
		return false, err
	}
	var current bool
	err := tx.QueryRowContext(ctx, `SELECT e.status='processing' AND e.attempts=$2 AND e.claimed_until=$3 AND e.claimed_until>$4 AND e.aggregate_id=$5 AND e.aggregate_version=$6 AND e.payload_json=$7::jsonb AND w.status NOT IN ('completed','failed','cancelled')
 FROM hhc_web.outbox_event e JOIN hhc_web.publication_workflow w ON w.id::text=e.payload_json->>'workflowId'
 WHERE e.id=$1 AND e.event_type='bulletin.publish.ensure_asset' FOR UPDATE OF e,w`, event.ID, event.Attempts, event.ClaimedUntil, now, event.AggregateID, event.AggregateVersion, event.Payload).Scan(&current)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return current, err
}

// RescheduleBulletinPublication ignores obsolete claims, including the attempts
// ABA caused by readiness deferral; claimed_until must also match exactly.
func (r *Repository) RescheduleBulletinPublication(ctx context.Context, event publication.Event, detail string, next, now time.Time, waiting bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := lockBulletinPublicationClaim(ctx, tx, event, now)
	if err != nil || !current {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='pending',attempts=CASE WHEN $5 THEN GREATEST(attempts-1,0) ELSE attempts END,next_attempt_at=$2,claimed_until=NULL,last_error=$3,updated_at=$4 WHERE id=$1`, event.ID, next, detail, now, waiting)
	if err != nil {
		return err
	}
	return tx.Commit()
}
