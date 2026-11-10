package recordings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

func TestWithdrawnArchiveStaysBlockedAcrossProjectionRefresh(t *testing.T) {
	for _, auto := range []string{"published", "cancelled"} {
		t.Run(auto, func(t *testing.T) {
			repo, now, start := pendingBroadcastCaptureFixture()
			end := 30
			repo.broadcast.View.Boundaries.EndSequenceExclusive = &end
			repo.broadcast.RangePending = false
			repo.broadcast.RangeApplied = repo.broadcast.View.Boundaries.Revision
			repo.broadcast.MemberState = "blocked"
			repo.broadcast.View.Policy.AutoPublish = true
			// Exercise persisted input without relying on a constructor to set the marker.
			if err := json.Unmarshal([]byte(`{"PublicationWithdrawn":true}`), &repo.broadcast); err != nil {
				t.Fatal(err)
			}
			repo.capture.Status.AutoPublish = auto
			repo.capture.NormalEnd = true
			repo.capture.Status.State = "ready"
			repo.recording.Status = "draft"
			producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Error("withdrawal unexpectedly changed authority")
					w.WriteHeader(500)
					return
				}
				json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: 1, RangeRevision: repo.broadcast.View.Boundaries.Revision, State: "ready", StartSequence: &start, EndSequenceExclusive: &end, Revision: 7})
			}))
			defer producer.Close()
			svc := NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
			for range 2 {
				if err := svc.ReconcileBroadcasts(context.Background(), 100); err != nil {
					t.Fatal(err)
				}
				if repo.broadcast.Revoked || repo.broadcast.MemberState != "blocked" || repo.broadcast.View.Archive.State != "ready" || repo.recording.Status != "draft" || repo.capture.Status.AutoPublish != auto || repo.capture.Status.LiveEnabled {
					t.Fatalf("background refresh restored withdrawn archive: state=%s archive=%s auto=%s live=%v", repo.broadcast.MemberState, repo.broadcast.View.Archive.State, repo.capture.Status.AutoPublish, repo.capture.Status.LiveEnabled)
				}
			}
		})
	}
}
