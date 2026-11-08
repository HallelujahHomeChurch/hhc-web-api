package postgres

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// Readiness no longer pings SQL; preserve the capacity tests' reserved-connection assertion explicitly.
func assertDatabaseProbeAvailable(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("reserved database capacity unavailable: %v", err)
	}
}
