package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func enqueueOnlineBulletinWake(ctx context.Context, tx *sql.Tx, jobID string, now time.Time) error {
	id := platform.NewID()
	_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1::uuid,'bulletin-extraction-queue','bulletin.online.wake','bulletin_work',$2,1,'{"version":1}',$1::text,'pending',$3,$3,$3)`, id, jobID, now)
	return err
}

// Include delayed retries and live leases: an empty claim must not discard the
// last wake while a crashed execution's database lease is still valid.
func (r *Repository) NextOnlineBulletinWork(ctx context.Context) (time.Time, error) {
	var next sql.NullTime
	query := `SELECT min(ready_at) FROM (
 SELECT CASE WHEN status='queued' THEN available_at ELSE lease_until END AS ready_at FROM hhc_web.online_bulletin_extraction_job WHERE NOT superseded AND status IN ('queued','parsing')
 UNION ALL SELECT CASE WHEN status='queued' THEN available_at ELSE lease_until END FROM hhc_web.online_bulletin_layout_job WHERE status IN ('queued','validating')
 UNION ALL SELECT CASE WHEN status='queued' THEN available_at ELSE lease_until END FROM hhc_web.online_bulletin_conversion_job WHERE status IN ('queued','converting')
 ) pending`
	args := []any{}
	if r.llmBulletinExtraction {
		query = `SELECT min(ready_at) FROM (` + query + ` UNION ALL SELECT now()` + recognitionReleaseFrom + `) all_work(ready_at)`
		args = append(args, onlinebulletins.RecognitionExtractorVersion())
	}
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&next)
	return next.Time, err
}
