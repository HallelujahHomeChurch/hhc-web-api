package recordings

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

const LifecycleRepairLimit = 1000

var ErrLifecycleRepairCapacity = errors.New("lifecycle repair capacity reached")

// A permanent constraint failure is scoped to a known projection statement.
type LifecycleApplyFailure struct{ Err error }

func (e *LifecycleApplyFailure) Error() string { return "invalid lifecycle projection" }
func (e *LifecycleApplyFailure) Unwrap() error { return e.Err }

type LifecycleRepair struct {
	Binding        assetclient.RecordingLifecycleBinding `json:"binding"`
	Attempts       int                                   `json:"attempts"`
	FirstFailureAt time.Time                             `json:"firstFailureAt"`
	NextAttempt    time.Time                             `json:"nextAttempt"`
	Reason         string                                `json:"reason"`
}
type LifecycleCheckpoint struct {
	Cursor                string            `json:"cursor"`
	Repairs               []LifecycleRepair `json:"repairs"`
	PendingAttempts       int               `json:"pendingAttempts"`
	FirstFailureAt        time.Time         `json:"firstFailureAt"`
	CycleStartedAt        time.Time         `json:"cycleStartedAt"`
	LastSuccessfulCycleAt time.Time         `json:"lastSuccessfulCycleAt"`
	NextAttempt           time.Time         `json:"nextAttempt"`
	AuthorityFailures     int               `json:"authorityFailures"`
}
type LifecycleClaim struct {
	Token string
	State LifecycleCheckpoint
}
type LifecycleCycleRepository interface {
	ClaimLifecycleCycle(context.Context, time.Time) (LifecycleClaim, error)
	ReleaseLifecycleCycle(context.Context, string) error
	LifecycleBindingPage(context.Context, string, int) ([]assetclient.RecordingLifecycleBinding, error)
	LifecycleBindingCurrent(context.Context, assetclient.RecordingLifecycleBinding) (bool, error)
	CommitLifecycleCycle(context.Context, string, LifecycleCheckpoint, assetclient.RecordingLifecycleSnapshot) error
}
type LifecycleTickResult struct {
	Scanned, Repaired, Outstanding int
	OldestFailureAt                time.Time
}

