package recordings

import (
	"context"
	"errors"
	"testing"
	"time"
)

type retentionRepo struct {
	expired bool
	err     error
}

func (r *retentionRepo) ListExpired(context.Context, time.Time) ([]string, error) {
	return []string{"rec-a"}, nil
}
func (r *retentionRepo) Expire(context.Context, string, time.Time) error {
	r.expired = true
	return r.err
}

func TestRetentionExpiresOnlyReadyDueRecordings(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	for _, tc := range []struct {
		value  Recording
		expire bool
	}{
		{Recording{ReadyAt: &now, ExpiresAt: &now}, true},
		{Recording{ReadyAt: &now, ExpiresAt: &future}, false},
		{Recording{DraftExpiresAt: now.Add(-time.Hour)}, false},
	} {
		repo := &retentionRepo{}
		worker := NewRetention(repo, func(context.Context, string) (Recording, error) { return tc.value, nil })
		if err := worker.RunOnce(context.Background(), now); err != nil || repo.expired != tc.expire {
			t.Fatalf("expired=%v err=%v", repo.expired, err)
		}
	}
}

func TestRetentionPropagatesFailureButToleratesConcurrentStateChange(t *testing.T) {
	now := time.Now().UTC()
	for _, fail := range []error{errors.New("database unavailable"), ErrConflict} {
		repo := &retentionRepo{err: fail}
		worker := NewRetention(repo, func(context.Context, string) (Recording, error) {
			return Recording{ReadyAt: &now, ExpiresAt: &now}, nil
		})
		err := worker.RunOnce(context.Background(), now)
		if errors.Is(fail, ErrConflict) {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, fail) {
			t.Fatalf("lost failure: %v", err)
		}
	}
}
