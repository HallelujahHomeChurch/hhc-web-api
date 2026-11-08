package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestWatchDependencyWaitReleasesSQLAndRechecksSubject(t *testing.T) {
	for _, mode := range []string{"vod", "standalone live"} {
		for _, change := range []string{"unchanged", "erase", "delete", "terminal"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				repo, c, now := captureFixture(t)
				ctx := context.Background()
				var status assetclient.RecordingCaptureStatus
				pkg := strings.Repeat("a", 32)
				expiry := now.Add(720 * time.Hour)
				if mode == "vod" {
					if err := repo.AddPackage(ctx, c.RecordingID, pkg, now); err != nil {
						t.Fatal(err)
					}
					if _, err := repo.RecordPackageReady(ctx, c.RecordingID, pkg, now); err != nil {
						t.Fatal(err)
					}
					if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET status='published' WHERE id=$1`, c.RecordingID); err != nil {
						t.Fatal(err)
					}
				} else {
					var err error
					c, err = repo.PrepareCapture(ctx, c, now)
					if err != nil {
						t.Fatal(err)
					}
					c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
						c.Status.CaptureID = strings.Repeat("b", 32)
						c.Status.State = "uploading"
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
					now = now.Add(100 * time.Second)
					status = c.Status.RecordingCaptureStatus
					status.Progress = assetclient.RecordingLiveProgress{Revision: 3, LastSequence: 2, MediaEndSeconds: 90, LastAdvancedAt: &now}
				}
				entered, release := make(chan struct{}), make(chan struct{})
				var enter, unblock sync.Once
				resume := func() { unblock.Do(func() { close(release) }) }
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					enter.Do(func() { close(entered) })
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					if mode == "vod" {
						json.NewEncoder(w).Encode(assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: c.RecordingID, State: "ready", ReadyAt: &now, MediaExpiresAt: &expiry})
						return
					}
					if strings.HasSuffix(r.URL.Path, "/progress") {
						json.NewEncoder(w).Encode(status.Progress)
					} else {
						json.NewEncoder(w).Encode(status)
					}
				}))
				defer server.Close()
				defer resume()
				svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
				request, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				user := platform.NewID()
				type result struct {
					view recordings.RecordingWatchView
					err  error
				}
				done := make(chan result, 1)
				go func() { v, err := svc.ResolveRecordingWatch(request, c.RecordingID, user); done <- result{v, err} }()
				select {
				case <-entered:
				case out := <-done:
					t.Fatalf("dependency not reached: %+v", out)
				case <-request.Done():
					t.Fatal(request.Err())
				}
				if n := repo.db.Stats().InUse; n != 0 {
					t.Fatalf("watch dependency holds %d SQL connections", n)
				}
				probe, stop := context.WithTimeout(ctx, 400*time.Millisecond)
				var err error
				switch change {
				case "erase":
					_, _, err = New(repo.db).CleanupWatermarkAccount(probe, user, "watch:"+platform.NewID())
				case "delete":
					_, err = repo.db.ExecContext(probe, `UPDATE hhc_web.member_recording SET deleted_at=$2 WHERE id=$1`, c.RecordingID, now)
				case "terminal":
					_, err = repo.db.ExecContext(probe, `UPDATE hhc_web.member_recording SET media_terminal=true WHERE id=$1`, c.RecordingID)
				}
				stop()
				if err != nil {
					t.Fatalf("authority update blocked behind Asset: %v", err)
				}
				resume()
				out := <-done
				if change == "unchanged" {
					want := "live"
					if mode == "vod" {
						want = "vod"
					}
					if out.err != nil || out.view.View != want {
						t.Fatalf("watch=%+v err=%v", out.view, out.err)
					}
				} else if out.err == nil && (out.view.View == "live" || out.view.View == "vod") {
					t.Fatalf("late watch returned active media: %+v", out.view)
				}
			})
		}
	}
}
