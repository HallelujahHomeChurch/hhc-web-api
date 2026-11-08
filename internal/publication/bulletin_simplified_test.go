package publication

import (
	"context"
	"errors"
	"testing"
	"time"
)

type simplifiedReleaseRepository struct {
	*workerRepository
	releaseErr  error
	rescheduled bool
	next        time.Time
}

func (r *simplifiedReleaseRepository) CompleteSimplifiedRelease(context.Context, Event, time.Time) error {
	return r.releaseErr
}
func (r *simplifiedReleaseRepository) RescheduleSimplifiedRelease(_ context.Context, _ Event, next, _ time.Time) error {
	r.rescheduled = true
	r.next = next
	return nil
}
func TestSimplifiedReleaseWorkerKeepsRetriesAndStaleClaimsSeparate(t *testing.T) {
	for _, tc := range []struct {
		name          string
		err           error
		attempts      int
		retry, failed bool
	}{
		{"done", nil, 1, false, false}, {"stale", ErrStalePublication, 1, false, false}, {"retry", errors.New("database unavailable"), 1, true, false}, {"exhausted", errors.New("database unavailable"), 3, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &simplifiedReleaseRepository{workerRepository: &workerRepository{event: Event{EventType: "bulletin.online.publish_simplified", Attempts: tc.attempts}}, releaseErr: tc.err}
			w := NewWorker(r, nil, 3)
			if processed, err := w.processNext(context.Background()); err != nil || !processed {
				t.Fatal(processed, err)
			}
			if r.rescheduled != tc.retry || tc.retry && r.next.IsZero() != tc.failed {
				t.Fatal("wrong retry", r.rescheduled, r.next)
			}
			if r.completeCount != 0 {
				t.Fatal("generic completion bypassed source proof")
			}
		})
	}
}
