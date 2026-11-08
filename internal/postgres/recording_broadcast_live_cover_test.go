package postgres

import (
	"bytes"
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
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func liveBroadcastCoverFixture(t *testing.T, mode string) (*RecordingRepository, recordings.Broadcast, recordings.Capture, context.Context, time.Time) {
	t.Helper()
	repo, b, ctx, now := broadcastFixture(t)
	p, _ := auditclient.ProvenanceFromContext(ctx)
	_, c, err := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, p.ActorID, recordings.BroadcastBindInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind", ExpectedRevision: 1}, ProtocolVersion: recordings.BroadcastProtocolVersion}, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, current *recordings.Capture) error {
		start := 0
		b.View.Announced = true
		b.View.Phase = "live"
		b.View.Boundaries.StartSequence = &start
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: strings.Repeat("a", 32), Epoch: b.Epoch, ActorID: current.ActorID}
		b.Projection = &assetclient.BroadcastProjection{State: "ready"}
		b.CoverSnapshot.Settings = recordings.LiveCoverSettings{Revision: 3, Selection: recordings.LiveCoverSelection{Mode: mode}, CoverState: "ready", CoverID: "live-cover-image"}
		b.CoverSnapshot.Scope = ""
		if mode == "custom" {
			b.CoverSnapshot.Scope = "broadcast-" + b.View.RecordingID
		}
		if mode == "defaults" {
			b.CoverSnapshot.Settings.Selection.Mode = "custom"
			b.CoverSnapshot.Scope = "defaults"
		}
		current.Status.CaptureID = b.View.Binding.CaptureID
		current.Status.State = "uploading"
		current.Status.LiveEnabled = true
		current.LiveCover = &b.CoverSnapshot
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.GetCapture(ctx, c.IntentID, b.View.RecordingID)
	if err != nil {
		t.Fatal(err)
	}
	return repo, b, c, ctx, now
}

func TestBroadcastMemberLiveCoverCompatibility(t *testing.T) {
	for _, mode := range []string{"auto", "custom", "defaults"} {
		t.Run(mode, func(t *testing.T) {
			repo, b, c, ctx, now := liveBroadcastCoverFixture(t, mode)
			want := []byte("exact-selected-jpeg-bytes")
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/content") {
					scope, owner := c.Status.CaptureID, c.RecordingID
					if mode == "custom" {
						scope = "broadcast-" + c.RecordingID
					}
					if mode == "defaults" {
						scope, owner = "defaults", ""
					}
					if r.URL.Path != "/priv/recording-live-covers/"+scope+"/uploads/live-cover-image/content" || r.URL.Query().Get("recordingId") != owner {
						t.Errorf("wrong Asset ownership: %s", r.URL)
					}
					w.Header().Set("Content-Type", "image/jpeg")
					w.Write(want)
					return
				}
				if strings.HasSuffix(r.URL.Path, "/progress") {
					json.NewEncoder(w).Encode(c.Status.Progress)
					return
				}
				json.NewEncoder(w).Encode(c.Status.RecordingCaptureStatus)
			}))
			defer producer.Close()
			s := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			member := platform.NewID()
			got, err := s.MemberLiveCoverContent(ctx, b.View.RecordingID, c.Status.CaptureID, member)
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("B1 member cover %q: %v", got, err)
			}
			for _, query := range []string{"", "Broadcast"} {
				list, err := s.ListLive(ctx, query)
				if err != nil || len(list) != 1 || list[0].CoverRevision != "live-cover-image" {
					t.Fatalf("list/search cover %+v: %v", list, err)
				}
			}
			watch, err := s.ResolveRecordingWatch(ctx, b.View.RecordingID, member)
			if err != nil || watch.Live == nil || watch.Live.CoverRevision != "live-cover-image" {
				t.Fatalf("watch cover %+v: %v", watch, err)
			}
			if _, err = s.SetLiveCoverSettings(ctx, b.View.RecordingID, c.Status.CaptureID, 3, recordings.LiveCoverSelection{Mode: "auto"}, c.ActorID, platform.NewID()); !errors.Is(err, recordings.ErrNotFound) {
				t.Fatalf("legacy staff write opened: %v", err)
			}
		})
	}
}

func TestBroadcastMemberLiveCoverFencesEligibilityAndFetchRaces(t *testing.T) {
	cases := []struct {
		name   string
		change func(*recordings.Broadcast, *recordings.Capture)
	}{
		{"wrong binding", func(b *recordings.Broadcast, _ *recordings.Capture) {
			b.View.Binding.CaptureID = strings.Repeat("b", 32)
		}},
		{"unannounced", func(b *recordings.Broadcast, _ *recordings.Capture) { b.View.Announced = false }},
		{"revoked", func(b *recordings.Broadcast, _ *recordings.Capture) { b.Revoked = true }},
		{"ended", func(b *recordings.Broadcast, _ *recordings.Capture) { b.View.Phase = "processing" }},
		{"expired", func(_ *recordings.Broadcast, c *recordings.Capture) { c.Status.ExpiresAt = time.Time{} }},
		{"stopped", func(_ *recordings.Broadcast, c *recordings.Capture) {
			now := time.Now()
			c.Status.StopAcceptedAt = &now
		}},
		{"different epoch", func(b *recordings.Broadcast, _ *recordings.Capture) { b.Epoch++ }},
	}
	for _, tc := range cases {
		for _, duringFetch := range []bool{false, true} {
			name := tc.name
			if duringFetch {
				name += " during fetch"
			}
			t.Run(name, func(t *testing.T) {
				repo, b, c, ctx, now := liveBroadcastCoverFixture(t, "auto")
				mutate := func() {
					_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
						tc.change(b, c)
						return nil
					})
					if err != nil {
						t.Error(err)
					}
				}
				if !duringFetch {
					mutate()
				}
				producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if !duringFetch {
						t.Error("ineligible request reached Asset")
					} else {
						mutate()
					}
					w.Header().Set("Content-Type", "image/jpeg")
					w.Write([]byte("jpeg"))
				}))
				defer producer.Close()
				s := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
				data, err := s.MemberLiveCoverContent(ctx, b.View.RecordingID, c.Status.CaptureID, platform.NewID())
				if !errors.Is(err, recordings.ErrNotFound) || len(data) != 0 {
					t.Fatalf("returned ineligible bytes %q: %v", data, err)
				}
			})
		}
	}
}

