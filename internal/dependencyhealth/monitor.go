// Package dependencyhealth monitors dependencies without controlling application readiness.
package dependencyhealth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// Launch starts one bounded, non-overlapping monitor and returns a cancel-and-join function.
func Launch(parent context.Context, db *sql.DB, port int, logger *slog.Logger) func() {
	stopWatchdog := startWatchdog(parent, logger)
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	token := os.Getenv("DAPR_API_TOKEN")
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		var dbState, daprState observationState
		for {
			if ctx.Err() != nil {
				return
			}
			observe(ctx, logger, "db", func(check context.Context) (string, int, error) { return ping(check, db) }, &dbState)
			if port > 0 && ctx.Err() == nil {
				observe(ctx, logger, "dapr_outbound", func(check context.Context) (string, int, error) { return dapr(check, client, port, token) }, &daprState)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { stopWatchdog(); cancel(); <-done; client.CloseIdleConnections() }
}

func observe(parent context.Context, logger *slog.Logger, dependency string, check func(context.Context) (string, int, error), state *observationState) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	started := time.Now()
	phase, status, err := check(ctx)
	outcome := "ok"
	level := slog.LevelInfo
	if err != nil {
		outcome = "unavailable"
		level = slog.LevelWarn
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
			outcome = "deadline"
		}
		if errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled {
			outcome = "cancelled"
		}
	}
	if !state.emit(time.Now(), outcome+"/"+phase+"/"+fmt.Sprint(status)) {
		return
	}
	logger.Log(parent, level, "dependency_health", "dependency", dependency, "phase", phase, "outcome", outcome, "status", status, "elapsed_ms", time.Since(started).Milliseconds())
}

func ping(ctx context.Context, db *sql.DB) (string, int, error) {
	if db == nil {
		return "configuration", 0, errors.New("database not configured")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return "acquire", 0, err
	}
	defer conn.Close()
	return "ping", 0, conn.PingContext(ctx)
}

func dapr(ctx context.Context, client *http.Client, port int, token string) (string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/v1.0/healthz/outbound", port), nil)
	if err != nil {
		return "request", 0, err
	}
	req.Header.Set("dapr-api-token", token)
	response, err := client.Do(req)
	if err != nil {
		return "response_headers", 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return "response_status", response.StatusCode, errors.New("sidecar unavailable")
	}
	return "complete", response.StatusCode, nil
}

// Each dependency retains its last emitted state; sampling still runs every 30 seconds.
type observationState struct {
	last    string
	emitted time.Time
}

func (s *observationState) emit(now time.Time, state string) bool {
	if s.last == state && !s.emitted.IsZero() && now.Sub(s.emitted) < 5*time.Minute {
		return false
	}
	s.last, s.emitted = state, now
	return true
}
