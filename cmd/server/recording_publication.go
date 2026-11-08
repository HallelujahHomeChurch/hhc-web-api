package main

import (
	"context"
	"log/slog"
	"time"
)

func runRecordingAutoPublication(ctx context.Context, reconcile func(context.Context, int) error) {
	// Publication must not wait for the five-minute capture/cleanup batch.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := reconcile(runCtx, 100); err != nil && ctx.Err() == nil {
			slog.Error("recording auto publication reconciliation failed", "error", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
