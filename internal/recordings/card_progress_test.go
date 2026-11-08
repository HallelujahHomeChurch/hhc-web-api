package recordings

import (
	"context"
	"errors"
	"testing"
	"time"
)

type cardProgressRepo struct {
	Repository
	calls int
	err   error
}

func (r *cardProgressRepo) GetCardProgress(context.Context, string, []string, time.Time) (map[string]CardPlayback, error) {
	r.calls++
	return map[string]CardPlayback{}, r.err
}
func TestCardProgressBoundsAndFailure(t *testing.T) {
	repo := &cardProgressRepo{}
	s := NewService(repo, "", nil, time.Now)
	user := "11111111-1111-4111-8111-111111111111"
	id := "22222222-2222-4222-8222-222222222222"
	if _, err := s.GetCardProgress(context.Background(), user, nil); err != nil || repo.calls != 0 {
		t.Fatalf("empty lookup: %v calls=%d", err, repo.calls)
	}
	for _, ids := range [][]string{{"bad"}, make([]string, 101)} {
		if _, err := s.GetCardProgress(context.Background(), user, ids); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bounds: %v", err)
		}
	}
	repo.err = errors.New("db unavailable")
	if _, err := s.GetCardProgress(context.Background(), user, []string{id}); !errors.Is(err, repo.err) || repo.calls != 1 {
		t.Fatalf("database error hidden: %v calls=%d", err, repo.calls)
	}
}
