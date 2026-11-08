package recordings

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type shareBroadcastRepo struct {
	*testRepo
	BroadcastRepository
	broadcast Broadcast
}

func (r *shareBroadcastRepo) GetBroadcast(context.Context, string) (Broadcast, error) {
	return r.broadcast, nil
}
func TestBroadcastShareEligibility(t *testing.T) {
	now := time.Now()
	for _, phase := range []string{"draft", "scheduled", "preview", "start_pending", "live", "end_pending", "processing", "archived", "cancelled", "failed", "unknown"} {
		t.Run(phase, func(t *testing.T) {
			repo := &shareBroadcastRepo{testRepo: &testRepo{recording: Recording{ID: "recording", Title: "Public title", Broadcast: true, Status: "draft"}}, broadcast: Broadcast{View: BroadcastView{Announced: true, Phase: phase}}}
			svc := NewService(repo, "", nil, func() time.Time { return now })
			preview, err := svc.SharePreview(context.Background(), "recording")
			allowed := phase == "scheduled" || phase == "preview" || phase == "start_pending" || phase == "live" || phase == "end_pending" || phase == "processing"
			if allowed {
				if err != nil || preview.Title != "Public title" {
					t.Fatalf("eligible preview: %+v %v", preview, err)
				}
			} else if !errors.Is(err, ErrNotFound) {
				t.Fatalf("ineligible phase exposed: %v", err)
			}
		})
	}
	for _, deny := range []string{"hidden", "terminal", "expired", "unannounced", "revoked", "cancelled"} {
		t.Run(deny, func(t *testing.T) {
			repo := &shareBroadcastRepo{testRepo: &testRepo{recording: Recording{ID: "recording", Broadcast: true}}, broadcast: Broadcast{View: BroadcastView{Announced: true, Phase: "live"}}}
			switch deny {
			case "hidden":
				repo.recording.Hidden = true
			case "terminal":
				repo.recording.MediaTerminal = true
			case "expired":
				repo.recording.ExpiresAt = &now
			case "unannounced":
				repo.broadcast.View.Announced = false
			case "revoked":
				repo.broadcast.Revoked = true
			case "cancelled":
				repo.broadcast.CancelledAt = &now
			}
			svc := NewService(repo, "", nil, func() time.Time { return now })
			if _, err := svc.SharePreview(context.Background(), "recording"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("exposed %s: %v", deny, err)
			}
		})
	}
}

func TestBroadcastShareAutoCoverUsesStaticBranding(t *testing.T) {
	repo := &shareBroadcastRepo{testRepo: &testRepo{recording: Recording{ID: "recording", Broadcast: true}}, broadcast: Broadcast{View: BroadcastView{Announced: true, Phase: "live"}, CoverSnapshot: CaptureLiveCover{Settings: LiveCoverSettings{Selection: LiveCoverSelection{Mode: "auto"}}}}}
	svc := NewService(repo, "", nil, time.Now)
	data, err := svc.ShareCover(context.Background(), "recording")
	if err != nil || len(data) != len(churchShareCover) || len(data) < 3 || data[0] != 255 || data[1] != 216 {
		t.Fatalf("static cover: %d %v", len(data), err)
	}
}

func TestBroadcastShareCustomCoverRechecksAuthority(t *testing.T) {
	for _, change := range []string{"none", "progress", "revoke", "hide", "selection"} {
		t.Run(change, func(t *testing.T) {
			repo := &shareBroadcastRepo{testRepo: &testRepo{recording: Recording{ID: "recording", Broadcast: true}}, broadcast: Broadcast{View: BroadcastView{Announced: true, Phase: "processing"}, CoverSnapshot: CaptureLiveCover{Scope: "defaults", Settings: LiveCoverSettings{Selection: LiveCoverSelection{Mode: "custom"}, CoverState: "ready", CoverID: "custom-cover"}}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch change {
				case "progress":
					repo.broadcast.View.Revision++
				case "revoke":
					repo.broadcast.Revoked = true
				case "hide":
					repo.recording.Hidden = true
				case "selection":
					repo.broadcast.CoverSnapshot.Settings.CoverID = "replacement"
				}
				w.Header().Set("Content-Type", "image/jpeg")
				w.Write([]byte("custom jpeg"))
			}))
			defer server.Close()
			svc := NewService(repo, "", nil, time.Now).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			data, err := svc.ShareCover(context.Background(), "recording")
			if change == "none" || change == "progress" {
				if err != nil || string(data) != "custom jpeg" {
					t.Fatalf("custom cover: %q %v", data, err)
				}
			} else if !errors.Is(err, ErrNotFound) || len(data) != 0 {
				t.Fatalf("stale cover escaped: %q %v", data, err)
			}
		})
	}
}
