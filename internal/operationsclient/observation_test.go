package operationsclient

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
	"go.opentelemetry.io/otel/trace"
)

func TestFailurePhasesAndPrivateCorrelation(t *testing.T) {
	for _, phase := range []string{"response_headers", "response_body", "response_status", "decode"} {
		t.Run(phase, func(t *testing.T) {
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer slog.SetDefault(old)
			sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{2}, TraceFlags: trace.FlagsSampled})
			ctx := trace.ContextWithSpanContext(t.Context(), sc)
			ctx = auditclient.WithProvenance(ctx, subjectID, "request-test")
			stop := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-HHC-Request-ID") != "request-test" || !strings.Contains(r.Header.Get("traceparent"), sc.TraceID().String()) {
					t.Error("missing correlation")
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("baggage") != "" {
					t.Error("unexpected sensitive header")
				}
				switch phase {
				case "response_headers":
					<-stop
				case "response_body":
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					<-stop
				case "response_status":
					w.WriteHeader(503)
					_, _ = w.Write([]byte("private-upstream-detail"))
				case "decode":
					_, _ = w.Write([]byte("private-upstream-detail"))
				}
			}))
			defer func() { close(stop); server.Close() }()
			client := New(server.URL)
			client.http.Timeout = 50 * time.Millisecond
			_, err := client.CheckEntitlements(ctx, subjectID, []string{"bulletin.general.en.access"})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("expected unavailable: %v", err)
			}
			if phase == "response_headers" || phase == "response_body" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline lost: %v", err)
				}
			}
			output := logs.String()
			for _, want := range []string{`"phase":"` + phase + `"`, `"request_id":"request-test"`, sc.TraceID().String()} {
				if !strings.Contains(output, want) {
					t.Fatalf("missing %s: %s", want, output)
				}
			}
			for _, secret := range []string{subjectID, "private-upstream-detail", server.URL, "bulletin.general.en.access"} {
				if strings.Contains(output, secret) {
					t.Fatalf("private data in log: %s", secret)
				}
			}
		})
	}
}

func TestRefusedDaprConnectionIsNotResponseTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(old)
	_, err := New(endpoint).CheckEntitlements(t.Context(), subjectID, []string{"bulletin.general.en.access"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v", err)
	}
	for _, want := range []string{`"outcome":"connection_refused"`, `"phase":"connect"`, `"first_byte_ms":-1`} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("missing %s: %s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), endpoint) {
		t.Fatal("endpoint leaked")
	}
}

func TestTLSHandshakeDelayIsDistinctFromHeaderWait(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			defer c.Close()
			<-stop
		}
	}()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	client := New("https://" + listener.Addr().String())
	client.http.Timeout = 50 * time.Millisecond
	_, err = client.CheckEntitlements(t.Context(), subjectID, []string{"bulletin.general.en.access"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal("expected deadline")
	}
	if !strings.Contains(logs.String(), `"phase":"tls"`) || !strings.Contains(logs.String(), `"first_byte_ms":-1`) {
		t.Fatal(logs.String())
	}
}
