package operationsclient

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptrace"
	"sync"
	"syscall"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Phase clocks describe the client transport; header wait also includes remote work.
type observation struct {
	mu                                     sync.Mutex
	started                                time.Time
	phase                                  string
	connected, written, firstByte          int64
	dnsStarted, connectStarted, tlsStarted time.Time
	dnsMS, connectMS, tlsMS                int64
	reused                                 bool
	status                                 int
	requestID                              string
	span                                   trace.Span
}

func observe(ctx context.Context) (context.Context, *observation) {
	ctx, span := otel.Tracer("hhc-web-api/operationsclient").Start(ctx, "operations.entitlement_check", trace.WithSpanKind(trace.SpanKindClient))
	o := &observation{started: time.Now(), phase: "connection", connected: -1, written: -1, firstByte: -1, span: span, dnsMS: -1, connectMS: -1, tlsMS: -1}
	if p, ok := auditclient.ProvenanceFromContext(ctx); ok && auditclient.ValidActorRequest(p.ActorID, p.RequestID) {
		o.requestID = p.RequestID
	}
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.phase = "dns"
			o.dnsStarted = time.Now()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.dnsMS = time.Since(o.dnsStarted).Milliseconds()
		},
		ConnectStart: func(string, string) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.phase = "connect"
			o.connectStarted = time.Now()
		},
		ConnectDone: func(string, string, error) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.connectMS = time.Since(o.connectStarted).Milliseconds()
		},
		TLSHandshakeStart: func() { o.mu.Lock(); defer o.mu.Unlock(); o.phase = "tls"; o.tlsStarted = time.Now() },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.tlsMS = time.Since(o.tlsStarted).Milliseconds()
		},
		GotConn: func(i httptrace.GotConnInfo) {
			o.mu.Lock()
			defer o.mu.Unlock()
			o.connected = time.Since(o.started).Milliseconds()
			o.reused = i.Reused
			o.phase = "request_write"
		},
		WroteRequest: func(i httptrace.WroteRequestInfo) {
			o.mu.Lock()
			defer o.mu.Unlock()
			if i.Err == nil {
				o.written = time.Since(o.started).Milliseconds()
				o.phase = "response_headers"
			}
		},
		GotFirstResponseByte: func() { o.mu.Lock(); defer o.mu.Unlock(); o.firstByte = time.Since(o.started).Milliseconds() },
	})
	return ctx, o
}
func (o *observation) setPhase(p string) { o.mu.Lock(); defer o.mu.Unlock(); o.phase = p }
func (o *observation) headers(ctx context.Context, r *http.Request) {
	if o.requestID != "" {
		r.Header.Set("X-HHC-Request-ID", o.requestID)
	}
	// Propagate trace context only, never baggage or identity from the caller.
	propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(r.Header))
}
func (o *observation) finish(ctx context.Context, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	defer o.span.End()
	outcome := "ok"
	if err != nil {
		outcome = "unavailable"
		if errors.Is(err, syscall.ECONNREFUSED) {
			outcome = "connection_refused"
		}
		if errors.Is(err, syscall.ECONNRESET) {
			outcome = "connection_reset"
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			outcome = "unexpected_eof"
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded {
			outcome = "deadline"
		}
		if errors.Is(err, context.Canceled) || ctx.Err() == context.Canceled {
			outcome = "cancelled"
		}
		o.span.SetStatus(codes.Error, outcome)
	}
	elapsed := time.Since(o.started).Milliseconds()
	o.span.SetAttributes(attribute.String("failure.phase", o.phase), attribute.String("outcome", outcome), attribute.Int("http.response.status_code", o.status), attribute.Int64("connection_ms", o.connected), attribute.Int64("request_written_ms", o.written), attribute.Int64("first_byte_ms", o.firstByte), attribute.Bool("connection_reused", o.reused), attribute.Int64("dns_ms", o.dnsMS), attribute.Int64("connect_ms", o.connectMS), attribute.Int64("tls_ms", o.tlsMS))
	if err != nil || elapsed >= 1000 {
		record := slog.NewRecord(time.Now(), slog.LevelWarn, "operations_entitlement_call", 0)
		record.Add("request_id", o.requestID, "trace_id", o.span.SpanContext().TraceID().String(), "phase", o.phase, "outcome", outcome, "status", o.status, "elapsed_ms", elapsed, "connection_ms", o.connected, "request_written_ms", o.written, "first_byte_ms", o.firstByte, "connection_reused", o.reused, "dns_ms", o.dnsMS, "connect_ms", o.connectMS, "tls_ms", o.tlsMS)
		_ = slog.Default().Handler().Handle(ctx, record)
	}
}
