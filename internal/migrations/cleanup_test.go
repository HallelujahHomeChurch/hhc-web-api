package migrations

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/synctest"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrationDetachedUnlock(t *testing.T) {
	for _, kind := range []string{"canceled-parent", "blocked-unlock", "joined-errors", "successful-work"} {
		t.Run(kind, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				ctx := context.Background()
				var cancel context.CancelFunc
				if kind == "canceled-parent" {
					ctx, cancel = context.WithTimeout(ctx, time.Second)
					defer cancel()
				}
				primary := errors.New("schema unavailable")
				unlock := errors.New("unlock unavailable")
				mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
				create := mock.ExpectExec("CREATE TABLE IF NOT EXISTS public.schema_migrations")
				switch kind {
				case "canceled-parent":
					create.WillDelayFor(time.Hour).WillReturnResult(sqlmock.NewResult(0, 0))
				case "successful-work":
					create.WillReturnResult(sqlmock.NewResult(0, 0))
					entries, err := fs.Glob(files, "sql/*.sql")
					if err != nil {
						t.Fatal(err)
					}
					for _, name := range entries {
						mock.ExpectQuery("SELECT EXISTS").WithArgs(name).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
					}
				default:
					create.WillReturnError(primary)
				}
				cleanup := mock.ExpectExec("SELECT pg_advisory_unlock").WithArgs(sqlmock.AnyArg())
				if kind == "blocked-unlock" {
					cleanup.WillDelayFor(time.Hour).WillReturnResult(sqlmock.NewResult(0, 1))
				} else if kind == "canceled-parent" {
					cleanup.WillReturnResult(sqlmock.NewResult(0, 1))
				} else {
					cleanup.WillReturnError(unlock)
				}
				mock.ExpectClose()
				started := time.Now()
				err = Run(ctx, db)
				switch kind {
				case "canceled-parent":
					if !errors.Is(err, sqlmock.ErrCancelled) || time.Since(started) != time.Second {
						t.Errorf("parent error=%v,elapsed=%v", err, time.Since(started))
					}
				case "blocked-unlock":
					if !errors.Is(err, primary) || !errors.Is(err, sqlmock.ErrCancelled) {
						t.Errorf("lost primary/cleanup error: %v", err)
					}
					if got := time.Since(started); got != 10*time.Second {
						t.Errorf("unlock duration=%v,want10s", got)
					}
				case "joined-errors":
					if !errors.Is(err, primary) || !errors.Is(err, unlock) {
						t.Errorf("lost joined errors: %v", err)
					}
				case "successful-work":
					if !errors.Is(err, unlock) {
						t.Errorf("lost cleanup error: %v", err)
					}
				}
				if kind != "canceled-parent" && db.Stats().OpenConnections != 0 {
					t.Errorf("possibly locked connection returned to pool: %+v", db.Stats())
				}
				// Success unlock returns the borrowed Conn before the caller closes its pool.
				_ = db.Close()
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Error(err)
				}
			})
		})
	}
}

func TestMigrationAcquireErrorDiscardsSession(t *testing.T) {
	dsn := t.Name()
	db, mock, err := sqlmock.NewWithDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	primary := errors.New("lock acknowledgement failed")
	mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(sqlmock.AnyArg()).WillReturnError(primary)
	mock.ExpectClose()
	if err := Run(context.Background(), db); !errors.Is(err, primary) {
		t.Errorf("error=%v", err)
	}
	if db.Stats().OpenConnections != 0 {
		t.Errorf("uncertain session reused: %+v", db.Stats())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	// Register a replacement fake session to verify the caller-owned pool reconnects.
	replacement, next, err := sqlmock.NewWithDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	next.ExpectClose()
	next.ExpectClose()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw(func(raw any) error {
		if raw == mock || raw != next {
			return errors.New("borrow reused uncertain session")
		}
		return nil
	}); err != nil {
		t.Error(err)
	}
	_ = conn.Close()
	_ = db.Close()
	_ = replacement.Close()
	if err := next.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

func TestMigrationLockContentionHonorsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(sqlmock.AnyArg()).
			WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(false))
		if err := Run(ctx, db); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("lock wait error = %v", err)
		}
		if db.Stats().OpenConnections != 1 {
			t.Fatal("known unlocked session was discarded")
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}
