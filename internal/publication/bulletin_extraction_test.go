package publication

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type extractionRequestRepository struct {
	*workerRepository
	checksum string
	err      error
}

func (r *extractionRequestRepository) CompleteOnlineExtractionRequest(_ context.Context, _ Event, checksum string, _ time.Time) error {
	r.checksum = checksum
	return r.err
}

func (r *extractionRequestRepository) RescheduleOnlineExtractionRequest(_ context.Context, _ Event, _ string, next, _ time.Time, waiting bool) error {
	if next.IsZero() {
		r.failCount++
	} else if waiting {
		r.deferCount++
	} else {
		r.retryCount++
	}
	return nil
}

func TestUploadExtractionStaleWorkerCannotRetryOrFailNewClaim(t *testing.T) {
	for _, attempts := range []int{1, 5} {
		repo := &extractionRequestRepository{workerRepository: &workerRepository{event: Event{ID: "event-1", EventType: "bulletin.online.ensure_extraction", AggregateID: "issue-1", Attempts: attempts, Payload: []byte(`{"issueId":"issue-1","assetId":"asset-1","actorId":"018f47a1-0f8a-4f25-8f09-111111111111","requestId":"upload-request-1"}`)}}, err: ErrStalePublication}
		asset := Asset{ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready", DetectedMIMEType: "application/pdf", ChecksumSHA256: strings.Repeat("a", 64)}
		if _, err := NewWorker(repo, &workerAssets{asset: asset}, 5).processNext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if repo.retryCount+repo.failCount+repo.deferCount != 0 {
			t.Fatal("stale claimant rescheduled new work", attempts)
		}
	}
}

func TestUploadExtractionWaitsForCleanOwnedPDF(t *testing.T) {
	for _, tc := range []struct {
		name, scan, owner, mime, checksum    string
		wantQueued, wantDeferred, wantFailed bool
	}{
		{"ready", "clean", "issue-1", "application/pdf", strings.Repeat("a", 64), true, false, false},
		{"scan-pending", "pending", "issue-1", "application/pdf", strings.Repeat("a", 64), false, true, false},
		{"infected", "infected", "issue-1", "application/pdf", strings.Repeat("a", 64), false, false, true},
		{"wrong-owner", "clean", "another-issue", "application/pdf", strings.Repeat("a", 64), false, false, true},
		{"not-pdf", "clean", "issue-1", "image/png", strings.Repeat("a", 64), false, false, true},
		{"missing-checksum", "clean", "issue-1", "application/pdf", "", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &extractionRequestRepository{workerRepository: &workerRepository{event: Event{
				ID: "event-1", EventType: "bulletin.online.ensure_extraction", AggregateID: "issue-1", Attempts: 1,
				Payload: []byte(`{"issueId":"issue-1","assetId":"asset-1","actorId":"018f47a1-0f8a-4f25-8f09-111111111111","requestId":"upload-request-1"}`),
			}}}
			asset := Asset{ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: tc.owner, Locale: "zh-Hant", UploadStatus: "completed", ScanStatus: tc.scan, ProcessingStatus: "ready", DetectedMIMEType: tc.mime, ChecksumSHA256: tc.checksum}
			worker := NewWorker(repo, &workerAssets{asset: asset}, 5)
			if processed, err := worker.processNext(context.Background()); err != nil || !processed {
				t.Fatal(processed, err)
			}
			if (repo.checksum != "") != tc.wantQueued || (repo.deferCount != 0) != tc.wantDeferred || (repo.failCount != 0) != tc.wantFailed {
				t.Fatalf("queued=%t deferred=%d failed=%d", repo.checksum != "", repo.deferCount, repo.failCount)
			}
		})
	}
}

func TestUploadExtractionRetriesDatabaseFailureWithoutPublishing(t *testing.T) {
	repo := &extractionRequestRepository{workerRepository: &workerRepository{event: Event{ID: "event-1", EventType: "bulletin.online.ensure_extraction", AggregateID: "issue-1", Attempts: 1, Payload: []byte(`{"issueId":"issue-1","assetId":"asset-1","actorId":"018f47a1-0f8a-4f25-8f09-111111111111","requestId":"upload-request-1"}`)}}, err: errors.New("database unavailable")}
	asset := Asset{ID: "asset-1", Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: "issue-1", Locale: "zh-Hant", UploadStatus: "completed", ScanStatus: "clean", ProcessingStatus: "ready", DetectedMIMEType: "application/pdf", ChecksumSHA256: strings.Repeat("a", 64)}
	worker := NewWorker(repo, &workerAssets{asset: asset}, 5)
	if _, err := worker.processNext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if repo.retryCount != 1 || repo.failCount != 0 || repo.completeCount != 0 || repo.completedGrant != "" {
		t.Fatal("automatic conversion published or lost retry")
	}
}

type extractionWakeSender struct {
	err    error
	called bool
}

func (s *extractionWakeSender) Enqueue(context.Context, []byte) error { s.called = true; return s.err }
func TestExtractionWakeOutboxRetainsDeliveryOnQueueFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		repository := &workerRepository{event: Event{ID: "event-wake", EventType: "bulletin.online.wake", Payload: []byte(`{"version":1}`), Attempts: 1}}
		worker := NewWorker(repository, nil, 8)
		sender := &extractionWakeSender{}
		if failed {
			sender.err = errors.New("queue unavailable")
		}
		worker.WithExtractionQueue(sender)
		_, err := worker.processNext(context.Background())
		if err != nil || !sender.called {
			t.Fatal(err)
		}
		if failed && (repository.completeCount != 0 || repository.retryCount != 1) {
			t.Fatal("failed delivery did not retain its retry")
		}
		if !failed && repository.completeCount != 1 {
			t.Fatal("successful delivery not acknowledged")
		}
	}
}

func TestExtractionWakeDeliverySurvivesOutboxAttemptLimit(t *testing.T) {
	for _, queue := range []MessageQueue{nil, &extractionWakeSender{err: errors.New("queue unavailable")}} {
		repository := &workerRepository{event: Event{ID: "event-wake", EventType: "bulletin.online.wake", Payload: []byte(`{"version":1}`), Attempts: 20}}
		worker := NewWorker(repository, nil, 8)
		worker.WithExtractionQueue(queue)
		if _, err := worker.processNext(context.Background()); err != nil {
			t.Fatal(err)
		}
		if repository.retryCount != 1 || repository.failCount != 0 || repository.completeCount != 0 {
			t.Fatalf("wake stranded after transport outage: retries=%d failed=%d complete=%d", repository.retryCount, repository.failCount, repository.completeCount)
		}
	}
}
