package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"testing"
	"time"
)

func TestSubjectAdmissionBoundedQueueBeforeCheckout(t *testing.T) {
	db := sql.OpenDB(budgetPanicConnector{})
	defer db.Close()
	db.SetMaxOpenConns(10)
	repo := NewRecordingRepository(db)
	var held []func()
	for i := 0; i < 8; i++ {
		_, finish, e := repo.beginRecordingTransaction(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, finish)
	}
	defer func() {
		for _, f := range held {
			f()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 64)
	for i := 0; i < 64; i++ {
		go func() {
			_, finish, e := repo.beginSubjectReadTransaction(ctx)
			if finish != nil {
				finish()
			}
			results <- e
		}()
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for {
		repo.transactionMu.Lock()
		n := repo.subjectWaiters
		repo.transactionMu.Unlock()
		if n == 64 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue did not reach64: %d", n)
		}
		time.Sleep(time.Millisecond)
	}
	at := time.Now()
	_, _, e := repo.beginSubjectReadTransaction(ctx)
	if !errors.Is(e, recordings.ErrTransactionCapacity) || time.Since(at) > 100*time.Millisecond {
		t.Fatalf("queue overflow not promptly rejected: %v", e)
	}
	if stats := db.Stats(); stats.InUse != 8 || stats.WaitCount != 0 {
		t.Fatalf("waiters borrowed DB connections: %+v", stats)
	}
	cancel()
	for i := 0; i < 64; i++ {
		if e = <-results; !errors.Is(e, context.Canceled) {
			t.Fatalf("cancel result=%v", e)
		}
	}
	repo.transactionMu.Lock()
	n := repo.subjectWaiters
	repo.transactionMu.Unlock()
	if n != 0 {
		t.Fatalf("leaked%d waiters", n)
	}
	held[0]()
	_, finish, e := repo.beginSubjectReadTransaction(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	finish()
}

func TestSubjectAdmissionTimeoutAndWake(t *testing.T) {
	for _, mode := range []string{"timeout", "parent deadline", "release"} {
		t.Run(mode, func(t *testing.T) {
			db := sql.OpenDB(budgetPanicConnector{})
			defer db.Close()
			db.SetMaxOpenConns(2)
			repo := NewRecordingRepository(db)
			_, hold, e := repo.beginRecordingTransaction(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			defer hold()
			ctx := context.Background()
			if mode == "parent deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 25*time.Millisecond)
				defer cancel()
			}
			if mode == "release" {
				go func() { time.Sleep(20 * time.Millisecond); hold() }()
			}
			at := time.Now()
			_, finish, e := repo.beginSubjectReadTransaction(ctx)
			elapsed := time.Since(at)
			if finish != nil {
				finish()
			}
			switch mode {
			case "timeout":
				if !errors.Is(e, recordings.ErrTransactionCapacity) || elapsed < 900*time.Millisecond || elapsed > 2*time.Second {
					t.Fatalf("wait was not bounded: %s %v", elapsed, e)
				}
			case "parent deadline":
				if !errors.Is(e, context.DeadlineExceeded) || elapsed > 200*time.Millisecond {
					t.Fatalf("parent deadline lost: %s %v", elapsed, e)
				}
			case "release":
				if e != nil {
					t.Fatal(e)
				}
			}
			if repo.subjectWaiters != 0 {
				t.Fatal("waiter leaked")
			}
		})
	}
}

// This synthetic 100ms admitted service-time control is not historical evidence
// or a measurement of PostgreSQL latency. It checks the queue budget separately
// from the fast local SQL fixture: 50 requests need nine waves of six read slots.
func TestSubjectAdmissionFiftyAtHundredMillisecondServiceTime(t *testing.T) {
	db := sql.OpenDB(budgetPanicConnector{})
	defer db.Close()
	db.SetMaxOpenConns(10)
	repo := NewRecordingRepository(db)
	start := make(chan struct{})
	results := make(chan error, 50)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	at := time.Now()
	for i := 0; i < 50; i++ {
		go func() {
			<-start
			_, finish, e := repo.beginSubjectReadTransaction(ctx)
			if e == nil {
				select {
				case <-time.After(100 * time.Millisecond):
				case <-ctx.Done():
					e = ctx.Err()
				}
				finish()
			}
			results <- e
		}()
	}
	close(start)
	failed := 0
	for i := 0; i < 50; i++ {
		if e := <-results; e != nil {
			failed++
		}
	}
	t.Logf("success=%d/50 elapsed=%s", 50-failed, time.Since(at))
	if failed != 0 {
		t.Fatalf("%d bounded read requests failed", failed)
	}
}

func TestSubjectAdmissionLeavesMutationHeadroom(t *testing.T) {
	db := sql.OpenDB(budgetPanicConnector{})
	defer db.Close()
	db.SetMaxOpenConns(10)
	repo := NewRecordingRepository(db)
	var held []func()
	defer func() {
		for _, f := range held {
			f()
		}
	}()
	for i := 0; i < 6; i++ {
		_, f, e := repo.beginSubjectReadTransaction(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		held = append(held, f)
	}
	if _, _, e := repo.beginRecordingTransactionClass(context.Background(), true); !errors.Is(e, recordings.ErrTransactionCapacity) {
		t.Fatalf("seventh reader accepted: %v", e)
	}
	for i := 0; i < 2; i++ {
		_, f, e := repo.beginRecordingTransaction(context.Background())
		if e != nil {
			t.Fatalf("readers consumed mutation headroom: %v", e)
		}
		held = append(held, f)
	}
	if _, _, e := repo.beginRecordingTransaction(context.Background()); !errors.Is(e, recordings.ErrTransactionCapacity) {
		t.Fatalf("total budget exceeded: %v", e)
	}
	if stats := db.Stats(); stats.InUse != 8 {
		t.Fatalf("budget=%+v", stats)
	}
	for _, f := range held {
		f()
	}
	if repo.transactions != 0 || repo.subjectTransactions != 0 {
		t.Fatal("class permit leak")
	}
}

func TestSubjectAdmissionDriverPanicReleasesBothCounts(t *testing.T) {
	for _, phase := range []string{"commit", "rollback"} {
		t.Run(phase, func(t *testing.T) {
			db := sql.OpenDB(budgetPanicConnector{phase})
			defer db.Close()
			db.SetMaxOpenConns(10)
			repo := NewRecordingRepository(db)
			func() {
				defer func() {
					if recover() == nil {
						t.Error("panic lost")
					}
				}()
				tx, finish, e := repo.beginSubjectReadTransaction(context.Background())
				if e != nil {
					t.Fatal(e)
				}
				defer finish()
				if phase == "commit" {
					tx.Commit()
				}
			}()
			if repo.transactions != 0 || repo.subjectTransactions != 0 {
				t.Fatalf("panic leaked total=%d reads=%d", repo.transactions, repo.subjectTransactions)
			}
		})
	}
}

func TestSubjectReadSublimitSmallPools(t *testing.T) {
	for _, tc := range []struct{ maximum, readLimit int }{{1, 0}, {2, 1}, {3, 1}, {4, 1}, {5, 1}, {10, 6}, {0, 20}} {
		db := sql.OpenDB(budgetPanicConnector{})
		db.SetMaxOpenConns(tc.maximum)
		repo := NewRecordingRepository(db)
		admitted := 0
		for i := 0; i < 20; i++ {
			if !repo.admitRecordingTransactionClass(true) {
				break
			}
			admitted++
		}
		if admitted != tc.readLimit {
			t.Errorf("max%d readlimit=%d want%d", tc.maximum, admitted, tc.readLimit)
		}
		for i := 0; i < admitted; i++ {
			repo.releaseRecordingTransactionClass(true)
		}
		db.Close()
	}
}
