package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func enqueueConfirmedLearning(ctx context.Context, tx *sql.Tx, confirmed onlinebulletins.ConfirmedRevision, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'hhc-web-api','bulletin.online.learn','online_bulletin',$2,$3,'{}',$4,'pending',$5,$5,$5)`, platform.NewID(), confirmed.DocumentID, confirmed.Revision, fmt.Sprintf("online-bulletin:%s:learn:%d", confirmed.DocumentID, confirmed.Revision), now)
	return err
}

// The event refers only to immutable confirmation/revision evidence. A later
// Local, source upload or canonical edit cannot replace the reviewed input.
func (r *Repository) CompleteOnlineBulletinLearning(ctx context.Context, event publication.Event, now time.Time) error {
	if event.EventType != "bulletin.online.learn" || event.AggregateVersion < 1 || now.IsZero() {
		return bulletins.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stored []byte
	var status string
	var lease sql.NullTime
	var attempts int
	var revision int64
	err = tx.QueryRowContext(ctx, `SELECT payload_json,status,claimed_until,attempts,aggregate_version FROM hhc_web.outbox_event WHERE id=$1 AND destination='hhc-web-api' AND event_type='bulletin.online.learn' AND aggregate_type='online_bulletin' AND aggregate_id=$2 FOR UPDATE`, event.ID, event.AggregateID).Scan(&stored, &status, &lease, &attempts, &revision)
	if err != nil {
		return err
	}
	if !bytes.Equal(stored, event.Payload) || revision != event.AggregateVersion {
		return bulletins.ErrConflict
	}
	if status == "delivered" {
		return nil
	}
	if status != "processing" || !lease.Valid || !lease.Time.After(now) || !lease.Time.Equal(event.ClaimedUntil) || attempts != event.Attempts {
		return publication.ErrStalePublication
	}
	// Serialize revisions of one document, then lock shared signatures in order.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1740))`, event.AggregateID); err != nil {
		return err
	}
	var documentJSON, canonicalJSON []byte
	var actor string
	var confirmedAt time.Time
	err = tx.QueryRowContext(ctx, `SELECT r.document_json,c.canonical_metadata,c.created_by,c.created_at FROM hhc_web.online_bulletin_confirmation c JOIN hhc_web.online_bulletin_revision r USING(document_id,revision) WHERE c.document_id=$1 AND c.revision=$2`, event.AggregateID, revision).Scan(&documentJSON, &canonicalJSON, &actor, &confirmedAt)
	if err != nil {
		return err
	}
	var document onlinebulletins.Document
	var canonical onlinebulletins.CanonicalMetadata
	if json.Unmarshal(documentJSON, &document) != nil || json.Unmarshal(canonicalJSON, &canonical) != nil {
		return bulletins.ErrConflict
	}
	confirmed := onlinebulletins.ConfirmedRevision{DocumentID: event.AggregateID, Revision: revision}
	if err = captureConfirmedBodyKinds(ctx, tx, confirmed, document, canonical, actor, confirmedAt); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RescheduleOnlineBulletinLearning(ctx context.Context, event publication.Event, next, now time.Time) error {
	status := "pending"
	if next.IsZero() {
		status, next = "failed", now
	}
	if event.EventType != "bulletin.online.learn" || now.IsZero() || status == "pending" && !next.After(now) {
		return bulletins.ErrInvalid
	}
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status=$2,next_attempt_at=$3,claimed_until=NULL,last_error='learning_evaluation_failed',updated_at=$4 WHERE id=$1 AND event_type='bulletin.online.learn' AND status='processing' AND aggregate_id=$5 AND aggregate_version=$6 AND attempts=$7 AND claimed_until=$8 AND claimed_until>$4`, event.ID, status, next, now, event.AggregateID, event.AggregateVersion, event.Attempts, event.ClaimedUntil)
	return err
}
