package publication

import (
	"context"
	"errors"
	"fmt"
	"time"
)

func (w *Worker) learnBulletin(ctx context.Context, event Event) error {
	repo, ok := w.repository.(interface {
		CompleteOnlineBulletinLearning(context.Context, Event, time.Time) error
		RescheduleOnlineBulletinLearning(context.Context, Event, time.Time, time.Time) error
	})
	if !ok {
		return fmt.Errorf("bulletin learning repository unavailable")
	}
	err := repo.CompleteOnlineBulletinLearning(ctx, event, w.now().UTC())
	if err == nil || errors.Is(err, ErrStalePublication) {
		return nil
	}
	now := w.now().UTC()
	next := now.Add(time.Duration(1<<min(event.Attempts-1, 5)) * 5 * time.Second)
	if event.Attempts >= w.maxAttempts {
		next = time.Time{}
	}
	return repo.RescheduleOnlineBulletinLearning(ctx, event, next, now)
}
