package postgres

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
)

func accessDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = migrations.Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.outbox_event; DELETE FROM hhc_web.public_projection WHERE resource_type IN ('bulletin_issue','bulletin_latest')`); err != nil {
		t.Fatal(err)
	}
	return db
}
