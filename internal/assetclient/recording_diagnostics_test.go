package assetclient

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

func TestRecordingDependencyDiagnosticsAreBoundedAndRedacted(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte("secret-body-credential"))
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "https://example.test")
	ctx := auditclient.WithProvenance(context.Background(), "secret-subject", "request-test")
	err := client.request(ctx, http.MethodGet, "/priv/recording-captures/secret-id?token=secret-token", nil, "", nil)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	out := logs.String()
	for _, want := range []string{"recording_dependency_failed", "capture_read", `"status":503`, "request-test"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s: %s", want, out)
		}
	}
	for _, secret := range []string{"secret", server.URL} {
		if strings.Contains(out, secret) {
			t.Fatalf("private data logged: %s", out)
		}
	}
	logs.Reset()
	client.http.Transport = privateRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "secret-network", IsTimeout: true}
	})
	_ = client.request(ctx, http.MethodGet, "/priv/recording-captures/secret-id", nil, "", nil)
	if !strings.Contains(logs.String(), `"failure":"timeout"`) || strings.Contains(logs.String(), "secret") {
		t.Fatal(logs.String())
	}
	logs.Reset()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_ = client.request(cancelled, http.MethodGet, "/priv/recording-captures/secret-id", nil, "", nil)
	if !strings.Contains(logs.String(), `"failure":"cancelled"`) {
		t.Fatal(logs.String())
	}
}

func TestRecordingDependencyBodyTimeout(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := New(server.URL, "hhc-web-api", "https://example.test")
	client.http.Timeout = 100 * time.Millisecond
	var body map[string]any
	if err := client.request(context.Background(), http.MethodGet, "/priv/recording-captures/private-id", nil, "", &body); err == nil {
		t.Fatal("expected body timeout")
	}
	for _, want := range []string{`"failure":"timeout"`, `"phase":"response_body"`, `"status":200`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing %s: %s", want, logs.String())
		}
	}
}
