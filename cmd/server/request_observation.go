package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/dbphase"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"go.opentelemetry.io/otel/trace"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

type requestKey struct{}
type observationKey struct{}

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestKey{}).(string); return id }

type requestObservation struct {
	db           *sql.DB
	poolBefore   sql.DBStats
	dbPhases     *dbphase.Observation
	started      time.Time
	ctx          context.Context
	body         *observedBody
	cancelAt     atomic.Int64
	stop         func() bool
	phases       *diagnostics.Request
	once         sync.Once
	route        string
	parentSpanID string
}

func observeRequest(r *http.Request) (*http.Request, *requestObservation) {
	id := r.Header.Get("X-HHC-Request-ID")
	if len(r.Header.Values("X-HHC-Request-ID")) != 1 || !safeRequestID.MatchString(id) {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("request ID entropy unavailable")
		}
		id = hex.EncodeToString(b[:])
	}
	ctx := context.WithValue(r.Context(), requestKey{}, id)
	ctx, phases := diagnostics.WithRequest(ctx, id)
	o := &requestObservation{started: time.Now(), ctx: ctx, phases: phases, route: "other", parentSpanID: trace.SpanContextFromContext(ctx).SpanID().String()}
	if r.Method == "GET" {
		switch r.URL.Path {
		case "/api/member/recordings":
			o.route = "member_recordings_list"
		case "/api/admin/recordings":
			o.route = "admin_recordings_list"
		}
	}
	ctx = context.WithValue(ctx, observationKey{}, o)
	o.ctx = ctx
	o.stop = context.AfterFunc(ctx, func() { o.cancelAt.Store(time.Now().UnixNano()) })
	r = r.WithContext(ctx)
	if r.Body != nil {
		o.body = &observedBody{ReadCloser: r.Body}
		r.Body = o.body
	}
	return r, o
}
func (o *requestObservation) finish(status int, completed bool) {
	o.once.Do(func() { o.finishRequest(status, completed) })
}
func (o *requestObservation) finishRequest(status int, completed bool) {
	o.stop()
	elapsed := time.Since(o.started)
	phases, failed := o.phases.Finish()
	if !failed && completed && status < 400 && elapsed < time.Second && o.ctx.Err() == nil {
		return
	}
	outcome := "ok"
	if status >= 400 {
		outcome = "http_error"
	}
	if !completed {
		outcome = "panic"
	}
	if o.ctx.Err() != nil {
		outcome = "cancelled"
		if errors.Is(o.ctx.Err(), context.DeadlineExceeded) {
			outcome = "deadline"
		}
	}
	sc := trace.SpanContextFromContext(o.ctx)
	reason := o.phases.FailureReason()
	if reason == "" && status >= 400 {
		reason = "http_error"
	}
	if o.ctx.Err() != nil {
		reason = "caller_canceled"
		if errors.Is(o.ctx.Err(), context.DeadlineExceeded) {
			reason = "local_deadline"
		}
	}
	attrs := []any{"failure_reason", reason, "route_id", o.route, "operation", "http_request", "parent_span_id", o.parentSpanID, "span_id", sc.SpanID().String(), "sampled", sc.IsSampled(), "phase_totals", diagnostics.PhaseLogValue(phases), "timing_semantics", "overlapping_work_not_wall_time", "db_checkout", "unmeasured_implicit_checkout", "request_id", RequestID(o.ctx), "trace_id", trace.SpanContextFromContext(o.ctx).TraceID().String(), "status", status, "outcome", outcome, "handler_entry_unix_ms", o.started.UnixMilli(), "handler_elapsed_ms", elapsed.Milliseconds(), "cancel_observed_unix_ms", o.cancelAt.Load() / int64(time.Millisecond)}
	if deadline, ok := o.ctx.Deadline(); ok {
		attrs = append(attrs, "deadline_unix_ms", deadline.UnixMilli())
	}
	if o.body != nil {
		attrs = append(attrs, "body_read_ms", time.Duration(o.body.elapsed.Load()).Milliseconds())
	}
	if o.dbPhases != nil {
		d := o.dbPhases.Snapshot()
		attrs = append(attrs, "db_connect_ms", d.Connect.Milliseconds(), "db_reset_ms", d.Reset.Milliseconds(), "db_connect_count", d.ConnectCount, "db_reset_count", d.ResetCount)
	}
	if o.db != nil {
		s := o.db.Stats()
		attrs = append(attrs, "pool_open", s.OpenConnections, "pool_in_use", s.InUse, "pool_wait_count_delta_all_requests", s.WaitCount-o.poolBefore.WaitCount, "pool_wait_ms_delta_all_requests", (s.WaitDuration - o.poolBefore.WaitDuration).Milliseconds())
	}
	Diagnostic(slog.Default(), o.ctx, "http_request_observation", attrs...)
}

type observedBody struct {
	io.ReadCloser
	elapsed atomic.Int64
}

func (b *observedBody) Read(p []byte) (int, error) {
	started := time.Now()
	n, err := b.ReadCloser.Read(p)
	b.elapsed.Add(int64(time.Since(started)))
	return n, err
}

// Diagnostic bypasses the ordinary verbosity filter for bounded failure/slow records.
func Diagnostic(logger *slog.Logger, ctx context.Context, message string, attrs ...any) {
	r := slog.NewRecord(time.Now(), slog.LevelWarn, message, 0)
	r.Add(attrs...)
	_ = logger.Handler().Handle(ctx, r)
}

type exportWarnings struct {
	mu    sync.Mutex
	last  time.Time
	count uint64
}

func (w *exportWarnings) record(now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.count++
	if w.last.IsZero() || now.Sub(w.last) >= time.Minute {
		Diagnostic(slog.Default(), context.Background(), "trace export failed", "failure_count_total", w.count)
		w.last = now
	}
}
