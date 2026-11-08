package publication

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
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
	}}
	worker := NewWorker(repository, assets, 5)
	worker.now = func() time.Time { return now }

	processed, err := worker.processNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !processed || repository.completedGrant != "" || assets.grantCalls != 0 {
		t.Fatalf("processed=%v completedGrant=%q grants=%d", processed, repository.completedGrant, assets.grantCalls)
	}
	if repository.retryCount != 0 || repository.failCount != 0 {
		t.Fatalf("retry=%d fail=%d", repository.retryCount, repository.failCount)
	}
}

type auditAppenderStub struct{ result auditclient.AppendResult }

func (s auditAppenderStub) Append(context.Context, []byte) auditclient.AppendResult { return s.result }
func TestWorkerDispatchesAuditOutbox(t *testing.T) {
	event, err := auditclient.NewEvent("cms.bulletin.create", "018f47a1-0f8a-4f25-8f09-111111111111", "018f47a1-0f8a-4f25-8f09-222222222222", "request-1", time.Now(), auditclient.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	payload, _, _ := event.Canonical()
	repo := &workerRepository{event: Event{ID: "event-1", EventType: "audit.append", Payload: payload, Attempts: 1}}
	worker := NewWorker(repo, &workerAssets{}, 20)
	worker.WithAudit(auditAppenderStub{auditclient.AppendResult{Result: "accepted", HTTPStatus: 201}}, nil, nil)
	if processed, err := worker.processNext(context.Background()); err != nil || !processed || repo.completeCount != 1 {
		t.Fatalf("processed=%t complete=%d err=%v", processed, repo.completeCount, err)
	}
}

func TestWorkerPublishesProtectedBulletinWithoutAnyGrant(t *testing.T) {
	now := time.Date(2026, 9, 11, 2, 0, 0, 0, time.UTC)
	repository := &workerRepository{event: publishEvent(1)}
	assets := &workerAssets{asset: Asset{
		ID: "asset-1", OwnerService: "hhc-web-api", Namespace: "cms.weekly.pdf", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant",
		UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready",
	}}
	worker := NewWorker(repository, assets, 5)
	worker.now = func() time.Time { return now }

	processed, err := worker.processNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !processed || assets.grantCalls != 0 || repository.completedGrant != "" {
		t.Fatalf("processed=%v grants=%d completed=%q", processed, assets.grantCalls, repository.completedGrant)
	}
}

func TestWorkerUnpublishesMemberBulletinWithoutPublicGrant(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-1", EventType: "bulletin.unpublish.revoke_asset", AggregateID: "issue-1", Attempts: 1,
		Payload: []byte(`{"issueId":"issue-1","locale":"zh-Hant","assetId":"asset-1","aggregateVersion":3}`),
	}}
	assets := &workerAssets{}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.completeUnpublishCount != 1 || assets.revokedGrant != "" {
		t.Fatalf("complete=%d revoked=%q", repository.completeUnpublishCount, assets.revokedGrant)
	}
}

func TestWorkerFailsUnsupportedBulletinEditionBeforeAssetSideEffects(t *testing.T) {
	for _, eventType := range []string{"bulletin.publish.ensure_asset", "bulletin.asset.retire"} {
		t.Run(eventType, func(t *testing.T) {
			repository := &workerRepository{event: Event{
				ID: "event-1", EventType: eventType, AggregateID: "issue-1", Attempts: 1,
				Payload: []byte(`{"issueId":"issue-1","locale":"ja","assetId":"asset-1","grantId":"grant-1","aggregateVersion":3}`),
			}}
			assets := &workerAssets{}
			worker := NewWorker(repository, assets, 5)

			if _, err := worker.processNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if repository.failCount != 1 || assets.getCalls != 0 || assets.grantCalls != 0 || assets.revokedGrant != "" || assets.deletedAsset != "" {
				t.Fatalf("fail=%d get=%d grant=%d revoked=%q deleted=%q", repository.failCount, assets.getCalls, assets.grantCalls, assets.revokedGrant, assets.deletedAsset)
			}
		})
	}
}

