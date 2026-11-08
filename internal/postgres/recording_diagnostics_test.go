package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestBroadcastDiagnosticsDistinguishPoolAndRowWait(t *testing.T) {
	for _, pool := range []bool{true, false} {
		t.Run(map[bool]string{true: "pool", false: "row"}[pool], func(t *testing.T) {
			repo, b, ctx, now := broadcastFixture(t)
			warmRecordingPool(t, repo.db)
			spans := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
			previous := otel.GetTracerProvider()
			otel.SetTracerProvider(provider)
			defer otel.SetTracerProvider(previous)
			defer provider.Shutdown(context.Background())
			if pool {
				repo.db.SetMaxOpenConns(2)
				// Model other pool consumers outside recording admission. A size-one
				// recording pool intentionally rejects before checkout after PR306.
				heldConnection, e := repo.db.Conn(ctx)
				if e != nil {
					t.Fatal(e)
				}
				defer heldConnection.Close()
			}
			held, err := repo.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer held.Rollback()
			if !pool {
				if _, err = held.ExecContext(ctx, `SELECT id FROM hhc_web.member_recording WHERE id::text=$1 FOR UPDATE`, b.View.RecordingID); err != nil {
					t.Fatal(err)
				}
			}
			observed := diagnostics.WithRecorder(ctx, &diagnostics.Recorder{})
			bounded, cancel := context.WithTimeout(observed, 500*time.Millisecond)
			defer cancel()
			called := false
			_, err = repo.UpdateBroadcast(bounded, b.View.RecordingID, "", now, func(context.Context, *recordings.Broadcast, *recordings.Recording, *recordings.Capture) error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatal("blocked operation did not stop before callback")
			}
			expected := "cms.broadcast_lock"
			if pool {
				expected = "cms.broadcast_source_tx"
			}
			found := false
			for _, span := range spans.Ended() {
				if span.Name() == expected {
					found = true
					if attrs := span.Attributes(); len(attrs) != 1 || attrs[0].Value.AsString() != "cancelled" {
						t.Fatal("wait cancellation not classified")
					}
					if span.EndTime().Sub(span.StartTime()) < 50*time.Millisecond {
						t.Fatal("missing measured wait")
					}
				}
			}
			if !found {
				t.Fatalf("missing wait stage %s", expected)
			}
			if pool && repo.db.Stats().WaitCount == 0 {
				t.Fatal("pool wait not reflected in DBStats")
			}
		})
	}
}
