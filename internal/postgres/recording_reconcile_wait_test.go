package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestReconcileProjectionDoesNotBlockViewerOperations(t *testing.T) {
	for _, replicas := range []int{1, 2} {
		t.Run(fmt.Sprint(replicas), func(t *testing.T) { testReconcileProjectionViewerOperations(t, replicas) })
	}
}

func testReconcileProjectionViewerOperations(t *testing.T, replicas int) {
	repo, b, ctx, now := broadcastFixture(t)
	repo.db.SetMaxOpenConns(10)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	b, c, err := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, recordings.BroadcastBindInput{ProtocolVersion: recordings.BroadcastProtocolVersion, BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "bind-slow-projection", ExpectedRevision: b.View.Revision}}, now)
	if err != nil {
		t.Fatal(err)
	}
	capture := strings.Repeat("e", 32)
	start := 0
	if _, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = capture
		c.Status.State = "uploading"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, r *recordings.Recording, c *recordings.Capture) error {
		b.StartedAt = &now
		b.View.Phase = "live"
		b.View.Announced = true
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: actor.ActorID}
		b.View.Boundaries.StartSequence = &start
		b.RangePending = false
		b.RangeApplied = b.View.Boundaries.Revision
		b.MemberState = "live"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/broadcast-projection") {
			t.Errorf("unexpected asset route %s", r.URL.Path)
			w.WriteHeader(503)
			return
		}
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(assetclient.BroadcastProjection{Epoch: b.Epoch, RangeRevision: b.View.Boundaries.Revision, State: "ready", StartSequence: &start, Revision: 1})
	}))
	defer server.Close()
	defer unblock()
	svc := recordings.NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	runCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- svc.ReconcileBroadcasts(runCtx, 100) }()
	select {
	case <-entered:
	case err := <-done:
		unblock()
		t.Fatalf("reconcile never reached projection: %v", err)
	case <-runCtx.Done():
		unblock()
		t.Fatal(runCtx.Err())
	}
	var secondDone chan error
	if replicas == 2 {
		secondDB, e := sql.Open("pgx", os.Getenv("HHW_TEST_DATABASE_URL"))
		if e != nil {
			t.Fatal(e)
		}
		defer secondDB.Close()
		secondDB.SetMaxOpenConns(10)
		secondRepo := NewRecordingRepository(secondDB).WithAudit(NewAuditOutbox(secondDB))
		secondService := recordings.NewService(secondRepo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
		secondDone = make(chan error, 1)
		go func() { secondDone <- secondService.ReconcileBroadcasts(runCtx, 100) }()
		select {
		case <-entered:
		case e := <-secondDone:
			secondDone = nil
			t.Logf("second reconciler skipped locked work: %v", e)
		case <-runCtx.Done():
			unblock()
			t.Fatal(runCtx.Err())
		}
	}
	for _, operation := range []string{"heartbeat", "metrics"} {
		probeCtx, stop := context.WithTimeout(context.Background(), 150*time.Millisecond)
		began := time.Now()
		if operation == "heartbeat" {
			err = svc.RecordPlaybackHeartbeat(probeCtx, b.View.RecordingID, platform.NewID(), recordings.PlaybackHeartbeat{PlaybackSessionID: platform.NewID(), Sequence: 1, State: "playing"})
		} else {
			_, err = svc.GetPlaybackMetrics(probeCtx, b.View.RecordingID, platform.NewID(), false)
		}
		stop()
		if err != nil {
			t.Errorf("%s blocked by slow reconciliation projection: %v elapsed=%s", operation, err, time.Since(began))
		}
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	_, stopErr := repo.UpdateCapture(stopCtx, c.IntentID, func(current *recordings.Capture) error { current.Status.StopAcceptedAt = &now; return nil })
	stopCancel()
	if stopErr != nil {
		t.Errorf("Stop authority write blocked: %v", stopErr)
	}
	unblock()
	if secondDone != nil {
		if e := <-secondDone; e != nil {
			t.Errorf("second reconcile: %v", e)
		}
	}
	if err = <-done; err != nil {
		t.Errorf("reconcile: %v", err)
	}
	stored, e := repo.GetCapture(ctx, c.IntentID, b.View.RecordingID)
	if e != nil || stored.Status.StopAcceptedAt == nil {
		t.Fatalf("Stop authority lost: %v", e)
	}
	got, e := repo.GetBroadcast(ctx, b.View.RecordingID)
	if e != nil || got.Projection != nil {
		t.Fatalf("stale ready projection applied after Stop: %v", e)
	}
}