func TestWorkerAllowsUnsupportedLegacyEditionUnpublishCleanup(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-1", EventType: "bulletin.unpublish.revoke_asset", AggregateID: "issue-1", Attempts: 1,
		Payload: []byte(`{"issueId":"issue-1","locale":"ja","assetId":"asset-1","grantId":"grant-1","aggregateVersion":3}`),
	}}
	assets := &workerAssets{}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.completeUnpublishCount != 1 || assets.revokedGrant != "grant-1" || assets.deletedAsset != "" {
		t.Fatalf("complete=%d revoked=%q deleted=%q", repository.completeUnpublishCount, assets.revokedGrant, assets.deletedAsset)
	}
}

func TestWorkerQueuesBulletinNotification(t *testing.T) {
	payload := `{"issueId":"issue-1","actorId":"user-1","name":"Weekly bulletin 1732","translations":{"zh-Hant":{"subject":"第 1732 期週報已發布","body":"本週週報","clickBehavior":"url","actionUrl":"/zh-Hant/literature-ministry"}}}`
	repository := &workerRepository{event: Event{ID: "event-notification", EventType: "bulletin.notification.queue", AggregateID: "issue-1", Payload: []byte(payload), Attempts: 1}}
	notifications := &workerNotifications{}
	worker := NewWorker(repository, &workerAssets{}, 5, notifications)

	processed, err := worker.processNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !processed || notifications.payload.IssueID != "issue-1" || repository.completeNotificationCount != 1 {
		t.Fatalf("processed=%v payload=%#v completed=%d", processed, notifications.payload, repository.completeNotificationCount)
	}
}

func TestWorkerSubmitsStoredNotificationWithoutSourceLookup(t *testing.T) {
	payload := `{"name":"Weekly bulletin 1732","source":{"kind":"bulletin","issueId":"issue-1","notificationGeneration":0},"notifications":[{"channel":"email","audienceType":"bulletin_members","translations":{"zh-Hant":{"subject":"第 1732 期週報已發布","body":"本週週報","clickBehavior":"url","actionUrl":"/zh-Hant/literature-ministry"}},"requiredEntitlements":{"zh-Hant":"bulletin.general.zh-Hant.access"}},{"channel":"web_push","audienceType":"bulletin_members","translations":{"zh-Hant":{"subject":"第 1732 期週報已發布","body":"本週週報","clickBehavior":"url","actionUrl":"/zh-Hant/literature-ministry"}},"requiredEntitlements":{"zh-Hant":"bulletin.general.zh-Hant.access"}}],"actorId":"11111111-1111-4111-8111-111111111111","requestId":"22222222-2222-4222-8222-222222222222","idempotencyKey":"notification:v2:bulletin:issue-1"}`
	repository := &workerRepository{event: Event{ID: "event-notification", EventType: "bulletin.notification.submit", AggregateID: "deleted-issue", Payload: []byte(payload), Attempts: 1}}
	notifications := &workerNotifications{}
	worker := NewWorker(repository, &workerAssets{}, 5, notifications)

	processed, err := worker.processNext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !processed || repository.completeCount != 1 || notifications.submission.IdempotencyKey != "notification:v2:bulletin:issue-1" || notifications.submission.Source == nil || notifications.submission.Source.IssueID != "issue-1" || len(notifications.submission.Notifications) != 2 || notifications.submission.Notifications[0].Translations["zh-Hant"].Subject != "第 1732 期週報已發布" {
		t.Fatalf("processed=%v completed=%d submission=%#v", processed, repository.completeCount, notifications.submission)
	}
}

func TestWorkerRetriesTransientBulletinNotificationFailure(t *testing.T) {
	repository := &workerRepository{event: Event{ID: "event-notification", EventType: "bulletin.notification.queue", Payload: []byte(`{"issueId":"issue-1","actorId":"user-1","name":"Weekly bulletin 1732","translations":{"en":{"subject":"Weekly bulletin 1732 is now available","body":"Weekly","clickBehavior":"url","actionUrl":"/en/literature-ministry"}}}`), Attempts: 1}}
	worker := NewWorker(repository, &workerAssets{}, 5, &workerNotifications{err: errors.New("timeout")})

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.retryCount != 1 || repository.failCount != 0 {
		t.Fatalf("retry=%d fail=%d", repository.retryCount, repository.failCount)
	}
}

