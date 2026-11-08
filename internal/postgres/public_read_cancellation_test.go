package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

func TestPublicReadCancelsPoolAndSQLWaitAndRecovers(t *testing.T) {
	db := openBulletinAuditDB(t)
	db.SetMaxOpenConns(2)
	for _, phase := range []string{"pool", "query"} {
		t.Run(phase, func(t *testing.T) {
			control, cancelControl := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancelControl()
			tx, err := db.BeginTx(control, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if phase == "pool" {
				db.SetMaxOpenConns(1)
				defer db.SetMaxOpenConns(2)
			} else {
				// A blocked projection read must release its connection on deadline,
				// even while another session retains the table lock.
				if _, err := tx.ExecContext(control, "LOCK TABLE hhc_web.public_projection IN ACCESS EXCLUSIVE MODE"); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(control, 100*time.Millisecond)
			started := time.Now()
			_, err = New(db).PublicContent(ctx, content.ModuleNews, "en", 1, 10)
			cause := ctx.Err()
			cancel()
			if err == nil || cause != context.DeadlineExceeded || time.Since(started) > 2*time.Second {
				t.Fatalf("read did not honor deadline: error=%v context=%v", err, cause)
			}
			if phase == "pool" && db.Stats().WaitCount == 0 {
				t.Fatal("expected pool checkout wait")
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if _, err := New(db).PublicContent(control, content.ModuleNews, "en", 1, 10); err != nil {
				t.Fatalf("pool did not recover: %v", err)
			}
		})
	}
}
