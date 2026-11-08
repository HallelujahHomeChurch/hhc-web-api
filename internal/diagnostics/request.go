package diagnostics

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

var requestPhases = [...]string{"db_connect", "db_reset", "db_query_lifecycle", "lifecycle", "list_query", "package_calls", "projection", "serialization", "asset_headers", "asset_body", "asset_attempt"}

type requestPhase struct {
	count      uint64
	total, max time.Duration
	failures   uint64
	status     int
}
type Request struct {
	mu             sync.Mutex
	values         [len(requestPhases)]requestPhase
	closed, failed bool
	id             string
	reason         string
}
type PhaseSummary struct {
	Phase    string  `json:"phase"`
	Count    uint64  `json:"count"`
	TotalMS  float64 `json:"total_ms"`
	MaxMS    float64 `json:"max_ms"`
	Failures uint64  `json:"failures"`
	Status   int     `json:"status,omitempty"`
}
type requestContextKey struct{}

func WithRequest(ctx context.Context, id string) (context.Context, *Request) {
	r := &Request{id: id}
	return context.WithValue(ctx, requestContextKey{}, r), r
}
func RequestID(ctx context.Context) string {
	if r, _ := ctx.Value(requestContextKey{}).(*Request); r != nil {
		return r.id
	}
	return ""
}

// Work totals may overlap parent/child or parallel operations; never sum as wall time.
func RecordRequestPhase(ctx context.Context, phase string, elapsed time.Duration, failed bool) {
	RecordHTTPPhase(ctx, phase, elapsed, 0, failed)
}
func RecordHTTPPhase(ctx context.Context, phase string, elapsed time.Duration, status int, failed bool) {
	r, _ := ctx.Value(requestContextKey{}).(*Request)
	if r == nil {
		return
	}
	for i, name := range requestPhases {
		if phase != name {
			continue
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.closed {
			return
		}
		v := &r.values[i]
		v.count++
		if failed {
			v.failures++
			r.reason = name + "_failed"
		}
		if status >= 100 && status <= 599 {
			v.status = status
		}
		v.total += max(0, elapsed)
		v.max = max(v.max, elapsed)
		r.failed = r.failed || failed
		return
	}
}
func StartRequestPhase(ctx context.Context, phase string) func() {
	started := time.Now()
	var once sync.Once
	return func() { once.Do(func() { RecordRequestPhase(ctx, phase, time.Since(started), false) }) }
}
func (r *Request) Finish() ([]PhaseSummary, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	out := make([]PhaseSummary, 0, len(requestPhases))
	for i, v := range r.values {
		if v.count > 0 {
			out = append(out, PhaseSummary{requestPhases[i], v.count, float64(v.total) / float64(time.Millisecond), float64(v.max) / float64(time.Millisecond), v.failures, v.status})
		}
	}
	return out, r.failed
}

func (r *Request) FailureReason() string { r.mu.Lock(); defer r.mu.Unlock(); return r.reason }

// Groups keep named numeric fields under JSON and production text handlers.
func PhaseLogValue(values []PhaseSummary) slog.Value {
	attrs := make([]slog.Attr, 0, len(values))
	for _, v := range values {
		attrs = append(attrs, slog.Group(v.Phase, "count", v.Count, "total_ms", v.TotalMS, "max_ms", v.MaxMS, "failures", v.Failures, "status", v.Status))
	}
	return slog.GroupValue(attrs...)
}