func TestWorkerFailsInvalidBulletinNotificationPayload(t *testing.T) {
	repository := &workerRepository{event: Event{ID: "event-notification", EventType: "bulletin.notification.queue", Payload: []byte(`{"issueId":"issue-1"}`), Attempts: 1}}
	worker := NewWorker(repository, &workerAssets{}, 5, &workerNotifications{})

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.failCount != 1 || repository.retryCount != 0 {
		t.Fatalf("fail=%d retry=%d", repository.failCount, repository.retryCount)
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
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
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
	worker.now = func() time.Time { return now }

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

func TestWorkerPublishesExactReadyHomeBanner(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-home", EventType: "home.publish.ensure_asset", AggregateID: "page-home", AggregateVersion: 3,
		Payload: []byte(`{"contentId":"page-home","assetId":"banner-1","manifestSha256":"manifest","aggregateVersion":3}`), Attempts: 1,
	}}
	assets := &workerAssets{asset: readyHomeBanner(), grant: Grant{ID: "grant-home"}}
	worker := NewWorker(repository, assets, 5)
	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.completedContentAssets) != 1 || repository.completedContentAssets[0].Usage != "banner" || repository.completedContentAssets[0].GrantID != "grant-home" || !strings.Contains(string(repository.completedContentEvent.Payload), `"manifestSha256":"manifest"`) {
		t.Fatalf("completed=%#v", repository.completedContentAssets)
	}
}

func TestWorkerRejectsHomeBannerContractDrift(t *testing.T) {
	for _, mutate := range []func(*Asset){
		func(asset *Asset) { asset.Namespace = "cms.news.cover" },
		func(asset *Asset) { asset.OwnerID = "other-page" },
		func(asset *Asset) { asset.Purpose = "news_home_cover" },
		func(asset *Asset) { asset.DetectedMIMEType = "image/png" },
		func(asset *Asset) { asset.ProcessingStatus = "ready" },
	} {
		asset := readyHomeBanner()
		mutate(&asset)
		repository := &workerRepository{event: Event{
			ID: "event-home", EventType: "home.publish.ensure_asset", AggregateID: "page-home", AggregateVersion: 3,
			Payload: []byte(`{"contentId":"page-home","assetId":"banner-1","aggregateVersion":3}`), Attempts: 1,
		}}
		assets := &workerAssets{asset: asset, grant: Grant{ID: "grant-home"}}
		if _, err := NewWorker(repository, assets, 5).processNext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if repository.failCount != 1 || assets.grantCalls != 0 {
			t.Fatalf("asset=%#v fail=%d grants=%d", asset, repository.failCount, assets.grantCalls)
		}
	}
}

func TestWorkerCompensatesSupersededHomeBannerGrant(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-home", EventType: "home.publish.ensure_asset", AggregateID: "page-home", AggregateVersion: 3,
		Payload: []byte(`{"contentId":"page-home","assetId":"banner-1","manifestSha256":"manifest","aggregateVersion":3}`), Attempts: 1,
	}, completeContentError: ErrStalePublication}
	assets := &workerAssets{asset: readyHomeBanner(), grant: Grant{ID: "grant-home"}}
	if _, err := NewWorker(repository, assets, 5).processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repository.failCount != 1 || len(repository.compensationAssets) != 1 || repository.compensationAssets[0].GrantID != "grant-home" || repository.retryCount != 0 || !strings.Contains(string(repository.failedContentEvent.Payload), `"manifestSha256":"manifest"`) {
		t.Fatalf("fail=%d compensation=%#v retry=%d", repository.failCount, repository.compensationAssets, repository.retryCount)
	}
}

func TestWorkerPublishesNewsWithoutImages(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-news", EventType: "news.publish.ensure_asset", AggregateID: "news-1", AggregateVersion: 3,
		Payload: []byte(`{"contentId":"news-1","aggregateVersion":3}`), Attempts: 1,
	}}
	assets := &workerAssets{}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.grantCalls != 0 || len(repository.completedContentAssets) != 0 {
		t.Fatalf("grantCalls=%d completed=%#v", assets.grantCalls, repository.completedContentAssets)
	}
}

func TestWorkerPublishesNewsDetailAndHomeImages(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-news", EventType: "news.publish.ensure_asset", AggregateID: "news-1", AggregateVersion: 3,
		Payload: []byte(`{"contentId":"news-1","assetId":"asset-detail","homeAssetId":"asset-home","aggregateVersion":3}`), Attempts: 1,
	}}
	assets := &workerAssets{
		assets: map[string]Asset{
			"asset-detail": readyNewsAsset("asset-detail", "news_detail_cover"),
			"asset-home":   readyNewsAsset("asset-home", "news_home_cover"),
		},
		grants: map[string]Grant{"asset-detail": {ID: "grant-detail"}, "asset-home": {ID: "grant-home"}},
	}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.completedContentAssets) != 2 || repository.completedContentAssets[0].Usage != "detail" || repository.completedContentAssets[1].Usage != "home" {
		t.Fatalf("completed=%#v", repository.completedContentAssets)
	}
}

