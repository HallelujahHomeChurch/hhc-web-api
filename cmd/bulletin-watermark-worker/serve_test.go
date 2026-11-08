package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Every --once exit path of a Container Apps Job execution (PLAN-v2 W5).
func TestServeOnceRunsExactlyOneStep(t *testing.T) {
	processErr := errors.New("queue identity rejected")
	cases := map[string]struct {
		processed bool
		err       error
		wantErr   error
	}{
		"empty queue exits 0":                       {processed: false},
		"finished, terminal or retried job exits 0": {processed: true},
		"process-level error exits 1":               {processed: true, err: processErr, wantErr: processErr},
		"dequeue failure exits 1":                   {processed: false, err: processErr, wantErr: processErr},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := serve(context.Background(), true, time.Hour, func(context.Context) (bool, error) {
				calls++
				return tc.processed, tc.err
			})
			if calls != 1 || !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestServeOnceSkipsWorkAfterShutdownSignal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	if err := serve(ctx, true, time.Hour, func(context.Context) (bool, error) { calls++; return true, nil }); err != nil || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestServeLoopKeepsPollingUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := serve(ctx, false, time.Millisecond, func(context.Context) (bool, error) {
		calls++
		if calls == 3 {
			cancel()
		}
		return calls%2 == 0, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
