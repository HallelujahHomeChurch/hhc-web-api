package postgres

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func TestOnlineBulletinClaimCancellationReturnsConnection(t *testing.T) {
	observer := accessDB(t)
	for _, kind := range []string{"layout", "conversion", "extraction"} {
		t.Run(kind, func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			dsn, err := url.Parse(os.Getenv("HHW_TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			name := "extractor-cancel-" + platform.NewID()
			query := dsn.Query()
			query.Set("application_name", name)
			dsn.RawQuery = query.Encode()
			db, err := sql.Open("pgx", dsn.String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			db.SetMaxOpenConns(2)
			db.SetMaxIdleConns(1)
			lock, err := observer.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback()
			// Block even an empty queue's initial attempt-limit UPDATE.
			if _, err = lock.ExecContext(ctx, "LOCK TABLE hhc_web.online_bulletin_"+kind+"_job IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			claimCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan struct{})
			var claimErr error
			go func() {
				defer close(done)
				repo := New(db)
				switch kind {
				case "layout":
					_, claimErr = repo.ClaimOnlineBulletinLayout(claimCtx, time.Now().UTC(), time.Minute, 3)
				case "conversion":
					_, claimErr = repo.ClaimOnlineBulletinConversion(claimCtx, time.Now().UTC(), time.Minute, 3)
				case "extraction":
					_, claimErr = repo.ClaimOnlineBulletinExtraction(claimCtx, time.Now().UTC(), time.Minute, 3)
				}
			}()
			defer func() {
				cancel()
				lock.Rollback()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("claim goroutine did not stop after cancellation and lock release")
				}
			}()
			wait := func(description string, check func() (bool, error)) {
				t.Helper()
				ticker := time.NewTicker(5 * time.Millisecond)
				defer ticker.Stop()
				for {
					ok, err := check()
					if err != nil {
						t.Fatal(err)
					}
					if ok {
						return
					}
					select {
					case <-ctx.Done():
						t.Fatalf("timed out waiting for %s", description)
					case <-ticker.C:
					}
				}
			}
			wait("blocked repository claim", func() (bool, error) {
				var blocked bool
				err := observer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event_type='Lock' AND query LIKE 'UPDATE hhc_web.online_bulletin_%')`, name).Scan(&blocked)
				return blocked, err
			})
			cancel()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("blocked claim ignored cancellation")
			}
			if !errors.Is(claimErr, context.Canceled) {
				t.Fatalf("claim error=%v, want canceled", claimErr)
			}
			wait("pool connection return", func() (bool, error) {
				return db.Stats().InUse == 0, nil
			})
			if err = db.PingContext(ctx); err != nil {
				t.Fatalf("pool unusable after canceled claim: %v", err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			if stats := db.Stats(); stats.InUse != 0 || stats.OpenConnections != 0 {
				t.Fatalf("pool retained connections after Close: %+v", stats)
			}
			wait("closed worker backends", func() (bool, error) {
				var count int
				err := observer.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE application_name=$1`, name).Scan(&count)
				return count == 0, err
			})
			t.Log("observed blocked claim, cancellation, returned connection, reusable pool, Close and zero named backends")
		})
	}
}
