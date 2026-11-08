package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestSlowLiveGrantsReserveDatabaseCapacity(t *testing.T) {
	repo, c, now, _, _, _, _ := liveFixture(t)
	repo.db.SetMaxOpenConns(10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}, 6), make(chan struct{})
	results := make(chan error, 6)
	defer func() {
		close(release)
		for i := 0; i < 6; i++ {
			if err := <-results; err != nil {
				t.Error(err)
			}
		}
	}()
	for i := 0; i < 6; i++ {
		go func() {
			results <- repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID(), platform.NewID(), *now, false, func(recordings.Capture, string) error {
				entered <- struct{}{}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
		}()
	}
	for i := 0; i < 6; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	before := repo.db.Stats()
	started := time.Now()
	for i := 0; i < 2; i++ {
		called := false
		readCtx, stop := context.WithTimeout(ctx, 25*time.Millisecond)
		err := repo.WithCaptureScope(readCtx, c.RecordingID, c.Status.CaptureID, platform.NewID(), platform.NewID(), *now, false, func(recordings.Capture, string) error { called = true; return nil })
		stop()
		if !errors.Is(err, context.DeadlineExceeded) || called {
			t.Fatalf("excess grant admitted: called=%v err=%v", called, err)
		}
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatal("subject queue ignored its caller deadline")
	}
	if repo.db.Stats().WaitCount != before.WaitCount {
		t.Fatal("capacity rejection checked out a connection")
	}
	if got := repo.db.Stats().InUse; got != 6 {
		t.Fatalf("in use=%d, want6", got)
	}
	for i := 0; i < 2; i++ {
		_, finish, err := repo.beginRecordingTransaction(ctx)
		if err != nil {
			t.Fatalf("viewers consumed mutation headroom: %v", err)
		}
		defer finish()
	}
	response := httptest.NewRecorder()
	assertDatabaseProbeAvailable(t, repo.db)
	httpapi.New(nil, repo.db, nil).Routes().ServeHTTP(response, httptest.NewRequest("GET", "/ready", nil))
	if response.Code != 200 {
		t.Fatalf("readiness=%d %s", response.Code, response.Body.String())
	}
}

func TestLiveGrantCapacityReleasesOnErrorAndCancellation(t *testing.T) {
	repo, c, now, _, _, _, _ := liveFixture(t)
	repo.db.SetMaxOpenConns(2)
	failure := errors.New("grant failed")
	ctx := context.Background()
	call := func(ctx context.Context, capture string, issue func(recordings.Capture, string) error) error {
		return repo.WithCaptureScope(ctx, c.RecordingID, capture, platform.NewID(), platform.NewID(), *now, false, issue)
	}
	for _, phase := range []string{"callback", "fence", "cancelled callback", "cancelled admission"} {
		t.Run(phase, func(t *testing.T) {
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			capture := c.Status.CaptureID
			if phase == "fence" {
				capture = platform.NewID()
			}
			if phase == "cancelled admission" {
				cancel()
			}
			err := call(runCtx, capture, func(recordings.Capture, string) error {
				if phase == "cancelled callback" {
					cancel()
					return runCtx.Err()
				}
				return failure
			})
			if err == nil || errors.Is(err, recordings.ErrTransactionCapacity) {
				t.Fatalf("unexpected initial failure: %v", err)
			}
			if err := call(ctx, c.Status.CaptureID, func(recordings.Capture, string) error { return nil }); err != nil {
				t.Fatalf("permit leaked after %s: %v", phase, err)
			}
		})
	}
	closed, err := sql.Open("pgx", "")
	if err != nil {
		t.Fatal(err)
	}
	closed.SetMaxOpenConns(2)
	closed.Close()
	repo = NewRecordingRepository(closed)
	for i := 0; i < 2; i++ {
		if err := call(ctx, c.Status.CaptureID, func(recordings.Capture, string) error { return nil }); err == nil || errors.Is(err, recordings.ErrTransactionCapacity) {
			t.Fatalf("begin failure leaked permit: %v", err)
		}
	}
}

func TestLiveGrantCapacityTracksFinitePoolBudget(t *testing.T) {
	for _, tc := range []struct{ maximum, admitted int }{{1, 0}, {2, 1}, {3, 1}, {10, 8}, {0, 20}} {
		t.Run(fmt.Sprint(tc.maximum), func(t *testing.T) {
			db, err := sql.Open("pgx", "")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.SetMaxOpenConns(tc.maximum)
			repo := NewRecordingRepository(db)
			for i := 0; i < tc.admitted; i++ {
				if !repo.admitRecordingTransaction() {
					t.Fatalf("unexpected rejection at %d", i)
				}
			}
			if tc.maximum > 0 && repo.admitRecordingTransaction() {
				t.Fatal("exceeded finite pool budget")
			}
			for i := 0; i < tc.admitted; i++ {
				repo.releaseRecordingTransaction()
			}
			if tc.admitted > 0 {
				if !repo.admitRecordingTransaction() {
					t.Fatal("released capacity unavailable")
				}
				repo.releaseRecordingTransaction()
			}
		})
	}
}