func TestWorkerPublishesMultipleStatementInlineImages(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-statement", EventType: "news.publish.ensure_asset", AggregateID: "news-1", AggregateVersion: 3,
		Payload: []byte(`{"contentId":"news-1","inlineAssetIds":["inline-1","inline-2"],"aggregateVersion":3}`), Attempts: 1,
	}}
	assets := &workerAssets{
		assets: map[string]Asset{"inline-1": readyNewsAsset("inline-1", "statement_inline"), "inline-2": readyNewsAsset("inline-2", "statement_inline")},
		grants: map[string]Grant{"inline-1": {ID: "grant-1"}, "inline-2": {ID: "grant-2"}},
	}
	if _, err := NewWorker(repository, assets, 5).processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.completedContentAssets) != 2 || repository.completedContentAssets[0].Usage != "inline" || repository.completedContentAssets[1].AssetID != "inline-2" || assets.grantCalls != 2 {
		t.Fatalf("completed=%#v grants=%d", repository.completedContentAssets, assets.grantCalls)
	}
}

func TestWorkerCompensatesAllNewsGrantsWhenCompletionFails(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-news", EventType: "news.publish.ensure_asset", AggregateID: "news-1", AggregateVersion: 3,
		Payload: []byte(`{"contentId":"news-1","assetId":"asset-detail","homeAssetId":"asset-home","aggregateVersion":3}`), Attempts: 5,
	}, completeContentError: errors.New("database unavailable")}
	assets := &workerAssets{
		assets: map[string]Asset{
			"asset-detail": readyNewsAsset("asset-detail", "news_detail_cover"),
			"asset-home":   readyNewsAsset("asset-home", "news_home_cover"),
		},
		grants: map[string]Grant{"asset-detail": {ID: "grant-detail"}, "asset-home": {ID: "grant-home"}},
	}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.compensationAssets) != 2 || repository.failCount != 1 {
		t.Fatalf("compensation=%#v fail=%d", repository.compensationAssets, repository.failCount)
	}
}

func TestWorkerCompensatesCreatedNewsGrantWhenNextGrantFails(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-news", EventType: "news.publish.ensure_asset", AggregateID: "news-1", AggregateVersion: 3,
		Payload: []byte(`{"contentId":"news-1","assetId":"asset-detail","homeAssetId":"asset-home","aggregateVersion":3}`), Attempts: 5,
	}}
	assets := &workerAssets{
		assets: map[string]Asset{
			"asset-detail": readyNewsAsset("asset-detail", "news_detail_cover"),
			"asset-home":   readyNewsAsset("asset-home", "news_home_cover"),
		},
		grants:      map[string]Grant{"asset-detail": {ID: "grant-detail"}},
		grantErrors: map[string]error{"asset-home": errors.New("grant unavailable")},
	}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.compensationAssets) != 1 || repository.compensationAssets[0].GrantID != "grant-detail" || repository.failCount != 1 {
		t.Fatalf("compensation=%#v fail=%d", repository.compensationAssets, repository.failCount)
	}
}

func TestWorkerUnpublishesNewsWithoutDeletingCover(t *testing.T) {
	actorID := "11111111-1111-4111-8111-111111111111"
	repository := &workerRepository{event: Event{
		ID:               "22222222-2222-4222-8222-222222222222",
		EventType:        "news.unpublish.revoke_asset",
		AggregateID:      "news-1",
		AggregateVersion: 4,
		ActorID:          actorID,
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
	if assets.revokeActorID != actorID || assets.revokeRequestID != repository.event.ID {
		t.Fatalf("revoke provenance actor=%q request=%q", assets.revokeActorID, assets.revokeRequestID)
	}
}

func TestWorkerUnpublishesBothNewsImages(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-news-unpublish", EventType: "news.unpublish.revoke_asset", AggregateID: "news-1", AggregateVersion: 4, ActorID: "11111111-1111-4111-8111-111111111111",
		Payload: []byte(`{"contentId":"news-1","assets":[{"usage":"detail","assetId":"asset-detail","grantId":"grant-detail"},{"usage":"home","assetId":"asset-home","grantId":"grant-home"}],"aggregateVersion":4}`), Attempts: 1,
	}}
	assets := &workerAssets{}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(assets.revokedGrants) != 2 || assets.revokedGrants[0] != "grant-detail" || assets.revokedGrants[1] != "grant-home" {
		t.Fatalf("revoked=%#v", assets.revokedGrants)
	}
}