func TestBroadcastMemberLiveCoverDiscardsReplacedCover(t *testing.T) {
	repo, b, c, ctx, now := liveBroadcastCoverFixture(t, "custom")
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
			b.CoverSnapshot.Settings.CoverID = "new-image"
			b.CoverSnapshot.Settings.Revision++
			return nil
		})
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("old-jpeg"))
	}))
	defer producer.Close()
	s := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	data, err := s.MemberLiveCoverContent(ctx, b.View.RecordingID, c.Status.CaptureID, platform.NewID())
	if !errors.Is(err, recordings.ErrNotFound) || len(data) != 0 {
		t.Fatalf("returned replaced bytes %q: %v", data, err)
	}
}

func TestLegacyLiveWatchRetainsReadyCoverRevision(t *testing.T) {
	repo, c, now := captureFixture(t)
	resetLiveCoverDefault(t, repo)
	ctx := context.Background()
	var err error
	c, err = repo.PrepareCapture(ctx, c, now)
	if err != nil {
		t.Fatal(err)
	}
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = strings.Repeat("e", 32)
		c.Status.State = "uploading"
		c.LiveCover.Settings.CoverState = "ready"
		c.LiveCover.Settings.CoverID = "legacy-live-cover"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/content"):
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write([]byte("legacy-jpeg"))
		case strings.HasSuffix(r.URL.Path, "/progress"):
			json.NewEncoder(w).Encode(c.Status.Progress)
		default:
			json.NewEncoder(w).Encode(c.Status.RecordingCaptureStatus)
		}
	}))
	defer producer.Close()
	s := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	member := platform.NewID()
	watch, err := s.ResolveRecordingWatch(ctx, c.RecordingID, member)
	if err != nil || watch.Live == nil || watch.Live.CoverRevision != "legacy-live-cover" {
		t.Fatalf("legacy watch cover %+v: %v", watch, err)
	}
	data, err := s.MemberLiveCoverContent(ctx, c.RecordingID, c.Status.CaptureID, member)
	if err != nil || string(data) != "legacy-jpeg" {
		t.Fatalf("legacy content %q: %v", data, err)
	}
}

func TestBroadcastLiveCoverRevisionWaitsForReadableSnapshot(t *testing.T) {
	for _, prior := range []string{"", "prior-cover-image"} {
		t.Run("prior="+prior, func(t *testing.T) {
			repo, b, c, ctx, now := liveBroadcastCoverFixture(t, "auto")
			// Capture readiness commits before the next broadcast reconciliation copies it.
			_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
				b.CoverSnapshot.Settings.CoverID = prior
				b.CoverSnapshot.Settings.CoverState = "pending"
				if prior != "" {
					b.CoverSnapshot.Settings.CoverState = "ready"
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/content"):
					w.Header().Set("Content-Type", "image/jpeg")
					w.Write([]byte(r.URL.Path))
				case strings.HasSuffix(r.URL.Path, "/progress"):
					json.NewEncoder(w).Encode(c.Status.Progress)
				default:
					json.NewEncoder(w).Encode(c.Status.RecordingCaptureStatus)
				}
			}))
			defer producer.Close()
			s := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			member := platform.NewID()
			check := func(want string) {
				t.Helper()
				for _, query := range []string{"", "Broadcast"} {
					list, err := s.ListLive(ctx, query)
					if err != nil || len(list) != 1 || list[0].CoverRevision != want {
						t.Errorf("list/search published unreadable revision: %+v, want %q: %v", list, want, err)
					}
				}
				watch, err := s.ResolveRecordingWatch(ctx, b.View.RecordingID, member)
				if err != nil || watch.Live == nil || watch.Live.CoverRevision != want {
					t.Errorf("watch published unreadable revision: %+v, want %q: %v", watch.Live, want, err)
				}
				data, err := s.MemberLiveCoverContent(ctx, b.View.RecordingID, c.Status.CaptureID, member)
				if want == "" {
					if !errors.Is(err, recordings.ErrNotFound) || len(data) != 0 {
						t.Errorf("pending bytes %q: %v", data, err)
					}
				} else if err != nil || !strings.HasSuffix(string(data), "/uploads/"+want+"/content") {
					t.Errorf("bytes do not match revision %q: %q %v", want, data, err)
				}
			}
			check(prior)
			_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
				b.CoverSnapshot = *c.LiveCover
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			check("live-cover-image")
		})
	}
}
