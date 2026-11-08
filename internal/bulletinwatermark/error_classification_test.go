package bulletinwatermark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/google/uuid"
)

func classificationJob() DownloadJob {
	sourceSum := sha256.Sum256([]byte("%PDF-source"))
	return DownloadJob{ID: uuid.NewString(), SourceSHA256: hex.EncodeToString(sourceSum[:]), FingerprintPayload: strings.Repeat("0", 61), LeaseToken: uuid.NewString(), Generation: 1, Attempts: 1}
}

func downloadMessage(id string, dequeueCount int64) *workQueueStub {
	return &workQueueStub{message: LeasedMessage{WorkMessage: WorkMessage{Kind: "download", ID: id}, MessageID: "m", PopReceipt: "p", DequeueCount: dequeueCount}}
}

func TestTerminalSourceFailuresFailJobAndAck(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		repo := &downloadWorkerRepo{job: classificationJob()}
		queue := downloadMessage(repo.job.ID, 1)
		processed, err := ProcessOne(context.Background(), queue, NewDownloadWorker(repo, downloadWorkerRenderer{t: t}, &downloadWorkerAssetStub{sourceStatus: status}, "p"))
		if err != nil || !processed || !queue.acked || queue.retryDelay != 0 || repo.failed != "source_unavailable" || repo.released != 0 {
			t.Fatalf("status %d: err=%v acked=%v retry=%s failed=%q released=%d", status, err, queue.acked, queue.retryDelay, repo.failed, repo.released)
		}
	}
}

func TestTerminalRenderAndOutputFailuresFailJob(t *testing.T) {
	cases := map[string]struct {
		renderer watermarkRenderer
		want     string
	}{
		"invalid payload": {renderer: renderFunc(func() (*Output, error) { return nil, ErrInvalidFingerprint }), want: "invalid_payload"},
		"source changed": {renderer: renderFunc(func() (*Output, error) {
			return &Output{Metadata: RenderMetadata{SourceSHA256: strings.Repeat("0", 64)}}, nil
		}), want: "source_changed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &downloadWorkerRepo{job: classificationJob()}
			queue := downloadMessage(repo.job.ID, 1)
			if _, err := ProcessOne(context.Background(), queue, NewDownloadWorker(repo, tc.renderer, &downloadWorkerAssetStub{}, "p")); err != nil || !queue.acked || repo.failed != tc.want || repo.released != 0 {
				t.Fatalf("err=%v acked=%v failed=%q released=%d", err, queue.acked, repo.failed, repo.released)
			}
		})
	}
}

func TestRetryableFailureReleasesLeaseAndBacksOffWithoutStopping(t *testing.T) {
	for dequeue, window := range map[int64][2]time.Duration{1: {15 * time.Second, 18 * time.Second}, 2: {30 * time.Second, 36 * time.Second}, 3: {60 * time.Second, 72 * time.Second}} {
		repo := &downloadWorkerRepo{job: classificationJob()}
		queue := downloadMessage(repo.job.ID, dequeue)
		assets := &downloadWorkerAssetStub{uploadErr: assetclient.ErrUnavailable}
		processed, err := ProcessOne(context.Background(), queue, NewDownloadWorker(repo, downloadWorkerRenderer{t: t}, assets, "p"))
		if err != nil || !processed {
			t.Fatalf("retryable failure stopped the worker: %v", err)
		}
		if queue.acked || repo.released != 1 || repo.failed != "" || !assets.deleted {
			t.Fatalf("acked=%v released=%d failed=%q deleted=%v", queue.acked, repo.released, repo.failed, assets.deleted)
		}
		if queue.retryDelay < window[0] || queue.retryDelay > window[1] {
			t.Fatalf("dequeue %d retry delay %s outside %v", dequeue, queue.retryDelay, window)
		}
	}
}

