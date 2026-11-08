package postgres

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestMemberCoverWaitReleasesPoolAndRechecksAuthority(t *testing.T) {
	for _, change := range []string{"unchanged", "revoke", "cover", "erase", "delete", "expired", "terminal"} {
		t.Run(change, func(t *testing.T) {
			repo, b, ctx, now := subjectLiveFixture(t)
			user := platform.NewID()
			_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(_ context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
				b.CoverSnapshot.Scope = "defaults"
				b.CoverSnapshot.Settings.CoverState = "ready"
				b.CoverSnapshot.Settings.CoverID = strings.Repeat("a", 32)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			jpeg := []byte{0xff, 0xd8, 0xff, 0xd9}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "image/jpeg")
				w.Write(jpeg)
			}))
			defer server.Close()
			defer unblock()
			svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				data, err := svc.BroadcastCoverContent(runCtx, b.View.RecordingID, "", user, true)
				if change == "unchanged" && err == nil && !bytes.Equal(data, jpeg) {
					err = errors.New("wrong cover bytes")
				}
				if change != "unchanged" && len(data) != 0 {
					err = errors.New("returned bytes after authority changed")
				}
				done <- err
			}()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("cover not reached: %v", err)
			case <-runCtx.Done():
				t.Fatal(runCtx.Err())
			}
			if stats := repo.db.Stats(); stats.InUse != 0 {
				t.Fatalf("cover retained SQL connection: %+v", stats)
			}
			probe, stop := context.WithTimeout(ctx, 300*time.Millisecond)
			err = svc.RecordPlaybackHeartbeat(probe, b.View.RecordingID, user, recordings.PlaybackHeartbeat{PlaybackSessionID: platform.NewID(), Sequence: 1, State: "playing"})
			stop()
			if err != nil {
				t.Fatalf("same-subject heartbeat blocked: %v", err)
			}
			probe, stop = context.WithTimeout(ctx, 300*time.Millisecond)
			switch change {
			case "erase":
				_, _, err = New(repo.db).CleanupWatermarkAccount(probe, user, "cover-test:"+platform.NewID())
			case "delete":
				_, err = repo.db.ExecContext(probe, `UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, b.View.RecordingID, now)
			case "expired":
				_, err = repo.db.ExecContext(probe, `UPDATE hhc_web.member_recording SET uploaded_at=$2,expires_at=$3,package_id=$4 WHERE id=$1`, b.View.RecordingID, now.Add(-48*time.Hour), now.Add(-time.Second), strings.Repeat("a", 32))
			case "terminal":
				_, err = repo.db.ExecContext(probe, `UPDATE hhc_web.member_recording SET media_terminal=true WHERE id=$1`, b.View.RecordingID)
			case "revoke", "cover":
				_, err = repo.UpdateBroadcast(probe, b.View.RecordingID, "", now, func(_ context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
					if change == "revoke" {
						b.Revoked = true
					} else {
						b.CoverSnapshot.Settings.CoverID = strings.Repeat("b", 32)
					}
					return nil
				})
			}
			stop()
			if err != nil {
				t.Fatalf("authority mutation blocked: %v", err)
			}
			unblock()
			err = <-done
			if change == "unchanged" && err != nil {
				t.Fatal(err)
			}
			if change == "erase" && !errors.Is(err, bulletinwatermark.ErrRestricted) {
				t.Fatalf("erase fence: %v", err)
			}
			if change != "unchanged" && change != "erase" && !errors.Is(err, recordings.ErrNotFound) {
				t.Fatalf("stale cover accepted after %s: %v", change, err)
			}
		})
	}
}
