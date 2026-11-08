package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestAuditOutboxReusesCanonicalEvent(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err = migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `TRUNCATE hhc_web.outbox_event CASCADE`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	event, err := auditclient.NewEvent("cms.bulletin.create", "018f47a1-0f8a-4f25-8f09-111111111111", "018f47a1-0f8a-4f25-8f09-222222222222", "request-1", now, auditclient.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	outbox := NewAuditOutbox(db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = outbox.EnqueueTx(ctx, tx, event, now); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = outbox.Enqueue(ctx, event, now); err != nil {
		t.Fatal(err)
	}
	var count int
	var destination, eventType string
	if err = db.QueryRowContext(ctx, `SELECT count(*),min(destination),min(event_type) FROM hhc_web.outbox_event WHERE idempotency_key=$1`, event.EventID).Scan(&count, &destination, &eventType); err != nil || count != 1 || destination != "audit-log" || eventType != "audit.append" {
		t.Fatalf("count=%d destination=%s type=%s err=%v", count, destination, eventType, err)
	}
	stats, err := outbox.Stats(ctx, now)
	if err != nil || stats.PendingCount != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}
