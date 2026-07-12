package publication

import (
	"context"
	"testing"
	"time"
)

func TestWorkerCompletesPublishForCleanAsset(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	repository := &workerRepository{event: publishEvent(1)}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-1",
		OwnerService:     "hhc-web-api",
		UploadStatus:     "completed",
		ScanStatus:       "clean",
		ProcessingStatus: "ready",
	}, grant: Grant{ID: "grant-1"}}
	worker := NewWorker(repository, assets, 5)
	worker.now = func() time.Time { return now }

	processed, err := worker.processNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !processed || repository.completedGrant != "grant-1" {
		t.Fatalf("processed=%v completedGrant=%q", processed, repository.completedGrant)
	}
	if repository.retryCount != 0 || repository.failCount != 0 {
		t.Fatalf("retry=%d fail=%d", repository.retryCount, repository.failCount)
	}
}

func TestWorkerRetriesWhileAssetScanIsPending(t *testing.T) {
	repository := &workerRepository{event: publishEvent(1)}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-1",
		OwnerService:     "hhc-web-api",
		UploadStatus:     "completed",
		ScanStatus:       "pending",
		ProcessingStatus: "pending",
	}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.retryCount != 1 || repository.failCount != 0 {
		t.Fatalf("retry=%d fail=%d", repository.retryCount, repository.failCount)
	}
}

func TestWorkerFailsImmediatelyForInfectedAsset(t *testing.T) {
	repository := &workerRepository{event: publishEvent(1)}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-1",
		OwnerService:     "hhc-web-api",
		UploadStatus:     "completed",
		ScanStatus:       "infected",
		ProcessingStatus: "failed",
	}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.failCount != 1 || repository.retryCount != 0 {
		t.Fatalf("fail=%d retry=%d", repository.failCount, repository.retryCount)
	}
}

func TestWorkerTreatsMissingGrantAsSuccessfulUnpublish(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID:               "event-2",
		EventType:        "bulletin.unpublish.revoke_asset",
		AggregateID:      "issue-1",
		AggregateVersion: 4,
		Payload:          []byte(`{"issueId":"issue-1","locale":"zh-Hant","assetId":"asset-1","grantId":"grant-1","aggregateVersion":4}`),
		Attempts:         1,
	}}
	assets := &workerAssets{revokeError: ErrGrantNotFound}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.completeCount != 1 {
		t.Fatalf("complete=%d", repository.completeCount)
	}
}

func TestWorkerRevokesGrantWhenPublishWasSuperseded(t *testing.T) {
	repository := &workerRepository{event: publishEvent(1), completePublishError: ErrStalePublication}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-1",
		OwnerService:     "hhc-web-api",
		UploadStatus:     "completed",
		ScanStatus:       "clean",
		ProcessingStatus: "ready",
	}, grant: Grant{ID: "grant-1"}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.revokedGrant != "grant-1" || repository.failCount != 1 || repository.retryCount != 0 {
		t.Fatalf("revoked=%q fail=%d retry=%d", assets.revokedGrant, repository.failCount, repository.retryCount)
	}
}

func publishEvent(attempts int) Event {
	return Event{
		ID:               "event-1",
		EventType:        "bulletin.publish.ensure_asset",
		AggregateID:      "issue-1",
		AggregateVersion: 3,
		Payload:          []byte(`{"workflowId":"workflow-1","issueId":"issue-1","locale":"zh-Hant","assetId":"asset-1","aggregateVersion":3}`),
		Attempts:         attempts,
	}
}

type workerRepository struct {
	event                Event
	claimed              bool
	retryCount           int
	failCount            int
	completeCount        int
	completedGrant       string
	completePublishError error
}

func (r *workerRepository) Claim(context.Context, time.Time, time.Duration) (Event, bool, error) {
	if r.claimed {
		return Event{}, false, nil
	}
	r.claimed = true
	return r.event, true, nil
}
func (r *workerRepository) Retry(context.Context, string, string, time.Time, time.Time) error {
	r.retryCount++
	return nil
}
func (r *workerRepository) Fail(context.Context, Event, string, time.Time) error {
	r.failCount++
	return nil
}
func (r *workerRepository) CompletePublish(_ context.Context, _ Event, grantID, _ string, _ time.Time) error {
	r.completedGrant = grantID
	return r.completePublishError
}
func (r *workerRepository) Complete(context.Context, string, time.Time) error {
	r.completeCount++
	return nil
}

type workerAssets struct {
	asset        Asset
	grant        Grant
	getError     error
	grantError   error
	revokeError  error
	revokedGrant string
}

func (a *workerAssets) Get(context.Context, string) (Asset, error) {
	return a.asset, a.getError
}
func (a *workerAssets) CreatePublicGrant(context.Context, string, string) (Grant, error) {
	return a.grant, a.grantError
}
func (a *workerAssets) RevokeGrant(_ context.Context, _, grantID string) error {
	a.revokedGrant = grantID
	return a.revokeError
}
func (a *workerAssets) PublicURL(string) string {
	return "https://www.alive.org.tw/api/assets/public/asset-1"
}

var _ Repository = (*workerRepository)(nil)
var _ AssetClient = (*workerAssets)(nil)
