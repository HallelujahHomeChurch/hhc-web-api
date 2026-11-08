package diagnostics

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace"
)

func TestDatabaseEvidenceSurvivesFilteringAndUnsampledContext(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelError}))
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)
	RecordDatabaseOutcome(ctx, logger, "db_query_lifecycle", time.Millisecond, false)
	RecordDatabaseOutcome(ctx, logger, "secret-sql", time.Second, true)
	if output.Len() != 0 {
		t.Fatal("fast success or unknown phase was emitted")
	}
	RecordDatabaseOutcome(ctx, logger, "db_connect", time.Millisecond, true)
	RecordDatabaseOutcome(ctx, logger, "db_query_lifecycle", 300*time.Millisecond, false)
	for _, expected := range []string{sc.TraceID().String(), `"phase":"db_connect"`, `"outcome":"error"`, `"phase":"db_query_lifecycle"`, `"outcome":"slow"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing retained evidence %q", expected)
		}
	}
}

func TestRequestAccumulatorConcurrentAndClosed(t *testing.T) {
	ctx, observation := WithRequest(context.Background(), "synthetic")
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordDatabaseOutcome(ctx, slog.Default(), "db_query_lifecycle", 100*time.Millisecond, false)
		}()
	}
	wg.Wait()
	values, failed := observation.Finish()
	if failed || len(values) != 1 || values[0].Count != 32 || values[0].TotalMS != 3200 || values[0].MaxMS != 100 {
		t.Fatal(values, failed)
	}
	RecordRequestPhase(ctx, "db_query_lifecycle", time.Second, true)
	next, _ := observation.Finish()
	if next[0].Count != 32 {
		t.Fatal("late operation counted")
	}
}
