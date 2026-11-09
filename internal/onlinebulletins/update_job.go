package onlinebulletins

import (
	"context"
	"errors"
	"time"
)

const UpdateJobTimeout = 4 * time.Minute

type UpdateJob struct {
	ID, DocumentID, ExtractionJobID, LeaseToken     string
	CanonicalVersion, OnlineVersion, SourceRevision int64
	Attempts                                        int
	LeaseUntil                                      time.Time
	Input                                           UpdateInput
	SavedAttempts                                   []UpdateAttempt
}

type UpdateStore interface {
	ClaimOnlineBulletinUpdate(context.Context, time.Time, time.Duration, int) (*UpdateJob, error)
	SaveOnlineBulletinUpdateAttempt(context.Context, UpdateJob, UpdateAttempt, time.Time) error
	CompleteOnlineBulletinUpdate(context.Context, UpdateJob, UpdateEvidence, time.Time) error
	FailOnlineBulletinUpdate(context.Context, UpdateJob, string, time.Time, int) error
}

func (w ExtractionWorker) runUpdate(parent context.Context, store UpdateStore, job UpdateJob) error {
	deadline := documentDeadline(parent, job.LeaseUntil, UpdateJobTimeout)
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	processing, stop := context.WithDeadline(ctx, deadline.Add(-10*time.Second))
	evidence, err := func() (result UpdateEvidence, err error) {
		defer func() {
			if recover() != nil {
				result, err = UpdateEvidence{}, ErrUpdatePlan
			}
		}()
		return w.Update(processing, job)
	}()
	stop()
	if err != nil {
		code := "update_plan_failed"
		if errors.Is(err, ErrExtractionLease) {
			return err
		}
		if errors.Is(err, ErrUpdateSource) {
			code = "source_not_ready"
		} else if errors.Is(err, ErrRecognition) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			code = "provider_unavailable"
		} else if !errors.Is(err, ErrUpdatePlan) {
			return err
		}
		return store.FailOnlineBulletinUpdate(ctx, job, code, time.Now().UTC(), w.MaxAttempts)
	}
	return store.CompleteOnlineBulletinUpdate(ctx, job, evidence, time.Now().UTC())
}
