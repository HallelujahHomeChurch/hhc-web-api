package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestLiveScopesDoNotSerializeIndependentIssuance(t *testing.T) {
	for _, sameMember := range []bool{false, true} {
		t.Run(map[bool]string{false: "different members", true: "same member"}[sameMember], func(t *testing.T) {
			repo, c, now, _, _, _, _ := liveFixture(t)
			repo.db.SetMaxOpenConns(8)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			users := []string{platform.NewID(), platform.NewID()}
			if sameMember {
				users[1] = users[0]
			}
			// Reuse stable receipts: creating the first receipt is a separate unique-key fence.
			for _, user := range users {
				if err := repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, platform.NewID(), *now, true, func(recordings.Capture, string) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			results := make(chan error, 2)
			for _, user := range users {
				go func(user string) {
					results <- repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, platform.NewID(), *now, true, func(recordings.Capture, string) error { entered <- struct{}{}; <-release; return nil })
				}(user)
			}
			parallel := true
			for i := 0; i < 2; i++ {
				select {
				case <-entered:
				case <-time.After(500 * time.Millisecond):
					parallel = false
				}
			}
			close(release)
			for i := 0; i < 2; i++ {
				if err := <-results; err != nil {
					t.Fatal(err)
				}
			}
			if !parallel {
				t.Fatal("independent grants serialized behind an in-flight media-authority call")
			}
		})
	}
}

func TestLivePlaybackRefreshDoesNotBlockIndependentGrant(t *testing.T) {
	repo, c, now, _, status, _, _ := liveFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.Status.Progress = status.Progress; return nil }); err != nil {
		t.Fatal(err)
	}
	users := []string{platform.NewID(), platform.NewID()}
	for _, user := range users {
		if err := repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, platform.NewID(), *now, true, func(recordings.Capture, string) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/grant"):
			var input struct{ ExpiresAt time.Time }
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			entered <- struct{}{}
			<-release
			json.NewEncoder(w).Encode(assetclient.RecordingGrant{ExchangeCredential: "credential", ExpiresAt: input.ExpiresAt})
		case strings.HasSuffix(r.URL.Path, "/progress"):
			json.NewEncoder(w).Encode(status.Progress)
		default:
			json.NewEncoder(w).Encode(status)
		}
	}))
	defer server.Close()
	service := recordings.NewService(repo, "https://media.alive.org.tw", []byte(strings.Repeat("test", 8)), func() time.Time { return *now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", "")).WithWatermarkTrace(true)
	for _, user := range users {
		go func(user string) {
			_, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID(), user)
			results <- err
		}(user)
	}
	parallel := true
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(500 * time.Millisecond):
			parallel = false
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if !parallel {
		t.Fatal("playback refresh serialized independent media-authority calls")
	}
}

func TestLiveCompetingScopeOwnersIssueOnlyForWinner(t *testing.T) {
	repo, c, now, _, _, _, _ := liveFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	scope := platform.NewID()
	entered, release := make(chan struct{}), make(chan struct{})
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		first <- repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID(), scope, *now, true, func(recordings.Capture, string) error { close(entered); <-release; return nil })
	}()
	<-entered
	var loserIssued atomic.Bool
	go func() {
		second <- repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID(), scope, *now, true, func(recordings.Capture, string) error { loserIssued.Store(true); return nil })
	}()
	select {
	case err := <-second:
		close(release)
		t.Fatalf("competing scope escaped uncommitted owner: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; !errors.Is(err, recordings.ErrNotFound) || loserIssued.Load() {
		t.Fatalf("competing owner issued=%v err=%v", loserIssued.Load(), err)
	}
}

func TestLiveConcurrentFirstWatermarkUsesCommittedReceipt(t *testing.T) {
	repo, c, now, _, _, _, _ := liveFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	user := platform.NewID()
	entered, release := make(chan struct{}, 2), make(chan struct{})
	type result struct {
		code string
		err  error
	}
	results := make(chan result, 2)
	for _, code := range []string{"AAAAAAAAAA", "BBBBBBBBBB"} {
		go func(code string) {
			tx, err := repo.db.BeginTx(ctx, nil)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer tx.Rollback()
			receipt, err := ensureRecordingWatermarkTx(ctx, tx, user, c.RecordingID, c.Status.CaptureID, *now, func() (string, error) { entered <- struct{}{}; <-release; return code, nil })
			if err == nil {
				err = tx.Commit()
			}
			results <- result{receipt.Code, err}
		}(code)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-ctx.Done():
			close(release)
			t.Fatal(ctx.Err())
		}
	}
	close(release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.code == "" || first.code != second.code {
		t.Fatalf("first receipt race: %+v %+v", first, second)
	}
}

func TestLiveStaleStatusCannotReopenCommittedControl(t *testing.T) {
	for _, operation := range []string{"stop/new scope", "stop/registered scope", "close", "delete"} {
		t.Run(operation, func(t *testing.T) {
			repo, c, now, _, status, _, _ := liveFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			user, scope := platform.NewID(), platform.NewID()
			if operation == "stop/registered scope" {
				if err := repo.WithCaptureScope(ctx, c.RecordingID, c.Status.CaptureID, user, scope, *now, true, func(recordings.Capture, string) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var grants atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/progress"):
					close(entered)
					<-release
					json.NewEncoder(w).Encode(status.Progress)
				case strings.HasSuffix(r.URL.Path, "/grant"):
					grants.Add(1)
					var input struct{ ExpiresAt time.Time }
					if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
						t.Error(err)
					}
					json.NewEncoder(w).Encode(assetclient.RecordingGrant{ExchangeCredential: "credential", ExpiresAt: input.ExpiresAt})
				default:
					json.NewEncoder(w).Encode(status)
				}
			}))
			defer server.Close()
			service := recordings.NewService(repo, "https://media.alive.org.tw", []byte(strings.Repeat("test", 8)), func() time.Time { return *now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", "")).WithWatermarkTrace(true)
			type result struct {
				playback recordings.LivePlayback
				err      error
			}
			done := make(chan result, 1)
			go func() {
				v, err := service.LivePlayback(ctx, c.RecordingID, c.Status.CaptureID, scope, user)
				done <- result{v, err}
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				close(release)
				t.Fatal(ctx.Err())
			}
			var err error
			if operation == "delete" {
				_, err = repo.db.ExecContext(ctx, `UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, c.RecordingID, *now)
			} else {
				_, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
					if operation == "close" {
						c.Status.LiveEnabled = false
					} else {
						c.Status.StopAcceptedAt = now
					}
					return nil
				})
			}
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			got := <-done
			if operation == "stop/registered scope" {
				if got.err != nil || got.playback.StopAcceptedAt == nil || got.playback.LiveState != "ending" || grants.Load() != 1 {
					t.Fatalf("registered replay lost stop fence: %+v grants=%d", got, grants.Load())
				}
			} else if !errors.Is(got.err, recordings.ErrNotFound) || grants.Load() != 0 {
				t.Fatalf("stale snapshot reopened %s: %+v grants=%d", operation, got, grants.Load())
			}
		})
	}
}
