package recordings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type memberProjectionRepo struct {
	pageRepo
	BroadcastRepository
	LiveRepository
	broadcast Broadcast
}

func (r *memberProjectionRepo) ListVisible(context.Context, time.Time) ([]Recording, error) {
	return r.values, nil
}
func (r *memberProjectionRepo) Get(context.Context, string) (Recording, error) {
	return r.values[0], nil
}
func (r *memberProjectionRepo) GetBroadcast(context.Context, string) (Broadcast, error) {
	return r.broadcast, nil
}
func (r *memberProjectionRepo) WithRecordingSubject(_ context.Context, _ string, fn func(SubjectReader) error) error {
	return fn(r)
}

func TestMemberVODWaitsForAppliedPublicationAuthority(t *testing.T) {
	now := time.Date(2026, 10, 16, 9, 56, 17, 0, time.UTC)
	uploaded, expiry := now.Add(-time.Hour), now.Add(719*time.Hour)
	start, end, duration := 0, 8, 218.4182
	id, pkg := "00000000-0000-4000-8000-000000000001", strings.Repeat("a", 32)
	recording := Recording{ID: id, PackageID: pkg, Status: "published", UploadedAt: &uploaded, ReadyAt: &uploaded, ExpiresAt: &expiry, Broadcast: true}
	ready := Broadcast{Epoch: 1, MemberState: "vod", RangeApplied: 5, View: BroadcastView{Announced: true, Phase: "archived", Boundaries: BroadcastBoundaries{Revision: 5, StartSequence: &start, EndSequenceExclusive: &end}}, Projection: &assetclient.BroadcastProjection{Epoch: 1, RangeRevision: 5, State: "ready", StartSequence: &start, EndSequenceExclusive: &end, DurationSeconds: &duration}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeReadyFixture(t, w, r, assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: id, State: "ready", ReadyAt: &uploaded, MediaExpiresAt: &expiry})
	}))
	defer server.Close()
	for _, tc := range []struct {
		name    string
		change  func(*Broadcast)
		wantVOD bool
	}{
		{"publication pending", func(b *Broadcast) { b.RangePending = true; b.RangeApplied = 4 }, false},
		{"old authority", func(b *Broadcast) { b.RangeApplied = 4 }, false},
		{"old projection", func(b *Broadcast) { b.Projection.RangeRevision = 4 }, false},
		{"old epoch", func(b *Broadcast) { b.Projection.Epoch = 2 }, false},
		{"live authority", func(b *Broadcast) { b.MemberState = "live" }, false},
		{"revoked", func(b *Broadcast) { b.Revoked = true }, false},
		{"mismatched boundary", func(b *Broadcast) { other := 9; b.Projection.EndSequenceExclusive = &other }, false},
		{"applied", func(*Broadcast) {}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := ready
			projection := *ready.Projection
			b.Projection = &projection
			tc.change(&b)
			repo := &memberProjectionRepo{pageRepo: pageRepo{values: []Recording{recording}}, broadcast: b}
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			watch, err := svc.ResolveRecordingWatch(context.Background(), id, "00000000-0000-4000-8000-000000000002")
			if err != nil || (watch.View == "vod") != tc.wantVOD || (watch.Recording != nil) != tc.wantVOD {
				t.Fatalf("watch=%+v err=%v", watch, err)
			}
			for _, paginated := range []bool{false, true} {
				var items []Recording
				if paginated {
					items, _, err = svc.ListPage(context.Background(), "", 12, "")
				} else {
					items, err = svc.List(context.Background())
				}
				if err != nil || (len(items) == 1) != tc.wantVOD {
					t.Fatalf("paginated=%v count=%d err=%v", paginated, len(items), err)
				}
			}
		})
	}
}

