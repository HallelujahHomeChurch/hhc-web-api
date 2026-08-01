package publication

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWorkerCompletesPublishForCleanAsset(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	repository := &workerRepository{event: publishEvent(1)}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-1",
		OwnerService:     "hhc-web-api",
		Namespace:        "cms.weekly.pdf",
		OwnerType:        "bulletin_issue",
		OwnerID:          "issue-1",
		Locale:           "zh-Hant",
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

func TestWorkerFailsClosedForInvalidAssetStates(t *testing.T) {
	for _, test := range []struct {
		name       string
		upload     string
		scan       string
		processing string
	}{
		{name: "failed upload", upload: "failed", scan: "pending", processing: "pending"},
		{name: "unknown scan", upload: "completed", scan: "unknown", processing: "pending"},
		{name: "failed processing", upload: "completed", scan: "clean", processing: "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &workerRepository{event: publishEvent(1)}
			assets := &workerAssets{asset: Asset{
				ID: "asset-1", OwnerService: "hhc-web-api", Namespace: "cms.weekly.pdf",
				OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant",
				UploadStatus: test.upload, ScanStatus: test.scan, ProcessingStatus: test.processing,
			}}
			worker := NewWorker(repository, assets, 5)
			if _, err := worker.processNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if repository.failCount != 1 || repository.deferCount != 0 {
				t.Fatalf("fail=%d defer=%d", repository.failCount, repository.deferCount)
			}
		})
	}
}

func TestWorkerDefersWhileAssetScanIsPendingWithoutConsumingRetry(t *testing.T) {
	repository := &workerRepository{event: publishEvent(20)}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-1",
		OwnerService:     "hhc-web-api",
		Namespace:        "cms.weekly.pdf",
		OwnerType:        "bulletin_issue",
		OwnerID:          "issue-1",
		Locale:           "zh-Hant",
		UploadStatus:     "completed",
		ScanStatus:       "pending",
		ProcessingStatus: "pending",
	}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.deferCount != 1 || repository.retryCount != 0 || repository.failCount != 0 {
		t.Fatalf("defer=%d retry=%d fail=%d", repository.deferCount, repository.retryCount, repository.failCount)
	}
}

func TestWorkerFailsWhenAssetWaitDeadlineExpires(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	event := publishEvent(1)
	event.CreatedAt = now.Add(-31 * time.Minute)
	repository := &workerRepository{event: event}
	assets := &workerAssets{asset: Asset{
		ID: "asset-1", OwnerService: "hhc-web-api", Namespace: "cms.weekly.pdf",
		OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant",
		UploadStatus: "completed", ScanStatus: "pending", ProcessingStatus: "pending",
	}}
	worker := NewWorker(repository, assets, 5)
	worker.now = func() time.Time { return now }

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.failCount != 1 || repository.deferCount != 0 {
		t.Fatalf("fail=%d defer=%d", repository.failCount, repository.deferCount)
	}
}

func TestWorkerFailsImmediatelyForInfectedAsset(t *testing.T) {
	repository := &workerRepository{event: publishEvent(1)}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-1",
		OwnerService:     "hhc-web-api",
		Namespace:        "cms.weekly.pdf",
		OwnerType:        "bulletin_issue",
		OwnerID:          "issue-1",
		Locale:           "zh-Hant",
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
	if assets.deletedAsset != "" {
		t.Fatalf("unpublish deleted asset=%q", assets.deletedAsset)
	}
}

func TestWorkerRejectsBulletinAssetFromAnotherOwner(t *testing.T) {
	repository := &workerRepository{event: publishEvent(1)}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-1",
		OwnerService:     "hhc-web-api",
		Namespace:        "cms.news.cover",
		OwnerType:        "news",
		OwnerID:          "news-1",
		UploadStatus:     "completed",
		ScanStatus:       "clean",
		ProcessingStatus: "ready",
	}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.failCount != 1 || assets.grantCalls != 0 {
		t.Fatalf("fail=%d grantCalls=%d", repository.failCount, assets.grantCalls)
	}
}

