package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
)

func TestConcurrentBulletinDownloadsKeepAccountIsolationAndBoundedWorkers(t *testing.T) {
	for _, replicas := range []int{1, 2} {
		t.Run(fmt.Sprintf("workers-%d", replicas), func(t *testing.T) {
			db := accessDB(t)
			db.SetMaxOpenConns(8)
			repo := New(db)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			now := time.Now().UTC()
			issueID := uuid.NewString()
			var issueNumber int
			var issueDate string
			if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(issue_number),0)+1,(COALESCE(max(issue_date),CURRENT_DATE)+1)::text FROM hhc_web.bulletin_issue`).Scan(&issueNumber, &issueDate); err != nil {
				t.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,'published',1,$5,'test','test',$4,$4)`, issueID, issueNumber, issueDate, now, uuid.NewString()); err != nil {
				t.Fatal(err)
			}
			source := []byte("%PDF-synthetic-clean-source")
			sum := sha256.Sum256(source)
			requests := make([]bulletinwatermark.DownloadJobRequest, 25)
			jobs := make([]bulletinwatermark.DownloadJob, len(requests))
			errors := make(chan error, 100)
			var group sync.WaitGroup
			for i := range requests {
				code, _ := bulletinwatermark.NewCode()
				fingerprint, _ := bulletinwatermark.NewFingerprint()
				requests[i] = bulletinwatermark.DownloadJobRequest{ID: uuid.NewString(), IdempotencyKey: uuid.NewString(), AccountID: uuid.NewString(), IssueDate: issueDate, RendererVersion: "trustmark-pdf-v2", TraceCode: code, FingerprintPayload: fingerprint, SourceSHA256: hex.EncodeToString(sum[:]), Source: bulletinwatermark.Source{IssueID: issueID, Series: "general", Locale: "zh-Hant", Revision: 1, AssetID: testWatermarkAssetID, Filename: "synthetic.pdf"}, Now: now}
				group.Go(func() {
					var err error
					jobs[i], err = repo.CreateDownloadJob(ctx, requests[i])
					if err != nil {
						errors <- err
					}
				})
			}
			group.Wait()
			if len(errors) != 0 {
				t.Fatal(<-errors)
			}
			// Concurrent tabs for one account must reuse that account's job.
			for range 25 {
				group.Go(func() {
					request := requests[0]
					request.ID, request.IdempotencyKey = uuid.NewString(), uuid.NewString()
					job, err := repo.CreateDownloadJob(ctx, request)
					if err != nil {
						errors <- err
					} else if job.ID != jobs[0].ID {
						errors <- fmt.Errorf("duplicate job created")
					}
				})
			}
			group.Wait()
			if len(errors) != 0 {
				t.Fatal(<-errors)
			}
			var queued int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.outbox_event WHERE destination='bulletin-watermark-queue'`).Scan(&queued); err != nil || queued != 25 {
				t.Fatalf("queued=%d err=%v", queued, err)
			}
			queue := &concurrentBulletinQueue{messages: make(chan bulletinwatermark.LeasedMessage, 2*len(jobs))}
			for _, job := range jobs {
				for range 2 {
					queue.messages <- bulletinwatermark.LeasedMessage{WorkMessage: bulletinwatermark.WorkMessage{Kind: "download", ID: job.ID}, DequeueCount: 1}
				}
			}
			renderer := &concurrentBulletinRenderer{t: t, entered: make(chan struct{}, len(jobs)), release: make(chan struct{}, len(jobs))}
			assets := &concurrentBulletinAssets{source: source}
			for range replicas {
				group.Go(func() {
					worker := bulletinwatermark.NewDownloadWorker(repo, renderer, assets, "synthetic-privacy")
					for {
						processed, err := bulletinwatermark.ProcessOne(ctx, queue, worker)
						if err != nil {
							errors <- err
							return
						}
						if !processed {
							return
						}
					}
				})
			}
			for range replicas {
				select {
				case <-renderer.entered:
				case <-ctx.Done():
					t.Fatal("workers failed to admit independent jobs")
				}
			}
			if renderer.active.Load() != int32(replicas) || renderer.renders.Load() != int32(replicas) {
				t.Fatalf("active=%d waiting=%d", renderer.active.Load(), len(queue.messages))
			}
			// Release controlled rendering only after proving serial/parallel admission.
			for range jobs {
				renderer.release <- struct{}{}
			}
			group.Wait()
			if len(errors) != 0 {
				t.Fatal(<-errors)
			}
			if queue.acked.Load() != 50 || renderer.renders.Load() != 25 || assets.completions.Load() != 25 {
				t.Fatalf("acked=%d renders=%d completions=%d", queue.acked.Load(), renderer.renders.Load(), assets.completions.Load())
			}
			for i, job := range jobs {
				ready, err := repo.GetDownloadJob(ctx, job.ID, requests[i].AccountID, time.Now().UTC())
				if err != nil || !ready.Ready(time.Now().UTC()) {
					t.Fatalf("ready=%v err=%v", ready.Ready(time.Now().UTC()), err)
				}
				if _, err := repo.GetDownloadJob(ctx, job.ID, requests[(i+1)%len(jobs)].AccountID, time.Now().UTC()); err != bulletinwatermark.ErrNotFound {
					t.Fatalf("cross-account read: %v", err)
				}
				request := requests[i]
				request.ID, request.IdempotencyKey = uuid.NewString(), uuid.NewString()
				cached, err := repo.CreateDownloadJob(ctx, request)
				if err != nil || cached.ID != job.ID || !cached.Ready(time.Now().UTC()) {
					t.Fatalf("cache not reused: %v", err)
				}
			}
			t.Logf("25 independent accounts, 25 duplicate tabs, 50 queue deliveries, workers=%d, active-before-release=%d, waiting-before-release=%d, ready=25, renders=25", replicas, replicas, 25-replicas)
		})
	}
}

type concurrentBulletinQueue struct {
	messages chan bulletinwatermark.LeasedMessage
	acked    atomic.Int32
	retried  atomic.Int32
}

func (q *concurrentBulletinQueue) Dequeue(context.Context) (bulletinwatermark.LeasedMessage, bool, error) {
	select {
	case message := <-q.messages:
		return message, true, nil
	default:
		return bulletinwatermark.LeasedMessage{}, false, nil
	}
}
func (q *concurrentBulletinQueue) Ack(context.Context, bulletinwatermark.LeasedMessage) error {
	q.acked.Add(1)
	return nil
}
func (q *concurrentBulletinQueue) Poison(context.Context, bulletinwatermark.LeasedMessage) error {
	return fmt.Errorf("unexpected poison")
}

// Retry simulates visibility-timeout redelivery immediately; the requested
// backoff is recorded rather than slept.
func (q *concurrentBulletinQueue) Retry(_ context.Context, message bulletinwatermark.LeasedMessage, delay time.Duration) error {
	q.retried.Add(1)
	if delay < 15*time.Second || delay > 72*time.Second {
		return fmt.Errorf("retry backoff %s outside 15s-72s", delay)
	}
	message.DequeueCount++
	q.messages <- message
	return nil
}

type concurrentBulletinRenderer struct {
	t                *testing.T
	entered, release chan struct{}
	active, renders  atomic.Int32
}

func (r *concurrentBulletinRenderer) Render(ctx context.Context, input io.Reader, payload string) (*bulletinwatermark.Output, error) {
	r.active.Add(1)
	defer r.active.Add(-1)
	r.renders.Add(1)
	r.entered <- struct{}{}
	select {
	case <-r.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	source, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	sourceHash := sha256.Sum256(source)
	output := []byte("%PDF-personalized-" + payload)
	outputHash := sha256.Sum256(output)
	f, err := os.CreateTemp(r.t.TempDir(), "derived-*.pdf")
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(output); err != nil {
		f.Close()
		return nil, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return &bulletinwatermark.Output{File: f, Metadata: bulletinwatermark.RenderMetadata{SourceSHA256: hex.EncodeToString(sourceHash[:]), OutputSHA256: hex.EncodeToString(outputHash[:]), RendererVersion: "trustmark-pdf-v2", ModelSHA256: strings.Repeat("d", 64), Pages: []bulletinwatermark.PageMetadata{{Page: 1, Width: 100, Height: 100, SourceSHA256: strings.Repeat("e", 64), OutputSHA256: strings.Repeat("f", 64)}}}}, nil
}

type concurrentBulletinAssets struct {
	source      []byte
	completions atomic.Int32
	// missingSource answers 404 for one source asset; failOnce makes the first
	// trusted completion for one job fail retryably.
	missingSource string
	failOnce      sync.Map
}

func (a *concurrentBulletinAssets) AuthorizedDownload(_ context.Context, _, assetID, _, _ string) (*http.Response, error) {
	if assetID == a.missingSource {
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(a.source)))}, nil
}
func (a *concurrentBulletinAssets) CreateDerivedBulletinUpload(_ context.Context, jobID, _, _ string, _ int64, _ string) (assetclient.CreatedUpload, error) {
	id := strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, ok := a.failOnce.Load(jobID); ok {
		a.failOnce.Store(id, true)
		a.failOnce.Delete(jobID)
	}
	return assetclient.CreatedUpload{Asset: assetclient.Asset{ID: id}}, nil
}
func (a *concurrentBulletinAssets) Upload(context.Context, assetclient.UploadTarget, io.Reader, int64) error {
	return nil
}
func (a *concurrentBulletinAssets) CompleteDerivedBulletin(_ context.Context, id, _, _ string, _ assetclient.CompleteUploadInput) (assetclient.Asset, error) {
	if _, ok := a.failOnce.LoadAndDelete(id); ok {
		return assetclient.Asset{}, fmt.Errorf("%w: injected asset 503", assetclient.ErrUnavailable)
	}
	a.completions.Add(1)
	return assetclient.Asset{ID: id, ScanStatus: "clean", ScanSignature: "trusted-bulletin-watermark-v1"}, nil
}
func (a *concurrentBulletinAssets) CreateServiceGrant(context.Context, string, string) (assetclient.Grant, error) {
	return assetclient.Grant{ID: "synthetic-grant"}, nil
}
func (a *concurrentBulletinAssets) Delete(context.Context, string) error { return nil }

// V-offline (PLAN-v2 W1/W2): one source 404 and one retryable asset failure in
// a burst of 10 must fail or recover only that job, never stop a worker, and
// leave no job queued or running.
func TestConcurrentBulletinDownloadsIsolateInjectedFailures(t *testing.T) {
	db := accessDB(t)
	db.SetMaxOpenConns(8)
	repo := New(db)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	now := time.Now().UTC()
	issueID := uuid.NewString()
	var issueNumber int
	var issueDate string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(issue_number),0)+1,(COALESCE(max(issue_date),CURRENT_DATE)+1)::text FROM hhc_web.bulletin_issue`).Scan(&issueNumber, &issueDate); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,'published',1,$5,'test','test',$4,$4)`, issueID, issueNumber, issueDate, now, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	source := []byte("%PDF-synthetic-clean-source")
	sum := sha256.Sum256(source)
	missingSource := strings.Repeat("c", 32)
	jobs := make([]bulletinwatermark.DownloadJob, 10)
	for i := range jobs {
		code, _ := bulletinwatermark.NewCode()
		fingerprint, _ := bulletinwatermark.NewFingerprint()
		asset := testWatermarkAssetID
		if i == 0 {
			asset = missingSource
		}
		request := bulletinwatermark.DownloadJobRequest{ID: uuid.NewString(), IdempotencyKey: uuid.NewString(), AccountID: uuid.NewString(), IssueDate: issueDate, RendererVersion: "trustmark-pdf-v2", TraceCode: code, FingerprintPayload: fingerprint, SourceSHA256: hex.EncodeToString(sum[:]), Source: bulletinwatermark.Source{IssueID: issueID, Series: "general", Locale: "zh-Hant", Revision: 1, AssetID: asset, Filename: "synthetic.pdf"}, Now: now}
		var err error
		if jobs[i], err = repo.CreateDownloadJob(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	queue := &concurrentBulletinQueue{messages: make(chan bulletinwatermark.LeasedMessage, 4*len(jobs))}
	for _, job := range jobs {
		queue.messages <- bulletinwatermark.LeasedMessage{WorkMessage: bulletinwatermark.WorkMessage{Kind: "download", ID: job.ID}, DequeueCount: 1}
	}
	renderer := &concurrentBulletinRenderer{t: t, entered: make(chan struct{}, 4*len(jobs)), release: make(chan struct{}, 4*len(jobs))}
	for range 4 * len(jobs) {
		renderer.release <- struct{}{}
	}
	assets := &concurrentBulletinAssets{source: source, missingSource: missingSource}
	assets.failOnce.Store(jobs[1].ID, true)
	errs := make(chan error, 4)
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			worker := bulletinwatermark.NewDownloadWorker(repo, renderer, assets, "synthetic-privacy")
			for {
				processed, err := bulletinwatermark.ProcessOne(ctx, queue, worker)
				if err != nil {
					errs <- err
					return
				}
				if !processed {
					return
				}
			}
		})
	}
	group.Wait()
	if len(errs) != 0 {
		t.Fatalf("a worker stopped: %v", <-errs)
	}
	if queue.retried.Load() != 1 || queue.acked.Load() != 10 || assets.completions.Load() != 9 {
		t.Fatalf("retried=%d acked=%d completions=%d", queue.retried.Load(), queue.acked.Load(), assets.completions.Load())
	}
	states := map[string]int{}
	for i, job := range jobs {
		var status, code string
		var attempts int
		if err := db.QueryRowContext(ctx, `SELECT status,COALESCE(error_code,''),attempts FROM hhc_web.bulletin_download_job WHERE id=$1`, job.ID).Scan(&status, &code, &attempts); err != nil {
			t.Fatal(err)
		}
		states[status]++
		switch i {
		case 0:
			if status != "failed" || code != "source_unavailable" || attempts != 1 {
				t.Fatalf("404 job status=%s code=%s attempts=%d", status, code, attempts)
			}
		case 1:
			if status != "ready" || attempts != 2 {
				t.Fatalf("retryable job status=%s attempts=%d", status, attempts)
			}
		default:
			if status != "ready" || attempts != 1 {
				t.Fatalf("healthy job %d status=%s attempts=%d", i, status, attempts)
			}
		}
	}
	if states["queued"] != 0 || states["running"] != 0 {
		t.Fatalf("stuck jobs: %v", states)
	}
	t.Logf("10 jobs, 2 workers: ready=%d failed(source_unavailable)=%d retried=1, no stuck jobs", states["ready"], states["failed"])
}
