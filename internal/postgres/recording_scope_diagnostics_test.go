package postgres

import (
	"bytes"
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"log/slog"
	"strings"
	"testing"
)

func TestCaptureScopeDiagnosticsKeepPublicNotFoundAndPrivateIdentity(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	ctx := auditclient.WithProvenance(context.Background(), "secret-member", "scope-request")
	err := captureScopeNotFound(ctx, "capture_unavailable", "admission_closed")
	if !errors.Is(err, recordings.ErrNotFound) || err.Error() != recordings.ErrNotFound.Error() {
		t.Fatal("public not-found contract changed")
	}
	text := logs.String()
	for _, want := range []string{"recording_capture_scope_denied", "capture_unavailable", "admission_closed", "scope-request"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s: %s", want, text)
		}
	}
	if strings.Contains(text, "secret") {
		t.Fatal("private subject logged")
	}
}
