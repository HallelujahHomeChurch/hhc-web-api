package assetclient

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
)

// Only finite operation names and trace phases leave the private request path.
func recordingRequestDiagnostics(ctx context.Context, method, path string) (context.Context, func(error, error, int)) {
	if !strings.HasPrefix(path, "/priv/recording-") && !strings.HasPrefix(path, "/priv/recordings/") {
		return ctx, func(error, error, int) {}
	}
	path, _, _ = strings.Cut(path, "?")
	operation := "recording_request"
	family := ""
	if strings.HasPrefix(path, "/priv/recording-captures") {
		family = "capture"
	} else if strings.HasPrefix(path, "/priv/recording-packages") {
		family = "package"
	}
	if family != "" {
		operation = family + "_write"
		if method == http.MethodGet {
			operation = family + "_read"
		}
		parts := strings.Split(path, "/")
		if len(parts) == 5 {
			switch parts[4] {
			case "grant", "progress", "sign", "confirm", "seal", "abort", "heartbeat", "complete":
				operation = family + "_" + parts[4]
			}
		}
	}
	var mu sync.Mutex
	phase := "dispatch"
	setPhase := func(value string) { mu.Lock(); phase = value; mu.Unlock() }
	trace := &httptrace.ClientTrace{
		GetConn:           func(string) { diagnostics.RecordRequestPhase(ctx, "asset_attempt", 0, false) },
		DNSStart:          func(httptrace.DNSStartInfo) { setPhase("dns") },
		ConnectStart:      func(string, string) { setPhase("connect") },
		TLSHandshakeStart: func() { setPhase("tls") },
		GotConn:           func(httptrace.GotConnInfo) { setPhase("request_write") },
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err == nil {
				setPhase("response_headers")
			}
		},
		GotFirstResponseByte: func() { setPhase("response_body") },
	}
	started := time.Now()
	return httptrace.WithClientTrace(ctx, trace), func(result, transport error, status int) {
		if result == nil {
			return
		}
		failure := "http"
		var network net.Error
		switch {
		case errors.Is(ctx.Err(), context.Canceled) || errors.Is(result, context.Canceled):
			failure = "cancelled"
		case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(result, context.DeadlineExceeded) || errors.As(transport, &network) && network.Timeout() || errors.As(result, &network) && network.Timeout():
			failure = "timeout"
		case transport != nil:
			failure = "transport"
		case status < 400:
			failure = "response_decode"
		}
		mu.Lock()
		lastPhase := phase
		mu.Unlock()
		provenance, _ := auditclient.ProvenanceFromContext(ctx)
		slog.WarnContext(ctx, "recording_dependency_failed", "operation", operation, "phase", lastPhase, "status", status, "failure", failure, "elapsed_ms", time.Since(started).Milliseconds(), "request_id", provenance.RequestID)
	}
}
