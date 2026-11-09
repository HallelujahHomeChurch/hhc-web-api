package recordings

import (
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type inboxRecordingRepo struct {
	packageTestRepo
	items    []InboxSnapshot
	calls    int
	withdraw bool
}

func (r *inboxRecordingRepo) InboxRecordings(context.Context, []string) ([]InboxSnapshot, error) {
	r.calls++
	if r.withdraw && r.calls > 1 {
		items := append([]InboxSnapshot(nil), r.items...)
		for i := range items {
			items[i].Recording.Status = "draft"
		}
		return items, nil
	}
	return r.items, nil
}
func TestInboxRecordingCurrentReadyVODArchiveAndRetentionExtension(t *testing.T) {
	now := time.Now().UTC()
	uploaded := now.Add(-31 * 24 * time.Hour)
	ready := uploaded.Add(time.Hour)
	oldExpiry := uploaded.Add(30 * 24 * time.Hour)
	expiry := uploaded.Add(60 * 24 * time.Hour)
	start, end := 7, 20
	duration := 130.0
	for _, tc := range []struct {
		name                                                      string
		revoked, unpublished, expired, unready, offline, withdraw bool
		want                                                      bool
	}{{name: "published VOD archive after extension", want: true}, {name: "revoked", revoked: true}, {name: "unpublished", unpublished: true}, {name: "expired", expired: true}, {name: "unready", unready: true}, {name: "asset outage", offline: true}, {name: "withdrawn during Asset check", withdraw: true}} {
		t.Run(tc.name, func(t *testing.T) {
			r := Recording{ID: "rec", Title: "Current title", Broadcast: true, Status: "published", PackageID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", UploadedAt: &uploaded, ReadyAt: &ready, ExpiresAt: &oldExpiry}
			if tc.unpublished {
				r.Status = "draft"
			}
			b := Broadcast{Epoch: 1, MemberState: "vod", RangeApplied: 3, Revoked: tc.revoked, View: BroadcastView{Announced: true, Phase: "archived", Boundaries: BroadcastBoundaries{Revision: 3, StartSequence: &start, EndSequenceExclusive: &end}}, Projection: &assetclient.BroadcastProjection{Epoch: 1, RangeRevision: 3, State: "ready", StartSequence: &start, EndSequenceExclusive: &end, DurationSeconds: &duration}}
			repo := &inboxRecordingRepo{items: []InboxSnapshot{{Recording: r, Broadcast: &b}}, withdraw: tc.withdraw}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				calls++
				if request.URL.Path != "/priv/recordings/lifecycle" {
					t.Error("issued a playback capability", request.URL.Path)
					w.WriteHeader(500)
					return
				}
				if tc.offline {
					w.WriteHeader(503)
					return
				}
				state, deadline := "ready", expiry
				if tc.unready {
					state = "failed"
				}
				if tc.expired {
					deadline = oldExpiry
				}
				json.NewEncoder(w).Encode(assetclient.RecordingLifecycleSnapshot{Policy: assetclient.RecordingRetentionPolicy{RetentionDays: 60, Revision: 2}, Items: []assetclient.RecordingPackageStatus{{PackageID: r.PackageID, SessionID: r.PackageID, RecordingID: r.ID, State: state, UploadedAt: &uploaded, ReadyAt: &ready, MediaExpiresAt: &deadline, RetentionRevision: 2}}})
			}))
			defer server.Close()
			s := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			titles, err := s.InboxVisible(context.Background(), []string{"rec"})
			if tc.offline {
				if err == nil {
					t.Fatal("outage became false empty")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (titles["rec"] != "") != tc.want {
				t.Fatalf("visibility=%v want=%v", titles, tc.want)
			}
			if calls > 1 {
				t.Fatalf("N+1 requests %d", calls)
			}
		})
	}
}

func TestInboxCurrentLiveUsesOnlyBoundedReadinessAndSupportsEndPending(t *testing.T) {
	now := time.Now().UTC()
	created := now.Add(-100 * time.Second)
	expiry := now.Add(time.Hour)
	captureID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	start := 0
	duration := 90.0
	for _, phase := range []string{"live", "end_pending"} {
		t.Run(phase, func(t *testing.T) {
			c := Capture{IntentID: "intent", RecordingID: "live-rec", Status: CaptureStatus{LiveEnabled: true, RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: captureID, RecordingID: "live-rec", State: "uploading", CreatedAt: created, ExpiresAt: expiry}}}
			b := Broadcast{Epoch: 1, MemberState: "live", RangeApplied: 3, CaptureIntentID: c.IntentID, View: BroadcastView{Announced: true, Phase: phase, Binding: &BroadcastBinding{CaptureID: captureID, Epoch: 1}, Boundaries: BroadcastBoundaries{Revision: 3, StartSequence: &start}}, Projection: &assetclient.BroadcastProjection{Epoch: 1, RangeRevision: 3, State: "ready", StartSequence: &start, DurationSeconds: &duration}}
			repo := &inboxRecordingRepo{items: []InboxSnapshot{{Recording: Recording{ID: "live-rec", Title: "Live title", Broadcast: true}, Broadcast: &b, Capture: &c}}}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/priv/website/content-readiness" {
					t.Error("unbounded playback/status path", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				var in struct {
					Items []assetclient.WebsiteContentRef `json:"items"`
				}
				if json.NewDecoder(r.Body).Decode(&in) != nil || len(in.Items) != 1 {
					t.Error("invalid batch")
				}
				json.NewEncoder(w).Encode(struct {
					Items []assetclient.WebsiteContentDecision `json:"items"`
				}{[]assetclient.WebsiteContentDecision{{Ref: in.Items[0], Ready: true, Live: &assetclient.WebsiteLiveReadiness{State: "uploading", CreatedAt: created, ExpiresAt: expiry, Progress: assetclient.RecordingLiveProgress{Revision: 3, LastSequence: 2, MediaEndSeconds: 90, LastAdvancedAt: &now}}}}})
			}))
			defer server.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
			titles, err := svc.InboxVisible(context.Background(), []string{"live-rec"})
			if err != nil || titles["live-rec"] != "Live title" || calls != 1 {
				t.Fatalf("current live unavailable titles=%v calls=%d err=%v", titles, calls, err)
			}
		})
	}
}
