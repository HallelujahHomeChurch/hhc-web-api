package diagnostics

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// RecordDatabaseOutcome is independent of span sampling and normal log verbosity.
// Query lifecycle includes result consumption; it is not server-only SQL time.
func RecordDatabaseOutcome(ctx context.Context, logger *slog.Logger, phase string, elapsed time.Duration, failed bool) {
	RecordRequestPhase(ctx, phase, elapsed, failed)
	if !failed && elapsed < 250*time.Millisecond {
		return
	}
	switch phase {
	case "db_connect", "db_reset", "db_query_lifecycle":
	default:
		return
	}
	outcome := "slow"
	if failed {
		outcome = "error"
	}
	r := slog.NewRecord(time.Now(), slog.LevelInfo, "database_diagnostic", 0)
	correlation := "uncorrelated"
	if RequestID(ctx) != "" {
		correlation = "request"
	}
	r.Add("request_id", RequestID(ctx), "operation", "database", "correlation", correlation, "phase", phase, "outcome", outcome, "elapsed_ms", float64(elapsed)/float64(time.Millisecond), "context_cancelled", ctx.Err() != nil)
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.Add("trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
	}
	_ = logger.Handler().Handle(ctx, r)
}
