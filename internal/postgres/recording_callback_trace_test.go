package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBroadcastControlParentsAssetSpan(t *testing.T) {
	for _, mutation := range []bool{false, true} {
		t.Run(fmt.Sprintf("mutation_%t", mutation), func(t *testing.T) { testBroadcastCallbackParent(t, mutation) })
	}
}
func testBroadcastCallbackParent(t *testing.T, mutation bool) {
	repo, b, ctx, now := subjectLiveFixture(t)
	_, err := repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(ctx context.Context, b *recordings.Broadcast, _ *recordings.Recording, c *recordings.Capture) error {
		b.SourceManaged = true
		b.View.Policy.AutoStart = true
		b.StartedAt = &now
		start := 0
		b.View.Boundaries.StartSequence = &start
		reason := "signal_grace"
		b.View.Health.State = "interrupted"
		b.View.Health.Reason = &reason
		c.RecoveryVersion = recordings.BroadcastRecoveryVersion
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx, `UPDATE hhc_web.member_recording_broadcast SET source_managed=true WHERE recording_id=$1`, b.View.RecordingID); err != nil {
		t.Fatal(err)
	}
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(previous)
	defer provider.Shutdown(context.Background())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mutation {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(assetclient.BroadcastSignal{State: "healthy", RecoveryVersion: recordings.BroadcastRecoveryVersion})
	}))
	defer server.Close()
	svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	ctx = diagnostics.WithRecorder(ctx, &diagnostics.Recorder{})
	ctx, parent := provider.Tracer("test").Start(ctx, "request")
	if mutation {
		_, err = svc.ControlBroadcast(ctx, b.View.RecordingID, b.View.Binding.ActorID, "emergency-close", "", recordings.BroadcastMutationInput{OperationKey: "trace-emergency", ExpectedRevision: b.View.Revision}, "test")
	} else {
		_, err = svc.GetBroadcastControlD2(ctx, b.View.RecordingID, b.View.Binding.ActorID, b.View.Binding.CaptureID, b.Epoch)
	}
	parent.End()
	if err != nil {
		t.Fatal(err)
	}
	var callback, dependency sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if mutation && s.Name() == "cms.broadcast_callback" || !mutation && s.Name() == "cms.signal_reconcile" {
			callback = s
		}
		if s.Name() == "asset.http" {
			dependency = s
		}
	}
	if mutation {
		current, err := repo.GetBroadcast(ctx, b.View.RecordingID)
		if err != nil || !current.Revoked || dependency != nil {
			t.Fatalf("emergency close must persist without Asset I/O: revoked=%t dependency=%v err=%v", current.Revoked, dependency, err)
		}
		return
	}
	if callback == nil || dependency == nil {
		t.Fatalf("missing production seam spans: parent=%v dependency=%v", callback, dependency)
	}
	if dependency.Parent().SpanID() != callback.SpanContext().SpanID() {
		t.Fatalf("asset span parent %s != expected stage %s", dependency.Parent().SpanID(), callback.SpanContext().SpanID())
	}
	if callback.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatal("control stage lost request parent")
	}
}

func TestBroadcastCallbackContextPreservesCancellationAndRollback(t *testing.T) {
	repo, b, ctx, now := broadcastFixture(t)
	ctx = diagnostics.WithRecorder(ctx, &diagnostics.Recorder{})
	request, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	deadline, _ := request.Deadline()
	_, err := repo.UpdateBroadcast(request, b.View.RecordingID, "", now, func(callback context.Context, b *recordings.Broadcast, _ *recordings.Recording, _ *recordings.Capture) error {
		if got, ok := callback.Deadline(); !ok || !got.Equal(deadline) {
			t.Fatal("callback deadline changed")
		}
		b.View.Phase = "cancelled"
		cancel()
		<-callback.Done()
		return callback.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	current, err := repo.GetBroadcast(ctx, b.View.RecordingID)
	if err != nil {
		t.Fatal(err)
	}
	if current.View.Phase != b.View.Phase {
		t.Fatal("cancelled callback committed mutation")
	}
	_, err = repo.UpdateBroadcast(ctx, b.View.RecordingID, "", now, func(context.Context, *recordings.Broadcast, *recordings.Recording, *recordings.Capture) error {
		return nil
	})
	if err != nil {
		t.Fatalf("transaction permit/lock not released: %v", err)
	}
}
