package recordings

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type studioCoverReadRepo struct {
	memberProjectionRepo
	StudioScheduleRepository
	mode string
}

func (r *studioCoverReadRepo) GetStudioBroadcast(context.Context, string) (StudioBroadcast, error) {
	return StudioBroadcast{Mode: r.mode, Broadcast: r.broadcast.View}, nil
}

func TestBroadcastCoverContentUsesSelectedCoverOwner(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	const capture = "11111111111111111111111111111111"
	const cover = "22222222222222222222222222222222"
	jpeg := []byte{0xff, 0xd8, 0xff, 0xd9}
	for _, tc := range []struct {
		name, mode, selection, savedScope, wantScope, wantOwner string
	}{
		{"direct auto binding fallback", "direct", "auto", "", capture, id},
		{"scheduled auto binding fallback", "scheduled", "auto", "", capture, id},
		{"direct auto capture scope", "direct", "auto", capture, capture, id},
		{"legacy auto", "legacy", "auto", "", capture, id},
		{"direct custom", "direct", "custom", "defaults", "defaults", ""},
		{"scheduled custom", "scheduled", "custom", "defaults", "defaults", ""},
		{"legacy custom", "legacy", "custom", "broadcast-" + id, "broadcast-" + id, id},
	} {
		for _, member := range []bool{false, true} {
			name := tc.name + "/admin"
			if member {
				name = tc.name + "/member"
			}
			t.Run(name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet || r.URL.Path != "/priv/recording-live-covers/"+tc.wantScope+"/uploads/"+cover+"/content" || r.URL.Query().Get("recordingId") != tc.wantOwner {
						t.Errorf("wrong cover ownership: %s %s", r.Method, r.URL.String())
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Header().Set("Content-Type", "image/jpeg")
					_, _ = w.Write(jpeg)
				}))
				t.Cleanup(server.Close)
				repo := &studioCoverReadRepo{
					mode: tc.mode,
					memberProjectionRepo: memberProjectionRepo{
						pageRepo: pageRepo{values: []Recording{{ID: id}}},
						broadcast: Broadcast{
							View: BroadcastView{RecordingID: id, Announced: true, Binding: &BroadcastBinding{CaptureID: capture}},
							CoverSnapshot: CaptureLiveCover{Scope: tc.savedScope, Settings: LiveCoverSettings{
								Selection: LiveCoverSelection{Mode: tc.selection}, CoverState: "ready", CoverID: cover,
							}},
						},
					},
				}
				service := NewService(repo, "", nil, time.Now).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
				data, err := service.BroadcastCoverContent(context.Background(), id, "", "00000000-0000-4000-8000-000000000002", member)
				if err != nil || !bytes.Equal(data, jpeg) {
					t.Fatalf("selected cover read: data=%x error=%v", data, err)
				}
			})
		}
	}
}

func TestMemberCoverSnapshotAuthorityFailures(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Second)
	for _, tc := range []struct {
		name   string
		mutate func(*memberProjectionRepo)
		want   error
	}{
		{"empty scope without binding", func(r *memberProjectionRepo) { r.broadcast.CoverSnapshot.Scope = "" }, assetclient.ErrInvalid},
		{"revoked", func(r *memberProjectionRepo) { r.broadcast.Revoked = true }, ErrNotFound},
		{"expired", func(r *memberProjectionRepo) { r.values[0].ExpiresAt = &past }, ErrNotFound},
		{"terminal", func(r *memberProjectionRepo) { r.values[0].MediaTerminal = true }, ErrNotFound},
		{"unannounced", func(r *memberProjectionRepo) { r.broadcast.View.Announced = false }, ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &memberProjectionRepo{pageRepo: pageRepo{values: []Recording{{ID: "00000000-0000-4000-8000-000000000001"}}}, broadcast: Broadcast{View: BroadcastView{Announced: true}, CoverSnapshot: CaptureLiveCover{Scope: "defaults", Settings: LiveCoverSettings{CoverState: "ready", CoverID: "22222222222222222222222222222222"}}}}
			tc.mutate(repo)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("invalid authority reached Asset")
				w.WriteHeader(500)
			}))
			defer server.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			_, e := svc.BroadcastCoverContent(context.Background(), repo.values[0].ID, "", "00000000-0000-4000-8000-000000000002", true)
			if !errors.Is(e, tc.want) {
				t.Fatalf("got %v want %v", e, tc.want)
			}
		})
	}
}
