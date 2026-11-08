package recordings

import (
	"context"
	"errors"
)

func (s *Service) Delete(ctx context.Context, id string, expected int64, actor string, canPublish bool) error {
	if expected < 1 || actor == "" {
		return ErrInvalid
	}
	return s.repo.Delete(ctx, id, expected, actor, s.now(), canPublish)
}

// CMS deletion commits independently of provider availability. A restart or
// lost response replays the idempotent private command before marking delivery.
func (s *Service) ReconcileDeletions(ctx context.Context) error {
	if s.packages == nil {
		return errors.New("recording cleanup client unavailable")
	}
	ids, err := s.repo.ListPendingDeletions(ctx)
	if err != nil {
		return err
	}
	var failures error
	for _, id := range ids {
		if ctx.Err() != nil {
			return errors.Join(failures, ctx.Err())
		}
		// Only attempted items rotate; a timed-out batch must not advance items
		// that never received a provider request.
		if err := s.repo.MarkDeletionAttempt(ctx, id); err != nil {
			return errors.Join(failures, err)
		}
		if err := s.packages.DeleteRecordingMedia(ctx, id); err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if err := s.repo.MarkDeletionDelivered(ctx, id); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}
