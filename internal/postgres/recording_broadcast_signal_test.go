package postgres

import (
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDirectIdleCutoffRecoveryPrecedesLongerNormalSeal(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	b, c, e := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, recordings.BroadcastBindInput{ProtocolVersion: recordings.BroadcastProtocolVersion, BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind-idle", ExpectedRevision: b.View.Revision}}, now)
	if e != nil {
		t.Fatal(e)
	}
	capture := strings.Repeat("e", 32)
	start, sealed, frozen := 0, 20, 3
	if _, e = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = capture
		c.Status.State = "uploading"
		c.Status.StopAcceptedAt = &now
		c.NormalEnd = true
		c.SealedEndSequence = &sealed
		c.Status.Progress.Ended = true
		c.Status.Progress.EndedAt = &now
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET source_managed=true WHERE recording_id=$1`, b.View.RecordingID); e != nil {
		t.Fatal(e)
	}
	if _, e = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, r *recordings.Recording, c *recordings.Capture) error {
		b.StartedAt = &now
		b.View.Phase = "live"
		b.View.Announced = true
		b.View.Policy.AutoStart = true
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: actor.ActorID}
		b.View.Boundaries.StartSequence = &start
		b.RangePending = false
		b.RangeApplied = b.View.Boundaries.Revision
		b.MemberState = "live"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	policy := assetclient.BroadcastRangeInput{RecordingID: b.View.RecordingID, Epoch: 1, RangeRevision: 1, StartSequence: &start, MemberState: "live"}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/broadcast-signal"):
			calls++
			var in struct {
				CloseIfIdle bool `json:"closeIfIdle"`
			}
			if e := json.NewDecoder(r.Body).Decode(&in); e != nil || in.CloseIfIdle {
				t.Error("Stop must recover without new cutoff", e)
			}
			json.NewEncoder(w).Encode(assetclient.BroadcastSignal{State: "ended", EndSequenceExclusive: &frozen})
		case strings.HasSuffix(r.URL.Path, "/broadcast-range"):
			json.NewDecoder(r.Body).Decode(&policy)
			json.NewEncoder(w).Encode(policy)
		case strings.HasSuffix(r.URL.Path, "/broadcast-projection"):
			duration := 90.09
			zero := 0.0
			json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: policy.Epoch, RangeRevision: policy.RangeRevision, State: "ready", StartSequence: policy.StartSequence, EndSequenceExclusive: policy.EndSequenceExclusive, DurationSeconds: &duration, MediaStartSeconds: &zero, Revision: 1})
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	service := recordings.NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	if e = service.ReconcileBroadcasts(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	got, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || calls != 1 || got.View.Boundaries.EndSequenceExclusive == nil || *got.View.Boundaries.EndSequenceExclusive != frozen {
		t.Fatalf("lost frozen boundary: %+v calls=%d %v", got.View.Boundaries, calls, e)
	}
	if e = service.ReconcileBroadcasts(context.Background(), 100); e != nil {
		t.Fatal(e)
	}
	got, e = repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || *got.View.Boundaries.EndSequenceExclusive != frozen || got.EndedAt == nil {
		t.Fatalf("end did not settle %+v %v", got, e)
	}
}