func TestRetryableFailureWithoutAttemptsLeftAcks(t *testing.T) {
	repo := &downloadWorkerRepo{job: classificationJob(), releaseExhausted: true}
	queue := downloadMessage(repo.job.ID, 3)
	if _, err := ProcessOne(context.Background(), queue, NewDownloadWorker(repo, downloadWorkerRenderer{t: t}, &downloadWorkerAssetStub{uploadErr: assetclient.ErrUnavailable}, "p")); err != nil || !queue.acked || queue.retryDelay != 0 {
		t.Fatalf("err=%v acked=%v retry=%s", err, queue.acked, queue.retryDelay)
	}
}

func TestCancelledRenderStillReleasesLeaseForRedelivery(t *testing.T) {
	repo := &downloadWorkerRepo{job: classificationJob()}
	queue := downloadMessage(repo.job.ID, 1)
	ctx, cancel := context.WithCancel(context.Background())
	renderer := renderFunc(func() (*Output, error) { cancel(); return nil, context.Canceled })
	if _, err := ProcessOne(ctx, queue, NewDownloadWorker(repo, renderer, &downloadWorkerAssetStub{}, "p")); err != nil || repo.released != 1 || queue.retryDelay == 0 {
		t.Fatalf("err=%v released=%d retry=%s", err, repo.released, queue.retryDelay)
	}
}

func TestMalformedMessageIsQuarantinedWithoutStopping(t *testing.T) {
	queue := &workQueueStub{message: LeasedMessage{MessageID: "m", PopReceipt: "p", DequeueCount: 1}, dequeueErr: errors.New("invalid watermark queue message")}
	if processed, err := ProcessOne(context.Background(), queue, NewDownloadWorker(&downloadWorkerRepo{}, nil, nil, "p")); err != nil || !processed || !queue.poisoned {
		t.Fatalf("processed=%v err=%v poisoned=%v", processed, err, queue.poisoned)
	}
}

func TestProcessLevelErrorsStillStopTheWorker(t *testing.T) {
	queue := &workQueueStub{dequeueErr: errors.New("queue identity rejected")}
	queue.message = LeasedMessage{}
	if _, err := ProcessOne(context.Background(), &failingDequeue{err: queue.dequeueErr}, NewDownloadWorker(&downloadWorkerRepo{}, nil, nil, "p")); err == nil {
		t.Fatal("queue identity failure was swallowed")
	}
	investigation := &workQueueStub{message: LeasedMessage{WorkMessage: WorkMessage{Kind: "investigation", ID: uuid.NewString()}, MessageID: "m", PopReceipt: "p", DequeueCount: 1}}
	if _, err := ProcessOne(context.Background(), investigation, NewDownloadWorker(&downloadWorkerRepo{}, nil, nil, "p")); err == nil || investigation.acked {
		t.Fatalf("missing investigation configuration err=%v acked=%v", err, investigation.acked)
	}
}

func TestInvestigationFailureIsRetried(t *testing.T) {
	queue := &workQueueStub{message: LeasedMessage{WorkMessage: WorkMessage{Kind: "investigation", ID: uuid.NewString()}, MessageID: "m", PopReceipt: "p", DequeueCount: 2}}
	if _, err := ProcessOne(context.Background(), queue, NewDownloadWorker(&downloadWorkerRepo{}, nil, nil, "p"), investigationFunc(func() error { return assetclient.ErrUnavailable })); err != nil || queue.acked || queue.retryDelay < 30*time.Second {
		t.Fatalf("err=%v acked=%v retry=%s", err, queue.acked, queue.retryDelay)
	}
}

type failingDequeue struct{ err error }

func (q *failingDequeue) Dequeue(context.Context) (LeasedMessage, bool, error) {
	return LeasedMessage{}, false, q.err
}
func (q *failingDequeue) Ack(context.Context, LeasedMessage) error                  { return nil }
func (q *failingDequeue) Poison(context.Context, LeasedMessage) error               { return nil }
func (q *failingDequeue) Retry(context.Context, LeasedMessage, time.Duration) error { return nil }

type investigationFunc func() error

func (f investigationFunc) Process(context.Context, string) error { return f() }

type renderFunc func() (*Output, error)

func (f renderFunc) Render(_ context.Context, source io.Reader, _ string) (*Output, error) {
	_, _ = io.Copy(io.Discard, source)
	return f()
}