func TestWorkerPublishesOwnedNewsCover(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID:               "event-news",
		EventType:        "news.publish.ensure_asset",
		AggregateID:      "news-1",
		AggregateVersion: 3,
		Payload:          []byte(`{"contentId":"news-1","assetId":"asset-news","aggregateVersion":3}`),
		Attempts:         1,
	}}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-news",
		Namespace:        "cms.news.cover",
		OwnerService:     "hhc-web-api",
		OwnerType:        "news",
		OwnerID:          "news-1",
		UploadStatus:     "completed",
		ScanStatus:       "clean",
		ProcessingStatus: "ready",
	}, grant: Grant{ID: "grant-news"}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.completedContentGrant != "grant-news" || assets.grantCalls != 1 {
		t.Fatalf("grant=%q calls=%d", repository.completedContentGrant, assets.grantCalls)
	}
}

func TestWorkerUnpublishesNewsWithoutDeletingCover(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID:               "event-news-unpublish",
		EventType:        "news.unpublish.revoke_asset",
		AggregateID:      "news-1",
		AggregateVersion: 4,
		Payload:          []byte(`{"contentId":"news-1","assetId":"asset-news","grantId":"grant-news","aggregateVersion":4}`),
		Attempts:         1,
	}}
	assets := &workerAssets{}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.revokedGrant != "grant-news" || assets.deletedAsset != "" || repository.completeContentUnpublishCount != 1 {
		t.Fatalf("revoked=%q deleted=%q completed=%d", assets.revokedGrant, assets.deletedAsset, repository.completeContentUnpublishCount)
	}
}

func TestWorkerRetiresReplacedAsset(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID:          "event-retire",
		EventType:   "bulletin.asset.retire",
		AggregateID: "issue-1",
		Payload:     []byte(`{"issueId":"issue-1","locale":"zh-Hant","assetId":"asset-old","grantId":"grant-old","aggregateVersion":4}`),
		Attempts:    1,
	}}
	assets := &workerAssets{}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.revokedGrant != "grant-old" || assets.deletedAsset != "asset-old" || repository.completeCount != 1 {
		t.Fatalf("revoked=%q deleted=%q complete=%d", assets.revokedGrant, assets.deletedAsset, repository.completeCount)
	}
}

