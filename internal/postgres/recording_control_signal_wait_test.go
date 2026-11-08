package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestControlSignalWaitDoesNotBlockStopOrViewers(t *testing.T) {
	for _, change := range []string{"stop", "epoch", "revoke", "revision", "public-end", "source", "cancel", "timeout", "delete", "unchanged"} {
		t.Run(change, func(t *testing.T) { testControlSignalWait(t, change, "control") })
	}
}
func TestHealthSignalWaitDoesNotBlockStopOrViewers(t *testing.T) {
	for _, change := range []string{"stop", "epoch", "revoke", "revision", "public-end", "source", "capture", "actor", "expiry", "progress", "cancel", "unchanged", "timeout"} {
		t.Run(change, func(t *testing.T) { testControlSignalWait(t, change, "health") })
	}
}
func TestBackgroundSignalWaitDoesNotBlockStopOrViewers(t *testing.T) {
	for _, change := range []string{"stop", "epoch", "revoke", "revision", "public-end", "source", "capture", "actor", "expiry", "progress", "cancel", "unchanged"} {
		t.Run(change, func(t *testing.T) { testControlSignalWait(t, change, "background") })
	}
}
func testControlSignalWait(t *testing.T, change, mode string) {
	repo, b, ctx, now := broadcastFixture(t)
	repo.db.SetMaxOpenConns(10)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	b, c, err := repo.PrepareBroadcastBinding(ctx, b.View.RecordingID, actor.ActorID, recordings.BroadcastBindInput{ProtocolVersion: recordings.BroadcastProtocolVersion, BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: "control-signal-wait", ExpectedRevision: b.View.Revision}}, now)
	if err != nil {
		t.Fatal(err)
	}
	capture := strings.Repeat("d", 32)
	_, err = repo.UpdateCapture(ctx, c.IntentID, func(c *recordings.Capture) error {
		c.Status.CaptureID = capture
		c.Status.State = "uploading"
		c.RecoveryVersion = recordings.BroadcastRecoveryVersion
		c.Status.Progress.LastAdvancedAt = &now
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET source_managed=true WHERE recording_id=$1`, b.View.RecordingID); err != nil {
		t.Fatal(err)
	}
	_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		start := 0
		reason := "signal_grace"
		b.StartedAt = &now
		b.View.Phase = "live"
		b.View.Announced = true
		b.MemberState = "live"
		b.RangePending = false
		b.RangeApplied = b.View.Boundaries.Revision
		b.View.Policy.AutoStart = true
		b.View.Policy.AutoEnd = true
		b.View.Binding = &recordings.BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: actor.ActorID}
		b.View.Boundaries.StartSequence = &start
		b.View.Health = recordings.BroadcastHealth{State: "interrupted", Reason: &reason}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	start := 0
	projection := assetclient.BroadcastProjection{Epoch: b.Epoch, RangeRevision: b.View.Boundaries.Revision, State: "ready", StartSequence: &start, Revision: 1}
	var projectionMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "background" && strings.HasSuffix(r.URL.Path, "/broadcast-range") {
			var in assetclient.BroadcastRangeInput
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
				return
			}
			projectionMu.Lock()
			projection.Epoch, projection.RangeRevision = in.Epoch, in.RangeRevision
			projection.StartSequence, projection.EndSequenceExclusive = in.StartSequence, in.EndSequenceExclusive
			if in.Revoked {
				projection.State = "failed"
			}
			projectionMu.Unlock()
			json.NewEncoder(w).Encode(in)
			return
		}
		if mode == "background" && strings.HasSuffix(r.URL.Path, "/broadcast-projection") {
			projectionMu.Lock()
			defer projectionMu.Unlock()
			json.NewEncoder(w).Encode(projection)
			return
		}
		var in struct {
			CloseIfIdle bool `json:"closeIfIdle"`
		}
		if !strings.HasSuffix(r.URL.Path, "/broadcast-signal") || json.NewDecoder(r.Body).Decode(&in) != nil || in.CloseIfIdle != (mode == "background") {
			t.Error("unexpected signal observation mode")
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(assetclient.BroadcastSignal{RecoveryVersion: recordings.BroadcastRecoveryVersion, State: "healthy", LastMediaConfirmedAt: &now})
	}))
	defer server.Close()
	defer unblock()
	svc := recordings.NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var err error
		if mode == "health" {
			_, err = svc.GetBroadcast(runCtx, b.View.RecordingID)
		} else if mode == "background" {
			err = svc.ReconcileBroadcasts(runCtx, 100)
		} else {
			_, err = svc.GetBroadcastControlD2(runCtx, b.View.RecordingID, actor.ActorID, capture, 1)
		}
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("signal not reached: %v", err)
	case <-runCtx.Done():
		t.Fatal(runCtx.Err())
	}

	if stats := repo.db.Stats(); stats.InUse != 0 {
		t.Fatalf("signal retained SQL connection: %+v", stats)
	}
	repo.transactionMu.Lock()
	admitted := repo.transactions
	repo.transactionMu.Unlock()
	if admitted != 0 {
		t.Fatalf("snapshot rollback leaked %d permits", admitted)
	}
	probe, stop := context.WithTimeout(ctx, time.Second)
	err = svc.RecordPlaybackHeartbeat(probe, b.View.RecordingID, platform.NewID(), recordings.PlaybackHeartbeat{PlaybackSessionID: platform.NewID(), Sequence: 1, State: "playing"})
	stop()
	if err != nil {
		t.Errorf("heartbeat blocked by signal observation: %v", err)
	}
	probe, stop = context.WithTimeout(ctx, time.Second)
	switch change {
	case "stop":
		_, err = repo.UpdateCapture(probe, c.IntentID, func(c *recordings.Capture) error { c.Status.StopAcceptedAt = &now; return nil })
	case "capture", "actor", "expiry", "progress":
		_, err = repo.UpdateCapture(probe, c.IntentID, func(c *recordings.Capture) error {
			switch change {
			case "capture":
				c.Status.CaptureID = strings.Repeat("e", 32)
			case "actor":
				c.ActorID = platform.NewID()
			case "expiry":
				c.Status.ExpiresAt = now.Add(-time.Second)
			case "progress":
				c.Status.Progress.Revision++
			}
			return nil
		})
	case "cancel":
		cancel()
	case "unchanged", "timeout":
	case "source":
		_, err = repo.db.ExecContext(probe, `UPDATE hhc_web.member_recording_broadcast SET source_managed=false WHERE recording_id=$1`, b.View.RecordingID)
	case "delete":
		err = repo.Delete(probe, b.View.RecordingID, 1, actor.ActorID, now, true)
	default:
		_, err = repo.UpdateBroadcast(probe, b.View.RecordingID, "", now, func(_ context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
			switch change {
			case "epoch":
				b.Epoch++
			case "revoke":
				b.Revoked = true
			case "revision":
				b.View.Revision++
			case "public-end":
				end := 3
				b.PublicEnd = &recordings.BroadcastPublicEnd{Reason: "staff", EndSequenceExclusive: &end}
			}
			return nil
		})
	}
	stop()
	if err != nil {
		t.Errorf("authority change blocked by signal observation: %v", err)
	}
	if change != "cancel" && change != "timeout" {
		if err := runCtx.Err(); err != nil {
			t.Fatalf("signal context expired before probes completed: %v", err)
		}
		select {
		case err := <-done:
			t.Fatalf("signal completed before release: %v", err)
		default:
		}
	}
	t.Logf("pool while signal pending: %+v", repo.db.Stats())
	if change == "timeout" {
		err = <-done
		unblock()
	} else {
		unblock()
		err = <-done
	}
	if change == "cancel" {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	} else if change == "epoch" && mode == "control" {
		if !errors.Is(err, recordings.ErrBroadcastEpoch) {
			t.Fatalf("epoch: %v", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetBroadcast(ctx, b.View.RecordingID)
	if change == "delete" {
		if !errors.Is(err, recordings.ErrNotFound) {
			t.Fatalf("deleted authority: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	want := "interrupted"
	if change == "unchanged" {
		want = "healthy"
	}
	if got.View.Health.State != want {
		t.Fatalf("signal crossed %s authority change: got %s", change, got.View.Health.State)
	}
}
