package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestLiveListDoesNotWaitForGrantFence(t *testing.T) {
	repo, c, now, service, _, _, _ := liveFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	issued := make(chan error, 1)
	go func() {
		issued <- repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID(), platform.NewID(), *now, false, func(recordings.Capture, string) error {
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	readCtx, stop := context.WithTimeout(ctx, 300*time.Millisecond)
	values, err := service.ListLive(readCtx, "")
	stop()
	close(release)
	grantErr := <-issued
	if err != nil || len(values) != 1 || values[0].Progress.LastSequence != 2 {
		t.Fatalf("live list waited for an unrelated grant instead of observing progress: count=%d err=%v", len(values), err)
	}
	if grantErr != nil {
		t.Fatal(grantErr)
	}
	stored, err := repo.GetCapture(ctx, c.IntentID, c.RecordingID)
	if err != nil || stored.Status.Progress.LastSequence != -1 {
		t.Fatalf("viewer read changed the producer aggregate: sequence=%d err=%v", stored.Status.Progress.LastSequence, err)
	}
}

func TestLiveObservationsOmitJournalWithoutChangingProducerState(t *testing.T) {
	repo, c, now, _, _, _, _ := liveFixture(t)
	ctx := context.Background()
	_, err := repo.UpdateCapture(ctx, c.IntentID, func(current *recordings.Capture) error {
		current.Operations["completed-confirm"] = recordings.CaptureOperation{Operation: "confirm", Digest: strings.Repeat("a", 64)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := repo.GetLiveCapture(ctx, c.Status.CaptureID, c.RecordingID)
	if err != nil || observed.Operations == nil || len(observed.Operations) != 0 {
		t.Fatalf("viewer loaded journal: %v", err)
	}
	latest, err := repo.LatestRecordingCapture(ctx, c.RecordingID)
	if err != nil || latest.Operations == nil || len(latest.Operations) != 0 {
		t.Fatalf("latest observation loaded producer journal: %v", err)
	}
	if err = repo.WithRecordingSubject(ctx, platform.NewID(), func(reader recordings.SubjectReader) error {
		current, readErr := reader.LatestRecordingCapture(ctx, c.RecordingID)
		if readErr == nil && (current.Operations == nil || len(current.Operations) != 0) {
			t.Error("watch subject loaded producer journal")
		}
		return readErr
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := repo.ListLiveCaptures(ctx, *now)
	if err != nil || len(listed) != 1 || len(listed[0].Operations) != 0 {
		t.Fatalf("list loaded journal: %v", err)
	}
	if err = repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID(), platform.NewID(), *now, false, func(current recordings.Capture, _ string) error {
		if current.Operations == nil || len(current.Operations) != 0 {
			t.Error("fenced viewer loaded journal")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetCapture(ctx, c.IntentID, c.RecordingID)
	if err != nil || stored.Operations["completed-confirm"].Operation != "confirm" {
		t.Fatalf("producer journal lost: %v", err)
	}
}

func TestLivePlaybackRevalidatesAfterGrantWithoutHoldingDatabase(t *testing.T) {
	for _, change := range []string{"progress", "stop", "close", "restriction", "delete", "retention"} {
		t.Run(change, func(t *testing.T) {
			repo, c, now, _, status, _, _ := liveFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/grant") {
					var input struct{ ExpiresAt time.Time }
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return
					}
					json.NewEncoder(w).Encode(assetclient.RecordingGrant{ExchangeCredential: "credential", ExpiresAt: input.ExpiresAt})
				} else if strings.HasSuffix(r.URL.Path, "/progress") {
					json.NewEncoder(w).Encode(status.Progress)
				} else {
					json.NewEncoder(w).Encode(status)
				}
			}))
			defer producer.Close()
			service := recordings.NewService(repo, "https://media.alive.org.tw", []byte(strings.Repeat("test", 8)), func() time.Time { return *now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", "")).WithWatermarkTrace(true)
			user, scope := platform.NewID(), platform.NewID()
			type result struct {
				playback recordings.LivePlayback
				err      error
			}
			resultCh := make(chan result, 1)
			go func() {
				value, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user)
				resultCh <- result{value, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			inUse := repo.db.Stats().InUse
			controlCtx, stop := context.WithTimeout(ctx, 300*time.Millisecond)
			var controlErr error
			if change == "restriction" {
				_, _, controlErr = New(repo.db).CleanupWatermarkAccount(controlCtx, user, platform.NewID())
			} else if change == "delete" {
				repo.WithAudit(NewAuditOutbox(repo.db))
				controlCtx = auditclient.WithProvenance(controlCtx, c.ActorID, "capacity-delete")
				controlErr = repo.Delete(controlCtx, c.RecordingID, c.Status.RecordingVersion, c.ActorID, *now, true)
			} else if change == "retention" {
				_, controlErr = repo.db.ExecContext(controlCtx, `UPDATE hhc_web.member_recording SET uploaded_at=created_at,expires_at=$2 WHERE id=$1`, c.RecordingID, now.Add(30*time.Second))
			} else {
				_, controlErr = repo.UpdateCapture(controlCtx, c.IntentID, func(current *recordings.Capture) error {
					switch change {
					case "progress":
						current.Status.Progress = status.Progress
						current.Status.Progress.Revision++
						current.Status.Progress.LastSequence++
					case "stop":
						current.Status.StopAcceptedAt = now
					case "close":
						current.ClosePublication()
					}
					return nil
				})
			}
			stop()
			close(release)
			got := <-resultCh
			if inUse != 0 || controlErr != nil {
				t.Fatalf("Asset wait held CMS capacity: in_use=%d control=%v", inUse, controlErr)
			}
			if change == "progress" {
				if got.err != nil || got.playback.Progress.Revision != status.Progress.Revision+1 {
					t.Fatalf("new producer progress lost: revision=%d err=%v", got.playback.Progress.Revision, got.err)
				}
			} else {
				want := recordings.ErrNotFound
				if change == "restriction" {
					want = bulletinwatermark.ErrRestricted
				} else if change == "retention" {
					want = recordings.ErrConflict
				}
				if !errors.Is(got.err, want) || got.playback.ExchangeCredential != "" {
					t.Fatalf("control/restriction allowed credential: err=%v", got.err)
				}
			}
		})
	}
}
