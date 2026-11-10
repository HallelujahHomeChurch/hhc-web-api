package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func runRecordingBroadcasts(ctx context.Context, recordingService *recordings.Service) {

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		runCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		if err := recordingService.ReconcileBroadcasts(runCtx, 100); err != nil && ctx.Err() == nil {
			slog.Error("recording broadcast reconciliation failed", "error", err)
		}
		cancel()
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		if err := recordingService.ReconcileLiveCovers(runCtx, 10); err != nil && ctx.Err() == nil {
			slog.Error("recording live cover reconciliation failed", "error", err)
		}
		cancel()
		runCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
		if err := recordingService.RollupPlaybackMetrics(runCtx); err != nil && ctx.Err() == nil {
			slog.Error("recording metrics rollup failed", "error", err)
		}
		cancel()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Asset exclusively owns expiry and irreversible cleanup. CMS must not
// unpublish from a stale local deadline while a policy is being extended.
func runRecordingCaptures(ctx context.Context, recordingService *recordings.Service) {

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := recordingService.ReconcileCaptures(runCtx, 100); err != nil && ctx.Err() == nil {
			slog.Error("recording capture reconciliation failed", "error", err)
		}
		cancel()
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		if err := recordingService.ReconcileDeletions(runCtx); err != nil && ctx.Err() == nil {
			slog.Error("recording deletion reconciliation failed", "error", err)
		}
		cancel()
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		if err := recordingService.ReconcileCoverReferences(runCtx); err != nil && ctx.Err() == nil {
			slog.Error("recording cover reference reconciliation failed", "error", err)
		}
		cancel()
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		if err := recordingService.ReconcileCompletions(runCtx); err != nil && ctx.Err() == nil {
			slog.Error("recording completion reconciliation failed", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func runRecordingLifecycle(ctx context.Context, service *recordings.Service) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		result, err := service.ReconcileLifecycle(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Error("recording lifecycle reconciliation failed")
		}
		if result.Outstanding > 0 {
			slog.Warn("recording lifecycle repair pending", "count", result.Outstanding, "oldest_failure_at", result.OldestFailureAt)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