func TestMemberRecordingDurationUsesPublicBroadcastRange(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	uploaded, expiry := now.Add(-time.Hour), now.Add(719*time.Hour)
	id, pkg := "00000000-0000-4000-8000-000000000001", strings.Repeat("a", 32)
	start, end, duration := 8, 56, 1441.44
	for _, broadcast := range []bool{true, false} {
		t.Run(map[bool]string{true: "broadcast", false: "standalone"}[broadcast], func(t *testing.T) {
			repo := &memberProjectionRepo{pageRepo: pageRepo{values: []Recording{{ID: id, PackageID: pkg, Status: "published", UploadedAt: &uploaded, ReadyAt: &uploaded, ExpiresAt: &expiry, Broadcast: broadcast}}}, broadcast: Broadcast{Epoch: 1, MemberState: "vod", RangeApplied: 5, View: BroadcastView{Announced: true, Phase: "archived", Boundaries: BroadcastBoundaries{Revision: 5, StartSequence: &start, EndSequenceExclusive: &end}}, Projection: &assetclient.BroadcastProjection{Epoch: 1, RangeRevision: 5, StartSequence: &start, EndSequenceExclusive: &end, State: "ready", DurationSeconds: &duration}}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeReadyFixture(t, w, r, assetclient.RecordingPackageStatus{PackageID: pkg, SessionID: pkg, RecordingID: id, State: "ready", ReadyAt: &uploaded, MediaExpiresAt: &expiry, Renditions: []assetclient.RecordingRendition{{Name: "480p", DurationSeconds: 1785.3836}}})
			}))
			defer server.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			want := 1785.3836
			if broadcast {
				want = duration
			}
			for _, paginated := range []bool{false, true} {
				var items []Recording
				var err error
				if paginated {
					items, _, err = svc.ListPage(context.Background(), "", 12, "")
				} else {
					items, err = svc.List(context.Background())
				}
				if err != nil || len(items) != 1 || items[0].DurationSeconds != want {
					t.Errorf("paginated=%v items=%+v err=%v want duration=%v", paginated, items, err, want)
				}
			}
			watch, err := svc.ResolveRecordingWatch(context.Background(), id, "00000000-0000-4000-8000-000000000002")
			if err != nil || watch.Recording == nil || watch.Recording.DurationSeconds != want {
				t.Errorf("watch=%+v err=%v", watch, err)
			}
			if broadcast {
				for _, projection := range []*assetclient.BroadcastProjection{nil, {State: "pending", DurationSeconds: &duration}, {State: "ready"}} {
					repo.broadcast.Projection = projection
					for _, paginated := range []bool{false, true} {
						var items []Recording
						var err error
						if paginated {
							items, _, err = svc.ListPage(context.Background(), "", 12, "")
						} else {
							items, err = svc.List(context.Background())
						}
						if err != nil || len(items) != 0 {
							t.Errorf("missing public duration fell back to raw: paginated=%v items=%+v err=%v", paginated, items, err)
						}
					}
				}
			}
		})
	}
}

type summaryProjectionRepo struct {
	Repository
	BroadcastRepository
	LiveRepository
	CaptureRepository
	capture   Capture
	broadcast Broadcast
}

func (r *summaryProjectionRepo) GetCapture(context.Context, string, string) (Capture, error) {
	return r.capture, nil
}
func (r *summaryProjectionRepo) LatestRecordingCapture(context.Context, string) (Capture, error) {
	return r.capture, nil
}
func (r *summaryProjectionRepo) GetBroadcast(context.Context, string) (Broadcast, error) {
	return r.broadcast, nil
}
func (r *summaryProjectionRepo) UpdateCapture(_ context.Context, _ string, fn func(*Capture) error) (Capture, error) {
	err := fn(&r.capture)
	return r.capture, err
}