func TestWorkerQueuesGrantRevocationWhenPublishWasSuperseded(t *testing.T) {
	repository := &workerRepository{event: publishEvent(1), completePublishError: ErrStalePublication}
	assets := &workerAssets{asset: Asset{
		ID:               "asset-1",
		OwnerService:     "hhc-web-api",
		Namespace:        "cms.weekly.pdf",
		OwnerType:        "bulletin_issue",
		OwnerID:          "issue-1",
		Locale:           "zh-Hant",
		UploadStatus:     "completed",
		ScanStatus:       "clean",
		ProcessingStatus: "ready",
	}, grant: Grant{ID: "grant-1"}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.revokedGrant != "" || repository.compensationGrant != "grant-1" || repository.failCount != 1 || repository.retryCount != 0 {
		t.Fatalf("revoked=%q compensation=%q fail=%d retry=%d", assets.revokedGrant, repository.compensationGrant, repository.failCount, repository.retryCount)
	}
}

func TestWorkerQueuesGrantRevocationBeforeFinalPublishFailure(t *testing.T) {
	repository := &workerRepository{event: publishEvent(5), completePublishError: errors.New("database unavailable")}
	assets := &workerAssets{asset: readyBulletinAsset(), grant: Grant{ID: "grant-1"}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.revokedGrant != "" || repository.compensationGrant != "grant-1" || repository.failCount != 1 || repository.retryCount != 0 {
		t.Fatalf("revoked=%q compensation=%q fail=%d retry=%d", assets.revokedGrant, repository.compensationGrant, repository.failCount, repository.retryCount)
	}
}

func TestWorkerRetriesFailedCompensationPastMaxAttempts(t *testing.T) {
	repository := &workerRepository{
		event: publishEvent(5), completePublishError: errors.New("database unavailable"),
		failPublishError: errors.New("database unavailable"),
	}
	assets := &workerAssets{asset: readyBulletinAsset(), grant: Grant{ID: "grant-1"}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.retryCount != 1 || repository.failCount != 0 {
		t.Fatalf("retry=%d fail=%d", repository.retryCount, repository.failCount)
	}
}

func TestWorkerRetriesAmbiguousGrantCreationPastMaxAttempts(t *testing.T) {
	repository := &workerRepository{event: publishEvent(5)}
	assets := &workerAssets{asset: readyBulletinAsset(), grantError: errors.New("response unavailable")}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.retryCount != 1 || repository.failCount != 0 {
		t.Fatalf("retry=%d fail=%d", repository.retryCount, repository.failCount)
	}
}

func TestWorkerDoesNotRevokeDeliveredPublishAfterAmbiguousError(t *testing.T) {
	repository := &workerRepository{
		event: publishEvent(5), completePublishError: errors.New("commit result unavailable"),
		eventDelivered: true,
	}
	assets := &workerAssets{asset: readyBulletinAsset(), grant: Grant{ID: "grant-1"}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.revokedGrant != "" || repository.failCount != 0 || repository.retryCount != 0 {
		t.Fatalf("revoked=%q fail=%d retry=%d", assets.revokedGrant, repository.failCount, repository.retryCount)
	}
}

func TestWorkerRetriesUnpublishRevocationPastMaxAttempts(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-unpublish", EventType: "bulletin.unpublish.revoke_asset", Attempts: 5,
		Payload: []byte(`{"issueId":"issue-1","locale":"zh-Hant","assetId":"asset-1","grantId":"grant-1","aggregateVersion":4}`),
	}}
	assets := &workerAssets{revokeError: errors.New("asset unavailable")}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.retryCount != 1 || repository.failCount != 0 {
		t.Fatalf("retry=%d fail=%d", repository.retryCount, repository.failCount)
	}
}

func readyBulletinAsset() Asset {
	return Asset{
		ID: "asset-1", OwnerService: "hhc-web-api", Namespace: "cms.weekly.pdf",
		OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant",
		UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready",
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
		CreatedAt:        time.Date(2026, 8, 1, 11, 55, 0, 0, time.UTC),
	}
}

type workerRepository struct {
	event                         Event
	claimed                       bool
	retryCount                    int
	deferCount                    int
	failCount                     int
	completeCount                 int
	completeUnpublishCount        int
	completedGrant                string
	completedContentGrant         string
	completePublishError          error
	completeContentError          error
	completeContentUnpublishCount int
	eventDelivered                bool
	eventDeliveredError           error
	compensationGrant             string
	failPublishError              error
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
func (r *workerRepository) Defer(context.Context, string, string, time.Time, time.Time) error {
	r.deferCount++
	return nil
}
func (r *workerRepository) Fail(context.Context, Event, string, time.Time) error {
	r.failCount++
	return nil
}
func (r *workerRepository) FailPublish(_ context.Context, _ Event, _, grantID, _ string, _ time.Time) error {
	if r.failPublishError != nil {
		return r.failPublishError
	}
	r.failCount++
	r.compensationGrant = grantID
	return nil
}
func (r *workerRepository) EventDelivered(context.Context, string) (bool, error) {
	return r.eventDelivered, r.eventDeliveredError
}
func (r *workerRepository) CompletePublish(_ context.Context, _ Event, grantID, _ string, _ time.Time) error {
	r.completedGrant = grantID
	return r.completePublishError
}
func (r *workerRepository) CompleteContentPublish(_ context.Context, _ Event, grantID, _ string, _ time.Time) error {
	r.completedContentGrant = grantID
	return r.completeContentError
}
func (r *workerRepository) CompleteContentUnpublish(context.Context, Event, time.Time) error {
	r.completeContentUnpublishCount++
	return nil
}
func (r *workerRepository) Complete(context.Context, string, time.Time) error {
	r.completeCount++
	return nil
}
func (r *workerRepository) CompleteUnpublish(context.Context, Event, time.Time) error {
	r.completeCount++
	r.completeUnpublishCount++
	return nil
}

type workerAssets struct {
	asset        Asset
	grant        Grant
	getError     error
	grantError   error
	revokeError  error
	revokedGrant string
	deletedAsset string
	grantCalls   int
}

func (a *workerAssets) Get(context.Context, string) (Asset, error) {
	return a.asset, a.getError
}
func (a *workerAssets) CreatePublicGrant(context.Context, string, string) (Grant, error) {
	a.grantCalls++
	return a.grant, a.grantError
}
func (a *workerAssets) RevokeGrant(_ context.Context, _, grantID string) error {
	a.revokedGrant = grantID
	return a.revokeError
}
func (a *workerAssets) Delete(_ context.Context, assetID string) error {
	a.deletedAsset = assetID
	return nil
}
func (a *workerAssets) PublicURL(string) string {
	return "https://www.alive.org.tw/api/assets/public/asset-1"
}

var _ Repository = (*workerRepository)(nil)
var _ AssetClient = (*workerAssets)(nil)
