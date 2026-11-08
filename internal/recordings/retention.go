package recordings

import (
	"context"
	"errors"
	"time"
)

type RetentionRepository interface {
	ListExpired(context.Context, time.Time) ([]string, error)
	Expire(context.Context, string, time.Time) error
}
type Retention struct {
	repo RetentionRepository
	get  func(context.Context, string) (Recording, error)
}

func NewRetention(repo RetentionRepository, get func(context.Context, string) (Recording, error)) *Retention {
	return &Retention{repo: repo, get: get}
}
func (w *Retention) RunOnce(ctx context.Context, now time.Time) error {
	ids, err := w.repo.ListExpired(ctx, now)
	if err != nil {
		return err
	}
	for _, id := range ids {
		value, err := w.get(ctx, id)
		if err != nil {
			return err
		}
		// Asset owns byte cleanup, including source attempts and existing-grant grace.
		if value.ReadyAt == nil || !value.Expired(now) {
			continue
		}
		if err := w.repo.Expire(ctx, id, now); errors.Is(err, ErrConflict) {
			continue
		} else if err != nil {
			return err
		}
	}
	return nil
}
