package postgres

import (
	"context"
	"encoding/json"
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

func TestLiveAutoCoverRefreshKeepsReadyImageUntilReplacementAndFreezesAtEnd(t *testing.T) {
	repo, b, c, ctx, now := liveBroadcastCoverFixture(t, "auto")
	repo.WithAudit(NewAuditOutbox(repo.db))
	start := now
	c, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.LiveCover.AutoUploadID = "live-cover-image"
		c.LiveCover.AutoState = "ready"
		c.LiveCover.AutoRefreshAt = now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	oldRef := c.LiveCover.AutoReferenceID
	if err = repo.ApplyLiveAutoCover(ctx, c, assetclient.RecordingCoverUpload{UploadID: "live-cover-image", State: "ready"}, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var refreshes, retains atomic.Int32
	var ready atomic.Bool
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/refresh-auto"):
			n := refreshes.Add(1)
			id := "refresh-image"
			if n > 1 {
				id = "late-image"
			}
			json.NewEncoder(w).Encode(assetclient.RecordingCoverUpload{UploadID: id, State: "pending"})
		case strings.HasSuffix(r.URL.Path, "/promote"):
			if !strings.Contains(r.URL.Path, "/uploads/refresh-image/") {
				t.Errorf("promoted wrong frozen image: %s", r.URL.Path)
			}
			json.NewEncoder(w).Encode(assetclient.LiveCoverPromotion{CoverID: "vod-frozen-image", State: "ready"})
		case strings.HasSuffix(r.URL.Path, "/covers"):
			json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingCover{{ID: "vod-frozen-image", UploadID: "vod-frozen", Kind: "auto", State: "ready"}}})
		case strings.HasSuffix(r.URL.Path, "/retain"):
			retains.Add(1)
			w.WriteHeader(204)
		case r.Method == "DELETE":
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/content"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte(r.URL.Path))
		case strings.Contains(r.URL.Path, "/uploads/"):
			parts := strings.Split(r.URL.Path, "/")
			id := parts[len(parts)-1]
			state := "pending"
			if id == "live-cover-image" || ready.Load() {
				state = "ready"
			}
			json.NewEncoder(w).Encode(assetclient.RecordingCoverUpload{UploadID: id, State: state})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer producer.Close()
	s := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	reconcile := func() {
		t.Helper()
		if err := s.ReconcileLiveCovers(ctx, 100); err != nil {
			t.Fatal(err)
		}
	}
	check := func(id string, revision int64) {
		t.Helper()
		got, err := repo.GetBroadcast(ctx, b.View.RecordingID)
		if err != nil || got.CoverSnapshot.Settings.CoverID != id || got.CoverSnapshot.Settings.CoverState != "ready" || got.CoverSnapshot.Settings.Revision != revision {
			t.Fatalf("selection: %+v %v", got.CoverSnapshot, err)
		}
		data, err := s.MemberLiveCoverContent(ctx, c.RecordingID, c.Status.CaptureID, platform.NewID())
		if err != nil || !strings.Contains(string(data), "/uploads/"+id+"/content") {
			t.Fatalf("readable revision: %q %v", data, err)
		}
	}
	now = start.Add(5*time.Minute - time.Second)
	reconcile()
	if refreshes.Load() != 0 {
		t.Fatal("refreshed before five minutes")
	}
	now = start.Add(5 * time.Minute)
	reconcile()
	check("live-cover-image", 3)
	if refreshes.Load() != 1 {
		t.Fatal("missing five minute refresh")
	}
	now = now.Add(30 * time.Second)
	reconcile()
	check("live-cover-image", 3)
	ready.Store(true)
	reconcile()
	check("refresh-image", 4)
	if retains.Load() != 1 {
		t.Fatal("replacement not retained once", retains.Load())
	}
	now = start.Add(10 * time.Minute)
	ready.Store(false)
	reconcile()
	check("refresh-image", 4)
	late, err := repo.GetCapture(ctx, c.IntentID, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
		end := 10
		b.View.Phase = "end_pending"
		b.View.Boundaries.EndSequenceExclusive = &end
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.ApplyLiveAutoCover(ctx, late, assetclient.RecordingCoverUpload{UploadID: "late-image", State: "ready"}, func(context.Context) error { t.Fatal("retained late image after End"); return nil }); err != nil {
		t.Fatal(err)
	}
	now = start.Add(20 * time.Minute)
	reconcile()
	frozen, err := repo.GetCapture(ctx, c.IntentID, "")
	if err != nil || !frozen.LiveCover.AutoFrozen || frozen.LiveCover.Settings.CoverID != "refresh-image" || refreshes.Load() != 2 {
		t.Fatalf("not frozen: %+v %v", frozen.LiveCover, err)
	}
	_, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.NormalEnd = true
		c.Status.State = "ready"
		c.Status.PackageID = &c.Status.CaptureID
		c.Status.StopAcceptedAt = &now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RecordPackageReady(ctx, c.RecordingID, c.Status.CaptureID, now); err != nil {
		t.Fatal(err)
	}
	reconcile()
	value, err := repo.Get(ctx, c.RecordingID)
	if err != nil || value.SelectedCoverID != "vod-frozen-image" {
		t.Fatalf("VOD lost frozen image: %+v %v", value, err)
	}
	var released bool
	if err = repo.db.QueryRow(`SELECT released_at IS NOT NULL FROM hhc_web.member_recording_live_cover_reference WHERE id=$1`, oldRef).Scan(&released); err != nil || !released {
		t.Fatalf("superseded reference retained: %v %v", released, err)
	}
}

func TestLiveAutoCoverRefreshLateResultCannotReplaceCustomOrNewGeneration(t *testing.T) {
	for _, change := range []string{"custom", "generation", "end", "promoted", "range-pending", "revoked"} {
		t.Run(change, func(t *testing.T) {
			repo, b, c, ctx, now := liveBroadcastCoverFixture(t, "auto")
			c, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
				c.LiveCover.AutoUploadID = "pending-image"
				c.LiveCover.AutoState = "pending"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, current *recordings.Capture) error {
				switch change {
				case "custom":
					b.CoverSnapshot.Settings.Selection = recordings.LiveCoverSelection{Mode: "custom", UploadID: "custom-image"}
					b.CoverSnapshot.Settings.CoverID = "custom-image"
					b.CoverSnapshot.Settings.Revision++
					current.LiveCover.Settings = b.CoverSnapshot.Settings
				case "generation":
					current.LiveCover.AutoUploadID = "newer-image"
					current.LiveCover.AutoReferenceID = platform.NewID()
				case "end":
					b.View.Phase = "end_pending"
				case "promoted":
					current.LiveCover.PromotionReady = true
				case "range-pending":
					b.RangePending = true
				case "revoked":
					b.Revoked = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			err = repo.ApplyLiveAutoCover(ctx, c, assetclient.RecordingCoverUpload{UploadID: "pending-image", State: "ready"}, func(context.Context) error {
				if change != "custom" {
					t.Fatal("retained stale callback")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.GetCapture(ctx, c.IntentID, "")
			want := "live-cover-image"
			if change == "custom" {
				want = "custom-image"
			}
			if err != nil || got.LiveCover.Settings.CoverID != want {
				t.Fatalf("late callback replaced selection: %+v %v", got.LiveCover, err)
			}
		})
	}
}

func TestLiveAutoCoverFailedRefreshKeepsCurrentImageAndRevision(t *testing.T) {
	for _, state := range []string{"pending", "failed", "expired"} {
		t.Run(state, func(t *testing.T) {
			repo, b, c, ctx, _ := liveBroadcastCoverFixture(t, "auto")
			c, err := repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error { c.LiveCover.AutoUploadID = "replacement-image"; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.ApplyLiveAutoCover(ctx, c, assetclient.RecordingCoverUpload{UploadID: "replacement-image", State: state}, func(context.Context) error { t.Fatal("retained unready image"); return nil }); err != nil {
				t.Fatal(err)
			}
			got, err := repo.GetBroadcast(ctx, b.View.RecordingID)
			if err != nil || got.CoverSnapshot.Settings.CoverID != "live-cover-image" || got.CoverSnapshot.Settings.CoverState != "ready" || got.CoverSnapshot.Settings.Revision != 3 {
				t.Fatalf("unready refresh replaced readable image: %+v %v", got.CoverSnapshot, err)
			}
		})
	}
}

func TestLiveAutoCoverRefreshRequiresActiveVerifiedPublicRange(t *testing.T) {
	for _, boundary := range []string{"custom", "prestart", "pending-range", "pending-projection", "ended", "revoked"} {
		t.Run(boundary, func(t *testing.T) {
			repo, b, c, ctx, now := liveBroadcastCoverFixture(t, "auto")
			_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
				c.LiveCover.AutoUploadID = "live-cover-image"
				c.LiveCover.AutoState = "ready"
				c.LiveCover.AutoRefreshAt = now.Add(-5 * time.Minute)
				switch boundary {
				case "custom":
					b.CoverSnapshot.Settings.Selection = recordings.LiveCoverSelection{Mode: "custom", UploadID: "live-cover-image"}
					c.LiveCover.Settings.Selection = b.CoverSnapshot.Settings.Selection
				case "prestart":
					b.View.Boundaries.StartSequence = nil
				case "pending-range":
					b.RangePending = true
				case "pending-projection":
					b.Projection.State = "pending"
				case "ended":
					b.View.Phase = "end_pending"
				case "revoked":
					b.Revoked = true
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("unexpected Asset request outside eligible public range: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(500)
			}))
			defer producer.Close()
			s := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			if err = s.ReconcileLiveCovers(ctx, 100); err != nil {
				t.Fatal(err)
			}
			got, err := repo.GetCapture(ctx, c.IntentID, "")
			if err != nil || got.LiveCover.Settings.CoverID != "live-cover-image" {
				t.Fatalf("changed ineligible cover: %+v %v", got.LiveCover, err)
			}
		})
	}
}

func TestLiveAutoCoverArchivePromotesLegacySelectionWithoutRefreshing(t *testing.T) {
	repo, b, c, ctx, now := liveBroadcastCoverFixture(t, "auto")
	repo.WithAudit(NewAuditOutbox(repo.db))
	_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
		b.View.Phase = "processing"
		c.NormalEnd = true
		c.Status.State = "ready"
		c.Status.PackageID = &c.Status.CaptureID
		// A persisted archive created before automatic refresh has no freeze field.
		c.LiveCover.AutoFrozen = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateCapture(ctx, c.IntentID, func(*recordings.Capture) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RecordPackageReady(ctx, c.RecordingID, c.Status.CaptureID, now); err != nil {
		t.Fatal(err)
	}
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/uploads/live-cover-image/promote"):
			json.NewEncoder(w).Encode(assetclient.LiveCoverPromotion{CoverID: "archive-cover", State: "ready"})
		case strings.HasSuffix(r.URL.Path, "/covers"):
			json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingCover{{ID: "archive-cover", UploadID: "archive-upload", Kind: "auto", State: "ready"}}})
		case strings.HasSuffix(r.URL.Path, "/retain"):
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected refresh for archived capture: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer producer.Close()
	s := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	if err = s.ReconcileLiveCovers(ctx, 100); err != nil {
		t.Fatal(err)
	}
	value, err := repo.Get(ctx, c.RecordingID)
	if err != nil || value.SelectedCoverID != "archive-cover" {
		t.Fatalf("legacy archive lost selected image: %+v %v", value, err)
	}
}

func TestLiveFirstAutoCoverPendingAtEndCompletesWithoutRefresh(t *testing.T) {
	for _, outcome := range []string{"ready", "failed", "expired", "selected", "published", "not-found", "forbidden", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			repo, b, c, ctx, now := liveBroadcastCoverFixture(t, "auto")
			repo.WithAudit(NewAuditOutbox(repo.db))
			_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
				b.CoverSnapshot.Settings.CoverID = ""
				b.CoverSnapshot.Settings.CoverState = "pending"
				b.CoverSnapshot.AutoUploadID = "first-pending"
				b.CoverSnapshot.AutoState = "pending"
				c.LiveCover = &b.CoverSnapshot
				end := 10
				b.View.Boundaries.EndSequenceExclusive = &end
				b.View.Phase = "processing"
				c.NormalEnd = true
				c.Status.State = "ready"
				c.Status.PackageID = &c.Status.CaptureID
				c.Status.StopAcceptedAt = &now
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.UpdateCapture(ctx, c.IntentID, func(*recordings.Capture) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err = repo.RecordPackageReady(ctx, c.RecordingID, c.Status.CaptureID, now); err != nil {
				t.Fatal(err)
			}
			var state atomic.Value
			state.Store("pending")
			var reads, promotes atomic.Int32
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/uploads/first-pending"):
					reads.Add(1)
					if state.Load().(string) == "forbidden" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if state.Load().(string) == "unavailable" {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if state.Load().(string) == "not-found" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					json.NewEncoder(w).Encode(assetclient.RecordingCoverUpload{UploadID: "first-pending", State: state.Load().(string)})
				case strings.HasSuffix(r.URL.Path, "/promote"):
					promotes.Add(1)
					json.NewEncoder(w).Encode(assetclient.LiveCoverPromotion{CoverID: "vod-first", State: "ready"})
				case strings.HasSuffix(r.URL.Path, "/covers"):
					json.NewEncoder(w).Encode(map[string]any{"items": []assetclient.RecordingCover{{ID: "vod-first", UploadID: "vod-first-upload", Kind: "auto", State: "ready"}}})
				case strings.HasSuffix(r.URL.Path, "/retain") || r.Method == http.MethodDelete:
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer producer.Close()
			svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			if err = svc.ReconcileLiveCovers(ctx, 100); err != nil {
				t.Fatal(err)
			}
			if reads.Load() != 1 || promotes.Load() != 0 {
				t.Fatalf("pending work not polled: reads=%d promotes=%d", reads.Load(), promotes.Load())
			}
			state.Store(outcome)
			if outcome == "published" {
				state.Store("ready")
				if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording SET status='published',published_at=$2,version=version+1 WHERE id::text=$1`, c.RecordingID, now); err != nil {
					t.Fatal(err)
				}
			}
			if outcome == "selected" {
				state.Store("ready")
				if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording SET selected_cover_id='user-choice' WHERE id::text=$1`, c.RecordingID); err != nil {
					t.Fatal(err)
				}
			}
			err = svc.ReconcileLiveCovers(ctx, 100)
			if outcome == "forbidden" || outcome == "unavailable" {
				if err == nil {
					t.Fatal("masked Asset error")
				}
				got, e := repo.GetCapture(ctx, c.IntentID, "")
				if e != nil || got.LiveCover.AutoState != "pending" || got.LiveCover.Settings.CoverState != "pending" {
					t.Fatalf("error changed pending work: %+v %v", got.LiveCover, e)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := repo.GetCapture(ctx, c.IntentID, "")
			if err != nil {
				t.Fatal(err)
			}
			value, err := repo.Get(ctx, c.RecordingID)
			if err != nil {
				t.Fatal(err)
			}
			if reads.Load() != 2 || got.LiveCover.AutoState != state.Load().(string) && !(outcome == "not-found" && got.LiveCover.AutoState == "failed") || !got.LiveCover.AutoFrozen {
				t.Fatalf("first cover not settled: %+v reads=%d", got.LiveCover, reads.Load())
			}
			if outcome == "selected" {
				if value.SelectedCoverID != "user-choice" || !got.LiveCover.PromotionReady || promotes.Load() != 0 {
					t.Fatalf("overwrote VOD selection: %+v", value)
				}
			} else if outcome == "ready" || outcome == "published" {
				if value.SelectedCoverID != "vod-first" || !got.LiveCover.PromotionReady || promotes.Load() != 1 {
					t.Fatalf("not inherited: %+v %+v", value, got.LiveCover)
				}
			} else if got.LiveCover.Settings.CoverState != "failed" || value.SelectedCoverID != "" || promotes.Load() != 0 {
				t.Fatalf("invalid fallback: %+v %+v", value, got.LiveCover)
			}
			priorPromotes := promotes.Load()
			if err = svc.ReconcileLiveCovers(ctx, 100); err != nil {
				t.Fatal(err)
			}
			if reads.Load() != 2 || promotes.Load() != priorPromotes {
				t.Fatal("repeated settled first-image work")
			}
		})
	}
}

func TestEndedFirstAutoCoverRejectsStaleIdentityAndSelection(t *testing.T) {
	for _, change := range []string{"custom", "revision", "upload", "epoch", "capture", "revoked", "never-public", "range-pending", "ready-image"} {
		t.Run(change, func(t *testing.T) {
			repo, b, c, ctx, now := liveBroadcastCoverFixture(t, "auto")
			_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
				b.CoverSnapshot.Settings.CoverID = ""
				b.CoverSnapshot.Settings.CoverState = "pending"
				b.CoverSnapshot.AutoUploadID = "first"
				b.CoverSnapshot.AutoState = "pending"
				c.LiveCover = &b.CoverSnapshot
				end := 10
				b.View.Boundaries.EndSequenceExclusive = &end
				b.View.Phase = "processing"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := repo.GetCapture(ctx, c.IntentID, "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
				switch change {
				case "custom":
					c.LiveCover.Settings.Selection.Mode = "custom"
				case "revision":
					c.LiveCover.Settings.Revision++
				case "upload":
					c.LiveCover.AutoUploadID = "replacement"
				case "epoch":
					b.Epoch++
				case "capture":
					b.View.Binding.CaptureID = "different-capture"
				case "revoked":
					b.Revoked = true
				case "never-public":
					b.View.Boundaries.StartSequence = nil
				case "range-pending":
					b.RangePending = true
				case "ready-image":
					c.LiveCover.Settings.CoverID = "existing"
					c.LiveCover.Settings.CoverState = "ready"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.ApplyLiveAutoCover(ctx, snapshot, assetclient.RecordingCoverUpload{UploadID: "first", State: "ready"}, func(context.Context) error { t.Fatal("retained invalid first image"); return nil }); err != nil {
				t.Fatal(err)
			}
			got, err := repo.GetCapture(ctx, c.IntentID, "")
			if err != nil {
				t.Fatal(err)
			}
			if got.LiveCover.Settings.CoverID == "first" {
				t.Fatal("applied invalid first image")
			}
		})
	}
}
