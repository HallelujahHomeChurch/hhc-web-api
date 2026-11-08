package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestRecordingTransactionCleanupOnFailureAndPanic(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	repo.db.SetMaxOpenConns(2)
	t.Run("callback panic", func(t *testing.T) {
		func() {
			defer func() {
				if recover() == nil {
					t.Error("callback panic disappeared")
				}
			}()
			repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(context.Context, *recordings.Broadcast, *recordings.Recording, *recordings.Capture) error {
				panic("synthetic callback")
			})
		}()
		tx, finish, err := repo.beginRecordingTransaction(ctx)
		if err != nil {
			t.Fatalf("panic leaked permit: %v", err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatal(err)
		}
		finish()
		finish()
	})
	t.Run("cancelled advisory wait", func(t *testing.T) {
		// Separate repository/pool ownership is not needed for the blocker: reserve
		// one raw test connection, then exercise the admitted source transaction.
		blocker, err := repo.db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback()
		if _, err = blocker.Exec(`SELECT pg_advisory_xact_lock(hashtextextended('recording-capture-single-input',0))`); err != nil {
			t.Fatal(err)
		}
		short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		_, _, err = repo.sourceTransaction(short)
		cancel()
		if err == nil {
			t.Fatal("blocked source did not cancel")
		}
		blocker.Rollback()
		tx, finish, err := repo.beginRecordingTransaction(ctx)
		if err != nil {
			t.Fatalf("cancel leaked permit: %v", err)
		}
		tx.Rollback()
		finish()
	})
	t.Run("commit failure", func(t *testing.T) {
		tx, finish, err := repo.beginRecordingTransaction(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer finish()
		if _, err = tx.Exec(`CREATE TEMP TABLE recording_budget_commit_test(id integer UNIQUE DEFERRABLE INITIALLY DEFERRED) ON COMMIT DROP; INSERT INTO recording_budget_commit_test VALUES(1),(1)`); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(); err == nil {
			t.Fatal("expected deferred constraint commit failure")
		}
		finish()
		_, next, err := repo.beginRecordingTransaction(ctx)
		if err != nil {
			t.Fatalf("commit failure leaked permit: %v", err)
		}
		next()
	})
	t.Run("cancelled before admission", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		_, _, err := repo.beginRecordingTransaction(cancelled)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation changed: %v", err)
		}
	})
	repo.transactionMu.Lock()
	active := repo.transactions
	repo.transactionMu.Unlock()
	if active != 0 {
		t.Fatalf("leaked recording permits: %d", active)
	}
}

// A driver panic is fatal to that SQL operation; the admission layer must still
// release ownership while propagating the panic. This is not driver recovery.
func TestRecordingTransactionDriverPanicReleasesAdmission(t *testing.T) {
	for _, phase := range []string{"commit", "rollback"} {
		t.Run(phase, func(t *testing.T) {
			db := sql.OpenDB(budgetPanicConnector{phase})
			defer db.Close()
			db.SetMaxOpenConns(2)
			repo := NewRecordingRepository(db)
			func() {
				defer func() {
					if recover() == nil {
						t.Error("driver panic disappeared")
					}
				}()
				tx, finish, err := repo.beginRecordingTransaction(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer finish()
				if phase == "commit" {
					tx.Commit()
				}
			}()
			if repo.transactions != 0 {
				t.Fatalf("driver panic leaked admission: %d", repo.transactions)
			}
		})
	}
}

type budgetPanicConnector struct{ phase string }

func (c budgetPanicConnector) Connect(context.Context) (driver.Conn, error) {
	return budgetPanicConn{c.phase}, nil
}
func (c budgetPanicConnector) Driver() driver.Driver { return budgetPanicDriver{c.phase} }

type budgetPanicDriver struct{ phase string }

func (d budgetPanicDriver) Open(string) (driver.Conn, error) { return budgetPanicConn{d.phase}, nil }

type budgetPanicConn struct{ phase string }

func (c budgetPanicConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (c budgetPanicConn) Close() error                        { return nil }
func (c budgetPanicConn) Begin() (driver.Tx, error)           { return budgetPanicTx{c.phase}, nil }

type budgetPanicTx struct{ phase string }

func (t budgetPanicTx) Commit() error {
	if t.phase == "commit" {
		panic("synthetic commit")
	}
	return nil
}
func (t budgetPanicTx) Rollback() error {
	if t.phase == "rollback" {
		panic("synthetic rollback")
	}
	return nil
}