// Every tick owns a durable lease; no database transaction spans Asset I/O.
func (s *Service) ReconcileLifecycle(ctx context.Context) (LifecycleTickResult, error) {
	var result LifecycleTickResult
	repo, ok := s.repo.(LifecycleCycleRepository)
	if !ok || s.packages == nil {
		return result, assetclient.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	now := s.now().UTC()
	claim, err := repo.ClaimLifecycleCycle(ctx, now)
	if err != nil || claim.Token == "" {
		return result, err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = repo.ReleaseLifecycleCycle(release, claim.Token)
	}()
	state := claim.State
	if state.NextAttempt.After(now) {
		return result, nil
	}
	if state.CycleStartedAt.IsZero() {
		state.CycleStartedAt = now
	}
	// Repairs receive at most 20 records and two seconds; normal work keeps its own budget.
	var due, remaining []LifecycleRepair
	for _, repair := range state.Repairs {
		if len(due) < 20 && !repair.NextAttempt.After(now) {
			due = append(due, repair)
		} else {
			remaining = append(remaining, repair)
		}
	}
	failure := func(cause error) (LifecycleTickResult, error) {
		state.AuthorityFailures++
		state.NextAttempt = now.Add(lifecycleBackoff(state.AuthorityFailures))
		if save := repo.CommitLifecycleCycle(ctx, claim.Token, state, assetclient.RecordingLifecycleSnapshot{}); save != nil {
			return result, save
		}
		return result, cause
	}

	attemptedRepairs := 0

	if len(due) > 0 {
		repairCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		updated := remaining
		for index, repair := range due {
			if repairCtx.Err() != nil {
				break
			}
			attemptedRepairs++
			current, err := repo.LifecycleBindingCurrent(repairCtx, repair.Binding)
			if err != nil {
				return result, err
			}
			part := assetclient.RecordingLifecycleSnapshot{}
			bad := map[string]bool{}
			if current {
				part, bad, err = s.fetchLifecyclePartition(repairCtx, []assetclient.RecordingLifecycleBinding{repair.Binding})
			}
			timedOut := errors.Is(repairCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
			if err != nil && !timedOut {
				return failure(err)
			}
			keep := timedOut || bad[repair.Binding.RecordingID]
			if keep {
				part = assetclient.RecordingLifecycleSnapshot{}
				repair.Attempts++
				repair.NextAttempt = now.Add(lifecycleBackoff(repair.Attempts))
			}
			next := state
			next.Repairs = append(slices.Clone(updated), due[index+1:]...)
			if keep {
				next.Repairs = append(next.Repairs, repair)
			}
			err = repo.CommitLifecycleCycle(ctx, claim.Token, next, part)
			var invalid *LifecycleApplyFailure
			if errors.As(err, &invalid) {
				keep = true
				repair.Attempts++
				repair.NextAttempt = now.Add(lifecycleBackoff(repair.Attempts))
				repair.Reason = "projection_constraint"
				next.Repairs = append(slices.Clone(updated), due[index+1:]...)
				next.Repairs = append(next.Repairs, repair)
				err = repo.CommitLifecycleCycle(ctx, claim.Token, next, assetclient.RecordingLifecycleSnapshot{})
			}
			if err != nil {
				return result, err
			}
			state = next
			if keep {
				updated = append(updated, repair)
			} else {
				result.Repaired++
			}
		}
	}

	limit := 100 - attemptedRepairs

	normal, cancel := context.WithTimeout(ctx, 7*time.Second)
	defer cancel()
	bindings, err := repo.LifecycleBindingPage(normal, state.Cursor, limit)
	if err != nil {
		return result, err
	}
	result.Scanned = len(bindings)
	if len(bindings) > 0 {
		before := state
		before.Repairs = slices.Clone(state.Repairs)
		// Known poison belongs to repair, so it cannot poison another normal batch.
		repairing := make(map[assetclient.RecordingLifecycleBinding]bool, len(state.Repairs))
		for _, repair := range state.Repairs {
			repairing[repair.Binding] = true
		}
		fresh := make([]assetclient.RecordingLifecycleBinding, 0, len(bindings))
		for _, binding := range bindings {
			if !repairing[binding] {
				fresh = append(fresh, binding)
			}
		}
		snapshot := assetclient.RecordingLifecycleSnapshot{}
		bad := map[string]bool{}
		var err error
		if len(fresh) > 0 {
			snapshot, bad, err = s.fetchLifecyclePartition(normal, fresh)
		}
		if err != nil {
			return failure(err)
		}
		if len(bad) > 0 {
			state.PendingAttempts++
			if state.FirstFailureAt.IsZero() {
				state.FirstFailureAt = now
			}
			if state.PendingAttempts >= 3 {
				existing := make(map[assetclient.RecordingLifecycleBinding]bool, len(state.Repairs))
				for _, repair := range state.Repairs {
					existing[repair.Binding] = true
				}
				for _, binding := range bindings {
					if bad[binding.RecordingID] && !existing[binding] {
						state.Repairs = append(state.Repairs, LifecycleRepair{Binding: binding, Attempts: 3, FirstFailureAt: state.FirstFailureAt, NextAttempt: now.Add(lifecycleBackoff(3)), Reason: "invalid_or_missing_binding"})
					}
				}
				if len(state.Repairs) > LifecycleRepairLimit {
					return result, ErrLifecycleRepairCapacity
				}
			}
		}
		if len(bad) == 0 || state.PendingAttempts >= 3 {
			state.Cursor = bindings[len(bindings)-1].RecordingID
			state.PendingAttempts = 0
			state.FirstFailureAt = time.Time{}
		}
		state.AuthorityFailures = 0
		state.NextAttempt = time.Time{}
		err = repo.CommitLifecycleCycle(normal, claim.Token, state, snapshot)
		var invalid *LifecycleApplyFailure
		if errors.As(err, &invalid) {
			// The transaction rolled back. Retain the entire unapplied range.
			state = before
			state.PendingAttempts++
			if state.FirstFailureAt.IsZero() {
				state.FirstFailureAt = now
			}
			if state.PendingAttempts >= 3 {
				existing := make(map[assetclient.RecordingLifecycleBinding]bool, len(state.Repairs))
				for _, repair := range state.Repairs {
					existing[repair.Binding] = true
				}
				for _, binding := range bindings {
					if !existing[binding] {
						state.Repairs = append(state.Repairs, LifecycleRepair{Binding: binding, Attempts: 3, FirstFailureAt: state.FirstFailureAt, NextAttempt: now.Add(lifecycleBackoff(3)), Reason: "projection_constraint"})
					}
				}
				if len(state.Repairs) > LifecycleRepairLimit {
					return result, ErrLifecycleRepairCapacity
				}
				state.Cursor = bindings[len(bindings)-1].RecordingID
				state.PendingAttempts = 0
				state.FirstFailureAt = time.Time{}
			}
			err = repo.CommitLifecycleCycle(normal, claim.Token, state, assetclient.RecordingLifecycleSnapshot{})
		}
		if err != nil {
			return result, err
		}
	}
	if len(bindings) < limit && state.PendingAttempts == 0 {
		state.Cursor = ""
		if len(state.Repairs) == 0 {
			state.LastSuccessfulCycleAt = now
			state.CycleStartedAt = now
		}
		if err = repo.CommitLifecycleCycle(normal, claim.Token, state, assetclient.RecordingLifecycleSnapshot{}); err != nil {
			return result, err
		}
	}
	result.Outstanding = len(state.Repairs)
	for _, repair := range state.Repairs {
		if result.OldestFailureAt.IsZero() || repair.FirstFailureAt.Before(result.OldestFailureAt) {
			result.OldestFailureAt = repair.FirstFailureAt
		}
	}
	return result, nil
}

func lifecycleBackoff(attempts int) time.Duration {
	if attempts > 4 {
		attempts = 4
	}
	if attempts < 1 {
		attempts = 1
	}
	return time.Duration(1<<uint(attempts-1)) * 30 * time.Second
}

// A malformed global policy is an authority failure. Missing or invalid individual
// bindings create repair obligations, never deletion or fabricated readiness.
func (s *Service) fetchLifecyclePartition(ctx context.Context, bindings []assetclient.RecordingLifecycleBinding) (assetclient.RecordingLifecycleSnapshot, map[string]bool, error) {
	v, err := s.packages.RecordingLifecycleWithSources(ctx, bindings, nil)
	if errors.Is(err, assetclient.ErrNotFound) {
		bad := make(map[string]bool, len(bindings))
		for _, binding := range bindings {
			bad[binding.RecordingID] = true
		}
		return assetclient.RecordingLifecycleSnapshot{}, bad, nil
	}
	if err != nil {
		return v, nil, err
	}
	if v.Policy.RetentionDays < 1 || v.Policy.RetentionDays > 365 || v.Policy.Revision < 1 || len(v.SourceItems) != 0 {
		return assetclient.RecordingLifecycleSnapshot{}, nil, ErrConflict
	}
	byID := make(map[string][]assetclient.RecordingPackageStatus, len(v.Items))
	for _, item := range v.Items {
		byID[item.RecordingID] = append(byID[item.RecordingID], item)
	}
	good := assetclient.RecordingLifecycleSnapshot{Policy: v.Policy}
	bad := make(map[string]bool)
	for _, binding := range bindings {
		items := byID[binding.RecordingID]
		one := assetclient.RecordingLifecycleSnapshot{Policy: v.Policy, Items: items}
		if validateLifecycleSnapshot(one, []assetclient.RecordingLifecycleBinding{binding}, nil, s.now()) != nil {
			bad[binding.RecordingID] = true
		} else {
			good.Items = append(good.Items, items[0])
		}
		delete(byID, binding.RecordingID)
	}
	if len(byID) != 0 {
		for _, binding := range bindings {
			bad[binding.RecordingID] = true
		}
		return assetclient.RecordingLifecycleSnapshot{}, bad, nil
	}
	return good, bad, nil
}
