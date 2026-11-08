package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestRecordingPublicationRechecksWithinThirtySecondsAfterError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls atomic.Int32
		go runRecordingAutoPublication(ctx, func(runCtx context.Context, limit int) error {
			calls.Add(1)
			deadline, ok := runCtx.Deadline()
			if !ok || deadline.Sub(time.Now()) != 30*time.Second || limit != 100 {
				t.Errorf("unexpected run budget or batch: deadline=%v limit=%d", deadline, limit)
			}
			return errors.New("dependency not yet ready")
		})
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("startup calls=%d, want 1", calls.Load())
		}
		time.Sleep(29 * time.Second)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("early calls=%d, want 1", calls.Load())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("calls after 30 seconds=%d, want 2; publication must not wait for maintenance", calls.Load())
		}
		cancel()
		synctest.Wait()
		time.Sleep(time.Minute)
		if calls.Load() != 2 {
			t.Fatalf("publication continued after cancellation: %d", calls.Load())
		}
	})
}

func TestRecordingPublicationTimeoutIsSerialAndCancellationStopsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var calls, active atomic.Int32
		done := make(chan struct{})
		go func() {
			defer close(done)
			runRecordingAutoPublication(ctx, func(runCtx context.Context, _ int) error {
				calls.Add(1)
				active.Add(1)
				if active.Load() != 1 {
					t.Error("publication runs overlap")
				}
				<-runCtx.Done()
				active.Add(-1)
				return runCtx.Err()
			})
		}()
		synctest.Wait()
		time.Sleep(30 * time.Second)
		synctest.Wait()
		if calls.Load() != 2 || active.Load() != 1 {
			t.Fatalf("timeout did not permit next serial run: calls=%d active=%d", calls.Load(), active.Load())
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("runner did not stop")
		}
		if active.Load() != 0 {
			t.Fatal("run remains active")
		}
	})
}