func TestWorkerDoesNotRevokeStaleNewsUnpublish(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID: "event-stale-unpublish", EventType: "news.unpublish.revoke_asset", AggregateID: "news-1", AggregateVersion: 4,
		Payload: []byte(`{"contentId":"news-1","assetId":"asset-news","grantId":"grant-news","aggregateVersion":4}`), Attempts: 1,
	}}
	assets := &workerAssets{}
	worker := NewWorker(repository, assets, 5)
	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.revokedGrant != "" || repository.failCount != 1 {
		t.Fatalf("revoked=%q failed=%d", assets.revokedGrant, repository.failCount)
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

func TestWorkerDeletesOwnedAssetAfterContentDeletion(t *testing.T) {
	repository := &workerRepository{event: Event{
		ID:          "event-delete",
		EventType:   "asset.owner.delete",
		AggregateID: "content-1",
		Payload:     []byte(`{"assetId":"asset-old"}`),
		Attempts:    1,
	}}
	assets := &workerAssets{}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.deletedAsset != "asset-old" || repository.completeCount != 1 {
		t.Fatalf("deleted=%q complete=%d", assets.deletedAsset, repository.completeCount)
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
	}}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.revokedGrant != "" || repository.compensationGrant != "" || repository.failCount != 1 || repository.retryCount != 0 {
		t.Fatalf("revoked=%q compensation=%q fail=%d retry=%d", assets.revokedGrant, repository.compensationGrant, repository.failCount, repository.retryCount)
	}
}

func TestWorkerQueuesGrantRevocationBeforeFinalPublishFailure(t *testing.T) {
	repository := &workerRepository{event: publishEvent(5), completePublishError: errors.New("database unavailable")}
	assets := &workerAssets{asset: readyBulletinAsset()}
	worker := NewWorker(repository, assets, 5)

	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if assets.revokedGrant != "" || repository.compensationGrant != "" || repository.failCount != 1 || repository.retryCount != 0 {
		t.Fatalf("revoked=%q compensation=%q fail=%d retry=%d", assets.revokedGrant, repository.compensationGrant, repository.failCount, repository.retryCount)
	}
}

