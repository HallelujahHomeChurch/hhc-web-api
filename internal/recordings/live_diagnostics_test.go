package recordings

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestLivePlaybackFailureDiagnosticsDoNotLogSecrets(t *testing.T) {
	for _, tc := range []struct {
		err   error
		class string
	}{
		{fmt.Errorf("sensitive context: %w", &pgconn.PgError{Code: "53300", Message: "secret-db-password"}), "database_capacity"},
		{fmt.Errorf("secret-signed-url: %w", assetclient.ErrUnavailable), "asset_unavailable"},
		{errors.New("secret-token"), "internal"},
		{ErrTransactionCapacity, "live_capacity"},
	} {
		t.Run(tc.class, func(t *testing.T) {
			var out bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
			defer slog.SetDefault(old)
			ctx := auditclient.WithProvenance(context.Background(), "secret-subject", "request-123")
			logLivePlaybackFailure(ctx, "issue_grant", tc.err)
			s := out.String()
			for _, want := range []string{"recording_live_playback_failed", "issue_grant", tc.class, "request-123"} {
				if !strings.Contains(s, want) {
					t.Fatalf("missing %s: %s", want, s)
				}
			}
			if strings.Contains(s, "secret") || strings.Contains(s, "sensitive") {
				t.Fatalf("sensitive error logged: %s", s)
			}
		})
	}
}

type failedLiveLookup struct {
	Repository
	CaptureRepository
	LiveRepository
	err error
}

func (r *failedLiveLookup) GetLiveCapture(context.Context, string, string) (Capture, error) {
	return Capture{}, r.err
}

func TestLivePlaybackPreservesLookupFailureAndLogsStage(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	defer slog.SetDefault(old)
	failure := &pgconn.PgError{Code: "53300", Message: "secret-dsn"}
	service := NewService(&failedLiveLookup{err: failure}, "https://media.example", bytes.Repeat([]byte("x"), 32), time.Now)
	id := "11111111-1111-4111-8111-111111111111"
	_, err := service.LivePlayback(auditclient.WithProvenance(context.Background(), id, "lookup-123"), id, strings.Repeat("a", 32), id, id)
	if err != failure {
		t.Fatalf("failure changed: %v", err)
	}
	if s := out.String(); !strings.Contains(s, "capture_lookup") || !strings.Contains(s, "database_capacity") || strings.Contains(s, "secret") {
		t.Fatal(s)
	}
}
