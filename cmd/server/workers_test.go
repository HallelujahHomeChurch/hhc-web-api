package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAPIWorkersDrainBeforeDatabaseCleanup(t *testing.T) {
	for _, blocked := range []int{0, 1, 2, 3, 4, 5} {
		t.Run([]string{"publication", "identity", "auto-publication", "broadcasts", "captures", "inbox-publications"}[blocked], func(t *testing.T) {
			started := make(chan struct{}, 6)
			canceled := make(chan struct{}, 6)
			release := make(chan struct{})
			defer close(release)
			loop := func(index int) func(context.Context) {
				return func(ctx context.Context) {
					started <- struct{}{}
					<-ctx.Done()
					canceled <- struct{}{}
					if index == blocked {
						<-release // Cleanup may outlive cancellation of the operation.
					}
				}
			}
			workers := apiWorkers{
				publication:        func(ctx context.Context) error { loop(0)(ctx); return nil },
				identityProjection: loop(1), recordingAutoPublication: loop(2),
				recordingBroadcasts: loop(3), recordingCaptures: loop(4), inboxPublications: loop(5),
			}
			stop := workers.start(context.Background(), func() { t.Error("unexpected process failure") })
			for range 6 {
				awaitWorkerEvent(t, started)
			}
			databaseClosed := make(chan struct{})
			go func() {
				defer close(databaseClosed)
				defer stop()
				// Same defer order as run(), including an HTTP startup error.
			}()
			for range 6 {
				awaitWorkerEvent(t, canceled)
			}
			select {
			case <-databaseClosed:
				t.Fatal("database cleanup ran while an owned worker was still cleaning up")
			case <-time.After(20 * time.Millisecond):
			}
			// Release the blocked cleanup and require completion; don't leak it on failure.
			release <- struct{}{}
			awaitWorkerEvent(t, databaseClosed)
		})
	}
}

func TestAPIWorkersPublicationFailureCancelsSiblings(t *testing.T) {
	identityStarted := make(chan struct{})
	identityCanceled := make(chan struct{})
	processStopped := make(chan struct{})
	workers := apiWorkers{
		publication: func(context.Context) error {
			<-identityStarted
			return errors.New("synthetic worker failure")
		},
		identityProjection: func(ctx context.Context) {
			close(identityStarted)
			<-ctx.Done()
			close(identityCanceled)
		},
	}
	stop := workers.start(context.Background(), func() { close(processStopped) })
	defer stop()
	awaitWorkerEvent(t, processStopped)
	awaitWorkerEvent(t, identityCanceled)
}

func TestRunRejectsQueueConstructionBeforeServing(t *testing.T) {
	// No database connection or cloud credential is needed for construction.
	for key, value := range map[string]string{
		"DATABASE_URL": "postgres://synthetic:synthetic@127.0.0.1:1/cms_lifecycle?sslmode=disable",
		"ENVIRONMENT":  "development", "APP_API_TOKEN": "synthetic",
		"CMS_DIAGNOSTICS_ENABLED": "false", "AUDIT_DISPATCH_ENABLED": "false",
		"VIDEO_MEDIA_BASE_URL": "https://media.invalid", "VIDEO_WATERMARK_KEY_HEX": strings.Repeat("ab", 32),
		"BULLETIN_WATERMARK_QUEUE_URL":        "http://invalid.example/queue",
		"BULLETIN_WATERMARK_POISON_QUEUE_URL": "",
	} {
		t.Setenv(key, value)
	}
	err := run()
	if err == nil || !strings.Contains(err.Error(), "queue URLs must use HTTPS") {
		t.Fatalf("expected queue construction error, got %v", err)
	}
}

func awaitWorkerEvent(t *testing.T, event <-chan struct{}) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(time.Second):
		t.Fatal("worker lifecycle event did not complete")
	}
}