func TestWorkerRetriesFailedCompensationPastMaxAttempts(t *testing.T) {
	repository := &workerRepository{
		event: publishEvent(5), completePublishError: errors.New("database unavailable"),
		failPublishError: errors.New("database unavailable"),
	}
	assets := &workerAssets{asset: readyBulletinAsset()}
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
	assets := &workerAssets{asset: readyBulletinAsset()}
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

func readyNewsAsset(id, purpose string) Asset {
	return Asset{
		ID: id, OwnerService: "hhc-web-api", Namespace: "cms.news.cover", OwnerType: "news", OwnerID: "news-1", Purpose: purpose,
		UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready",
	}
}

func readyHomeBanner() Asset {
	return Asset{
		ID: "banner-1", OwnerService: "hhc-web-api", Namespace: "cms.home.banner", OwnerType: "page", OwnerID: "page-home", Purpose: "home_banner",
		UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "not_required", DetectedMIMEType: "image/jpeg",
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
	statementBatch     content.StatementNotificationBatch
	cancelAfterCreate  bool
	statementCompleted int

	event                         Event
	claimed                       bool
	retryCount                    int
	deferCount                    int
	failCount                     int
	completeCount                 int
	completeUnpublishCount        int
	completedGrant                string
	completedContentGrant         string
	completedContentAssets        []PublishedAsset
	completedContentEvent         Event
	completePublishError          error
	completeContentError          error
	completeContentUnpublishCount int
	eventDelivered                bool
	eventDeliveredError           error
	compensationGrant             string
	compensationAssets            []PublishedAsset
	failedContentEvent            Event
	failPublishError              error
	completeNotificationCount     int
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
func (r *workerRepository) FailContentPublish(_ context.Context, event Event, assets []PublishedAsset, _ string, _ time.Time) error {
	if r.failPublishError != nil {
		return r.failPublishError
	}
	r.failCount++
	r.failedContentEvent = event
	r.compensationAssets = append([]PublishedAsset(nil), assets...)
	return nil
}
func (r *workerRepository) EventDelivered(context.Context, string) (bool, error) {
	return r.eventDelivered, r.eventDeliveredError
}
func (r *workerRepository) CompletePublish(_ context.Context, _ Event, grantID, _ string, _ time.Time) error {
	r.completedGrant = grantID
	return r.completePublishError
}
func (r *workerRepository) CompleteContentPublish(_ context.Context, event Event, assets []PublishedAsset, _ time.Time) error {
	r.completedContentEvent = event
	r.completedContentAssets = append([]PublishedAsset(nil), assets...)
	if len(assets) > 0 {
		r.completedContentGrant = assets[0].GrantID
	}
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
func (r *workerRepository) CompleteNotification(context.Context, Event, time.Time) error {
	r.completeNotificationCount++
	return nil
}

type workerNotifications struct {
	created   int
	sent      int
	cancelled int
	requestID string

	payload    BulletinNotificationPayload
	submission NotificationSubmission
	err        error
}

func (n *workerNotifications) SubmitNotifications(_ context.Context, command NotificationSubmission) error {
	n.submission = command
	return n.err
}

func (n *workerNotifications) QueueBulletinNotification(_ context.Context, payload BulletinNotificationPayload) error {
	n.payload = payload
	return n.err
}

type workerAssets struct {
	asset           Asset
	assets          map[string]Asset
	grant           Grant
	grants          map[string]Grant
	grantErrors     map[string]error
	getError        error
	grantError      error
	revokeError     error
	revokedGrant    string
	revokedGrants   []string
	revokeActorID   string
	revokeRequestID string
	deletedAsset    string
	grantCalls      int
	getCalls        int
}

func (a *workerAssets) Get(_ context.Context, assetID string) (Asset, error) {
	a.getCalls++
	if a.assets != nil {
		return a.assets[assetID], a.getError
	}
	return a.asset, a.getError
}
func (a *workerAssets) CreatePublicGrant(_ context.Context, assetID, _ string) (Grant, error) {
	a.grantCalls++
	if err := a.grantErrors[assetID]; err != nil {
		return Grant{}, err
	}
	if a.grants != nil {
		return a.grants[assetID], a.grantError
	}
	return a.grant, a.grantError
}
func (a *workerAssets) RevokeGrant(ctx context.Context, _, grantID string) error {
	if provenance, ok := auditclient.ProvenanceFromContext(ctx); ok {
		a.revokeActorID, a.revokeRequestID = provenance.ActorID, provenance.RequestID
	}
	a.revokedGrant = grantID
	a.revokedGrants = append(a.revokedGrants, grantID)
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

func (r *workerRepository) StatementNotification(context.Context, string) (content.StatementNotificationBatch, error) {
	return r.statementBatch, nil
}
func (r *workerRepository) SetStatementCampaign(_ context.Context, _ string, id string) error {
	r.statementBatch.CampaignID = id
	if r.cancelAfterCreate {
		now := time.Now()
		r.statementBatch.CancelledAt = &now
	}
	return nil
}
func (r *workerRepository) CompleteStatementNotification(context.Context, Event, content.StatementNotificationBatch, time.Time) error {
	r.statementCompleted++
	return nil
}
func (n *workerNotifications) CreateStatementCampaign(context.Context, content.StatementNotificationBatch) (string, error) {
	n.created++
	return "campaign-1", n.err
}
func (n *workerNotifications) SendStatementCampaign(_ context.Context, _, requestID string) error {
	n.sent++
	n.requestID = requestID
	return n.err
}
func (n *workerNotifications) CancelStatementCampaign(_ context.Context, _, requestID string) error {
	n.cancelled++
	n.requestID = requestID
	return n.err
}

func TestStatementNotificationRecoveryAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name                             string
		campaignID                       string
		cancelDuringCreate               bool
		cancelled                        bool
		wantCreate, wantSend, wantCancel int
	}{
		{name: "new", wantCreate: 1, wantSend: 1},
		{name: "retry existing campaign", campaignID: "campaign-1", wantSend: 1},
		{name: "cancelled before create", cancelled: true, wantCreate: 1, wantCancel: 1},
		{name: "cancelled during create", cancelDuringCreate: true, wantCreate: 1, wantCancel: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &workerRepository{statementBatch: content.StatementNotificationBatch{ID: "batch-1", CampaignID: test.campaignID, RequestID: "request-1"}, cancelAfterCreate: test.cancelDuringCreate}
			if test.cancelled {
				now := time.Now()
				repo.statementBatch.CancelledAt = &now
			}
			notifications := &workerNotifications{}
			worker := NewWorker(repo, &workerAssets{}, 5, notifications)
			err := worker.notifyStatement(context.Background(), Event{Payload: []byte(`{"batchId":"batch-1"}`)}, time.Now())
			if err != nil || notifications.created != test.wantCreate || notifications.sent != test.wantSend || notifications.cancelled != test.wantCancel || notifications.requestID != "request-1" || repo.statementCompleted != 1 {
				t.Fatalf("error=%v create=%d send=%d cancel=%d complete=%d", err, notifications.created, notifications.sent, notifications.cancelled, repo.statementCompleted)
			}
		})
	}
}
func TestStatementNotificationRetriesProviderError(t *testing.T) {
	repo := &workerRepository{event: Event{ID: "event-1", EventType: "statement.notification.queue", Payload: []byte(`{"batchId":"batch-1"}`), Attempts: 1}, statementBatch: content.StatementNotificationBatch{ID: "batch-1", CampaignID: "campaign-1"}}
	worker := NewWorker(repo, &workerAssets{}, 5, &workerNotifications{err: errors.New("timeout")})
	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.retryCount != 1 || repo.statementCompleted != 0 {
		t.Fatalf("retry=%d complete=%d", repo.retryCount, repo.statementCompleted)
	}
}

func TestStatementCancellationRetriesPastAttemptLimit(t *testing.T) {
	now := time.Now()
	repo := &workerRepository{event: Event{ID: "event-1", EventType: "statement.notification.queue", Payload: []byte(`{"batchId":"batch-1"}`), Attempts: 8}, statementBatch: content.StatementNotificationBatch{ID: "batch-1", CampaignID: "campaign-1", CancelledAt: &now}}
	worker := NewWorker(repo, &workerAssets{}, 5, &workerNotifications{err: errors.New("timeout")})
	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.retryCount != 1 || repo.failCount != 0 {
		t.Fatalf("retry=%d failed=%d", repo.retryCount, repo.failCount)
	}
}

// Exercise the shared loop without real-time sleeps or a second cleanup worker.
func TestWorkerWatermarkCleanupSchedule(t *testing.T) {
	for _, test := range []struct {
		name        string
		removed     int64
		err         error
		before, due time.Duration
	}{
		{name: "evidence expiry", before: 59 * time.Second, due: time.Minute},
		{name: "backlog", removed: 1000, before: time.Second, due: 2 * time.Second},
		{name: "retry", err: errors.New("database unavailable"), before: 59 * time.Second, due: time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
			repo := &cleanupWorkerRepository{times: []time.Time{base, base.Add(test.before), base.Add(test.due)}, cancel: cancel, removed: test.removed, err: test.err}
			worker := NewWorker(repo, nil, 1)
			worker.now = func() time.Time { return repo.times[repo.step] }
			if err := worker.Run(ctx); err != nil {
				t.Fatal(err)
			}
			if repo.purges != 2 {
				t.Fatalf("cleanup calls=%d, want 2", repo.purges)
			}
		})
	}
}

type cleanupWorkerRepository struct {
	workerRepository
	times        []time.Time
	step, purges int
	cancel       context.CancelFunc
	removed      int64
	err          error
}

func (r *cleanupWorkerRepository) Claim(context.Context, time.Time, time.Duration) (Event, bool, error) {
	if r.step == len(r.times)-1 {
		r.cancel()
		return Event{}, false, nil
	}
	r.step++
	return Event{EventType: "unsupported-test-event"}, true, nil
}
func (r *cleanupWorkerRepository) PurgeWatermarkRecords(_ context.Context, _ time.Time, limit int) (int64, error) {
	if limit != 1000 {
		panic("unexpected cleanup batch size")
	}
	r.purges++
	if r.purges == 1 {
		return r.removed, r.err
	}
	return 0, nil
}