func TestAdminCaptureSummaryUsesBroadcastPublicationIntent(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name                                  string
		epoch                                 int64
		policy, activated, revoked, cancelled bool
		state, status, want                   string
	}{
		{"live broadcast awaits archive", 1, true, false, false, false, "uploading", "cancelled", "pending"},
		{"ready broadcast awaits activation", 1, true, false, false, false, "ready", "cancelled", "pending"},
		{"standalone retains cancellation", 0, true, false, false, false, "uploading", "cancelled", "cancelled"},
		{"disabled policy", 1, false, false, false, false, "uploading", "cancelled", "cancelled"},
		{"explicit cancellation", 1, true, false, false, true, "uploading", "cancelled", "cancelled"},
		{"revoked", 1, true, false, true, false, "uploading", "cancelled", "cancelled"},
		{"failed", 1, true, false, false, false, "failed", "cancelled", "cancelled"},
		{"expired", 1, true, false, false, false, "expired", "cancelled", "cancelled"},
		{"aborted", 1, true, false, false, false, "aborted", "cancelled", "cancelled"},
		{"activated cancellation", 1, true, true, false, false, "ready", "cancelled", "cancelled"},
		{"blocked publication", 1, true, true, false, false, "ready", "blocked", "blocked"},
		{"published", 1, true, true, false, false, "ready", "published", "published"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := strings.Repeat("b", 32)
			repo := &summaryProjectionRepo{capture: Capture{RecordingID: "recording", BroadcastEpoch: tc.epoch, Operations: map[string]CaptureOperation{}, Status: CaptureStatus{AutoPublish: tc.status, RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: capture, State: tc.state, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}}}, broadcast: Broadcast{Epoch: 1, AutoPublicationActivated: tc.activated, Revoked: tc.revoked, View: BroadcastView{Policy: BroadcastPolicy{AutoPublish: tc.policy}, Binding: &BroadcastBinding{CaptureID: capture, Epoch: 1}}}}
			if tc.cancelled {
				repo.capture.Operations["cancel"] = CaptureOperation{Operation: "cancel_auto_publish"}
			}
			svc := NewService(repo, "", nil, func() time.Time { return now })
			got, err := svc.AdminCaptureSummary(context.Background(), "recording")
			if err != nil || got == nil || got.AutoPublish != tc.want {
				t.Fatalf("summary=%+v err=%v want=%s", got, err, tc.want)
			}
			if repo.capture.Status.AutoPublish != tc.status {
				t.Fatal("summary mutated execution state")
			}
			if tc.want == "pending" {
				for _, phase := range []string{"failed", "cancelled"} {
					repo.broadcast.View.Phase = phase
					got, err := svc.AdminCaptureSummary(context.Background(), "recording")
					if err != nil || got == nil || got.AutoPublish != "cancelled" {
						t.Fatalf("phase=%s summary=%+v err=%v", phase, got, err)
					}
				}
			}
		})
	}
}

func TestMemberWaitingCoverModePreservesCustomPendingIntent(t *testing.T) {
	for _, mode := range []string{"auto", "custom"} {
		t.Run(mode, func(t *testing.T) {
			id := "00000000-0000-4000-8000-000000000001"
			repo := &memberProjectionRepo{pageRepo: pageRepo{values: []Recording{{ID: id, Broadcast: true}}}, broadcast: Broadcast{View: BroadcastView{Announced: true, Phase: "scheduled"}, CoverSnapshot: CaptureLiveCover{Settings: LiveCoverSettings{Selection: LiveCoverSelection{Mode: mode}, CoverState: "pending"}}}}
			svc := NewService(repo, "", nil, time.Now)
			watch, err := svc.ResolveRecordingWatch(context.Background(), id, "00000000-0000-4000-8000-000000000002")
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(watch)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err = json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if got["coverMode"] != mode || got["cover"] != nil || got["view"] != "waiting" {
				t.Fatalf("unexpected waiting cover: %s", data)
			}
		})
	}
}

func TestLiveCoverModeOnlyProjectsKnownSelection(t *testing.T) {
	if liveCoverMode(nil) != "" {
		t.Fatal("missing metadata must not imply automatic cover")
	}
	for _, mode := range []string{"auto", "custom", "", "unknown"} {
		got := liveCoverMode(&CaptureLiveCover{Settings: LiveCoverSettings{Selection: LiveCoverSelection{Mode: mode}}})
		want := mode
		if mode == "unknown" {
			want = ""
		}
		if got != want {
			t.Fatalf("mode=%q got=%q want=%q", mode, got, want)
		}
	}
}
