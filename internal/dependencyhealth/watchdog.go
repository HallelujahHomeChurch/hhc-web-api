package dependencyhealth

import (
	"context"
	"log/slog"
	"runtime/metrics"
	"time"
)

// The independent timer excludes dependency-check duration. Lateness indicates
// delayed process scheduling, not proof of CPU throttling, GC, or ingress delay.
func startWatchdog(parent context.Context, logger *slog.Logger) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var last time.Time
		samples := []metrics.Sample{{Name: "/gc/pauses:seconds"}}
		var previous uint64
		metrics.Read(samples)
		for _, n := range samples[0].Value.Float64Histogram().Counts {
			previous += n
		}
		for {
			select {
			case <-ctx.Done():
				return
			case scheduled := <-ticker.C:
				now := time.Now()
				metrics.Read(samples)
				var count uint64
				for _, n := range samples[0].Value.Float64Histogram().Counts {
					count += n
				}
				if now.Sub(scheduled) >= 250*time.Millisecond && (last.IsZero() || now.Sub(last) >= 30*time.Second) {
					r := slog.NewRecord(now, slog.LevelWarn, "runtime_schedule_delay", 0)
					r.Add("timer_lag_ms", now.Sub(scheduled).Milliseconds(), "gc_pause_count_delta", count-previous)
					_ = logger.Handler().Handle(ctx, r)
					last = now
				}
				previous = count
			}
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
	}
}
