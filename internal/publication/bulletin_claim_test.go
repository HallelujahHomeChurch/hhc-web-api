package publication

import (
	"context"
	"errors"
	"testing"
	"time"
)

type bulletinClaimRepository struct {
	workerRepository
	got     Event
	waiting bool
	calls   int
	now     time.Time
}

func (r *bulletinClaimRepository) RescheduleBulletinPublication(_ context.Context, event Event, _ string, _ time.Time, now time.Time, waiting bool) error {
	r.got, r.waiting = event, waiting
	r.now = now
	r.calls++
	return nil
}
func TestBulletinWorkerReschedulesWithExactClaim(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "defer"}[waiting], func(t *testing.T) {
			event := publishEvent(1)
			event.CreatedAt = time.Now().UTC()
			event.ClaimedUntil = time.Now().UTC().Add(time.Minute)
			repo := &bulletinClaimRepository{workerRepository: workerRepository{event: event}}
			assets := &workerAssets{asset: Asset{ID: "asset-1", OwnerService: "hhc-web-api", Namespace: "cms.weekly.pdf", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant", UploadStatus: "completed", ScanStatus: "pending", ProcessingStatus: "pending"}}
			if !waiting {
				assets.asset.ScanStatus = "clean"
				assets.asset.ProcessingStatus = "ready"
				repo.completePublishError = errors.New("transient")
			}
			worker := NewWorker(repo, assets, 5)
			if _, err := worker.processNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if repo.calls != 1 || repo.got.ID != event.ID || repo.got.Attempts != event.Attempts || !repo.got.ClaimedUntil.Equal(event.ClaimedUntil) || repo.waiting != waiting || repo.retryCount != 0 || repo.deferCount != 0 {
				t.Fatalf("claim identity lost: %+v", repo)
			}
		})
	}
}

type bulletinClockAssets struct {
	workerAssets
	clock *time.Time
	after time.Time
}

func (a *bulletinClockAssets) Get(ctx context.Context, id string) (Asset, error) {
	*a.clock = a.after
	return a.workerAssets.Get(ctx, id)
}

type bulletinClockRepository struct {
	bulletinClaimRepository
	completedAt time.Time
	failedAt    time.Time
}

func (r *bulletinClockRepository) CompletePublish(_ context.Context, _ Event, _, _ string, now time.Time) error {
	r.completedAt = now
	return nil
}
func (r *bulletinClockRepository) Fail(_ context.Context, _ Event, _ string, now time.Time) error {
	r.failedAt = now
	return nil
}
func TestBulletinTransitionsUseTimeAfterAssetCall(t *testing.T) {
	for _, outcome := range []string{"ready", "pending", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			clock := time.Now().UTC()
			after := clock.Add(time.Minute)
			event := publishEvent(1)
			event.CreatedAt = clock
			repo := &bulletinClockRepository{bulletinClaimRepository: bulletinClaimRepository{workerRepository: workerRepository{event: event}}}
			assets := &bulletinClockAssets{clock: &clock, after: after, workerAssets: workerAssets{asset: Asset{ID: "asset-1", OwnerService: "hhc-web-api", Namespace: "cms.weekly.pdf", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready"}}}
			if outcome == "pending" {
				assets.asset.ScanStatus = "pending"
			}
			if outcome == "failure" {
				assets.getError = terminalError{errors.New("rejected")}
			}
			worker := NewWorker(repo, assets, 5)
			worker.now = func() time.Time { return clock }
			if _, err := worker.processNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := repo.completedAt
			if outcome == "pending" {
				got = repo.now
			}
			if outcome == "failure" {
				got = repo.failedAt
			}
			if !got.Equal(after) {
				t.Fatalf("transition used pre-call clock: got %s want %s", got, after)
			}
		})
	}
}
