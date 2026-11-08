package main

import (
	"context"
	"log/slog"
	"sync"
)

// apiWorkers owns the API process's existing background loops. Optional recording
// loops are configured together only when recording support is enabled.
type apiWorkers struct {
	publication              func(context.Context) error
	identityProjection       func(context.Context)
	recordingAutoPublication func(context.Context)
	recordingBroadcasts      func(context.Context)
	recordingCaptures        func(context.Context)
}

func (w apiWorkers) start(ctx context.Context, stopProcess func()) func() {
	ctx, cancel := context.WithCancel(ctx)
	var running sync.WaitGroup
	running.Add(1)
	go func() {
		defer running.Done()
		if err := w.publication(ctx); err != nil {
			slog.Error("publication worker stopped", "error", err)
			cancel()
			stopProcess()
		}
	}()
	for _, run := range []func(context.Context){w.identityProjection, w.recordingAutoPublication, w.recordingBroadcasts, w.recordingCaptures} {
		if run == nil {
			continue
		}
		running.Add(1)
		go func() {
			defer running.Done()
			run(ctx)
		}()
	}
	return func() {
		cancel()
		running.Wait()
	}
}
