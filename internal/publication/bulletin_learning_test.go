package publication

import (
	"context"
	"errors"
	"testing"
	"time"
)

type learningRepository struct {
	*workerRepository
	cause error
	calls int
	next  time.Time
}

func (r *learningRepository) CompleteOnlineBulletinLearning(context.Context, Event, time.Time) error {
	r.calls++
	return r.cause
}
func (r *learningRepository) RescheduleOnlineBulletinLearning(_ context.Context, _ Event, next, _ time.Time) error {
	r.next = next
	r.retryCount++
	return nil
}

func TestBulletinLearningUsesFencedRetryAndNeverReschedulesStaleClaim(t *testing.T) {
	for _, tc := range []struct {
		name            string
		cause           error
		attempts        int
		retry, terminal bool
	}{
		{"success", nil, 1, false, false}, {"retry", errors.New("storage unavailable"), 1, true, false}, {"exhausted", errors.New("storage unavailable"), 5, true, true}, {"stale", ErrStalePublication, 1, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &learningRepository{workerRepository: &workerRepository{event: Event{ID: "learning", EventType: "bulletin.online.learn", Attempts: tc.attempts}}, cause: tc.cause}
			found, err := NewWorker(repo, &workerAssets{}, 5).processNext(context.Background())
			if err != nil || !found || repo.calls != 1 || (repo.retryCount == 1) != tc.retry || tc.retry && repo.next.IsZero() != tc.terminal {
				t.Fatal("learning event handling", err, repo.retryCount, repo.next)
			}
		})
	}
}
