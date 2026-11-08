// Package diagnostics keeps bounded, process-local contention summaries.
package diagnostics

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type Stage uint8

const (
	ProjectionPoll Stage = iota
	SignalReconcile
	BroadcastSourceTx
	BroadcastLock
	BroadcastCallback
	BroadcastCommit
	stageCount
)

var stageNames = [stageCount]string{"projection_poll", "signal_reconcile", "broadcast_source_tx", "broadcast_lock", "broadcast_callback", "broadcast_commit"}

type contextKey struct{}

// Columns: count, sum_ms, max_ms, errors, cancelled, stale, <=10ms, <=100ms,
// <=1000ms, >1000ms. Buckets are exclusive. Saturation is explicitly reported.
type counters [10]uint32

type Recorder struct {
	mu        sync.Mutex
	values    [stageCount]counters
	saturated bool
}

func WithRecorder(ctx context.Context, r *Recorder) context.Context {
	return context.WithValue(ctx, contextKey{}, r)
}
func (r *Recorder) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		next.ServeHTTP(w, req.WithContext(WithRecorder(req.Context(), r)))
	})
}

// Reconciliation gives background stages a shared trace using the existing sampler.
func Reconciliation(ctx context.Context) (context.Context, func()) {
	if r, _ := ctx.Value(contextKey{}).(*Recorder); r == nil {
		return ctx, func() {}
	}
	ctx, span := otel.Tracer("hhc-web-api/diagnostics").Start(ctx, "cms.broadcast_reconcile")
	return ctx, func() { span.End() }
}

// Start is a no-op without an attached recorder. Errors are classified, never exported.
func Start(ctx context.Context, stage Stage) (context.Context, func(error)) {
	r, _ := ctx.Value(contextKey{}).(*Recorder)
	if r == nil || stage >= stageCount {
		return ctx, func(error) {}
	}
	ctx, span := otel.Tracer("hhc-web-api/diagnostics").Start(ctx, "cms."+stageNames[stage])
	started := time.Now()
	return ctx, func(err error) {
		elapsed := time.Since(started)
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		cancelled := err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil)
		if cancelled {
			outcome = "cancelled"
		}
		r.observe(stage, elapsed, err != nil, cancelled)
		span.SetAttributes(attribute.String("outcome", outcome))
		span.End()
	}
}
func Discard(ctx context.Context, stage Stage) {
	r, _ := ctx.Value(contextKey{}).(*Recorder)
	if r == nil || stage >= stageCount {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.add(&r.values[stage][5], 1)
}
func (r *Recorder) add(dst *uint32, n uint64) {
	if n > uint64(math.MaxUint32-*dst) {
		*dst = math.MaxUint32
		r.saturated = true
	} else {
		*dst += uint32(n)
	}
}
func (r *Recorder) observe(stage Stage, elapsed time.Duration, failed, cancelled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := &r.values[stage]
	ms := uint64(max(0, elapsed.Milliseconds()))
	r.add(&v[0], 1)
	r.add(&v[1], ms)
	if ms > uint64(v[2]) {
		v[2] = uint32(min(ms, math.MaxUint32))
		if ms > math.MaxUint32 {
			r.saturated = true
		}
	}
	if failed {
		r.add(&v[3], 1)
	}
	if cancelled {
		r.add(&v[4], 1)
	}
	bucket := 9
	switch {
	case elapsed <= 10*time.Millisecond:
		bucket = 6
	case elapsed <= 100*time.Millisecond:
		bucket = 7
	case elapsed <= time.Second:
		bucket = 8
	}
	r.add(&v[bucket], 1)
}
func (r *Recorder) summary() (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make(map[string]counters, stageCount)
	for stage, name := range stageNames {
		values[name] = r.values[stage]
	}
	raw, _ := json.Marshal(values) // Fixed numeric arrays and static keys cannot fail.
	saturated := r.saturated
	r.values = [stageCount]counters{}
	r.saturated = false
	return string(raw), saturated
}

// Run only writes through the existing logger, independently of request execution.
// Cancellation discards the partial window rather than waiting for a final flush.
func (r *Recorder) Run(ctx context.Context, stats func() sql.DBStats, logger *slog.Logger, release string) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	r.run(ctx, stats, logger, release, ticker.C, time.Now)
}
func (r *Recorder) run(ctx context.Context, stats func() sql.DBStats, logger *slog.Logger, release string, ticks <-chan time.Time, clock func() time.Time) {
	if len(release) != 40 {
		release = "unknown"
	} else if _, err := hex.DecodeString(release); err != nil {
		release = "unknown"
	}
	since := clock()
	previous, summaries := stats(), since
	initial := true
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			now := clock()
			if ctx.Err() != nil {
				return
			}
			current := stats()
			reset := current.WaitCount < previous.WaitCount || current.WaitDuration < previous.WaitDuration
			waits, waitMS := current.WaitCount-previous.WaitCount, (current.WaitDuration - previous.WaitDuration).Milliseconds()
			if reset {
				waits, waitMS = current.WaitCount, current.WaitDuration.Milliseconds()
			}
			logger.InfoContext(ctx, "cms_db_pool", "release", release, "interval_ms", max(0, now.Sub(since).Milliseconds()), "max", current.MaxOpenConnections, "open", current.OpenConnections, "in_use", current.InUse, "idle", current.Idle, "waits", waits, "wait_ms", waitMS, "reset", reset, "initial", initial)
			initial = false
			if ctx.Err() != nil {
				return
			}
			previous, since = current, now
			if now.Sub(summaries) >= time.Minute {
				data, saturated := r.summary()
				logger.InfoContext(ctx, "cms_stages", "release", release, "interval_ms", now.Sub(summaries).Milliseconds(), "saturated", saturated, "data", data)
				summaries = now
			}
		}
	}
}

// Launch starts one logger worker. Shutdown is bounded even if the log sink stalls;
// requests never wait for that sink. The stalled worker exits when its write returns.
func Launch(ctx context.Context, stats func() sql.DBStats, logger *slog.Logger, release string) (*Recorder, func()) {
	r := &Recorder{}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); r.Run(runCtx, stats, logger, release) }()
	return r, func() {
		cancel()
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
	}
}
