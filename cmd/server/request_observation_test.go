package main

import (
	"bytes"
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestUnsampledFailureHasEntryAndBodyEvidence(t *testing.T) {
	old := otel.GetTracerProvider()
	p := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	otel.SetTracerProvider(p)
	defer otel.SetTracerProvider(old)
	var out bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, &slog.HandlerOptions{Level: slog.LevelError})))
	defer slog.SetDefault(logger)
	h := observeHTTPEntry(newHTTPTraceHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.Copy(io.Discard, r.Body); w.WriteHeader(503) })), nil)
	req := httptest.NewRequest("POST", "/private?ticket=secret", strings.NewReader("secret"))
	req.Header.Set("X-HHC-Request-ID", "fixture-id")
	req.Header.Set("traceparent", "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-00")
	h.ServeHTTP(httptest.NewRecorder(), req)
	for _, want := range []string{"http_request_observation", "fixture-id", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "handler_entry_unix_ms", "body_read_ms", `"status":503`, `"parent_span_id":"bbbbbbbbbbbbbbbb"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "secret") || strings.Contains(out.String(), "/private") {
		t.Fatal("private data leaked")
	}
}
func TestCancelledRequestEvidence(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	defer slog.SetDefault(old)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/", nil).WithContext(ctx)
	observeHTTPEntry(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil).ServeHTTP(httptest.NewRecorder(), req)
	if !strings.Contains(out.String(), `"outcome":"cancelled"`) {
		t.Fatal(out.String())
	}
}
func TestExporterFailuresRemainCounted(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	defer slog.SetDefault(old)
	var w exportWarnings
	now := time.Now()
	w.record(now)
	w.record(now)
	w.record(now.Add(time.Minute))
	if strings.Count(out.String(), "trace export failed") != 2 || !strings.Contains(out.String(), `"failure_count_total":3`) {
		t.Fatal(out.String())
	}
}

func TestGeneratedServerTraceAndResponseIDJoinObservation(t *testing.T) {
	old := otel.GetTracerProvider()
	p := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(p)
	defer otel.SetTracerProvider(old)
	var out bytes.Buffer
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	defer slog.SetDefault(logger)
	var traceID string
	handler := observeHTTPEntry(newHTTPTraceHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID = trace.SpanContextFromContext(r.Context()).TraceID().String()
		w.Header().Set("X-HHC-Request-ID", "response-request-id")
		w.WriteHeader(503)
	})), nil)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if traceID == strings.Repeat("0", 32) || !strings.Contains(out.String(), traceID) || !strings.Contains(out.String(), `"request_id":"response-request-id"`) {
		t.Fatal(out.String())
	}
}

func TestCumulativeFastDatabasePhasesAppearOnce(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	defer slog.SetDefault(old)
	r, o := observeRequest(httptest.NewRequest("GET", "/api/member/recordings?limit=10&token=secret", nil))
	for range 6 {
		diagnostics.RecordDatabaseOutcome(r.Context(), slog.Default(), "db_query_lifecycle", 200*time.Millisecond, false)
	}
	o.started = time.Now().Add(-2 * time.Second)
	o.finish(200, true)
	o.finish(200, true)
	if strings.Count(out.String(), "http_request_observation") != 1 || !strings.Contains(out.String(), `"db_query_lifecycle":{"count":6,"total_ms":1200,"max_ms":200`) {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "secret") {
		t.Fatal("query leaked")
	}
}

func TestProductionTextSummaryHasNamedPhaseFields(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	defer slog.SetDefault(old)
	r, o := observeRequest(httptest.NewRequest("GET", "/api/member/recordings", nil))
	for range 6 {
		diagnostics.RecordDatabaseOutcome(r.Context(), slog.Default(), "db_query_lifecycle", 200*time.Millisecond, false)
	}
	o.started = time.Now().Add(-2 * time.Second)
	o.finish(200, true)
	for _, want := range []string{"phase_totals.db_query_lifecycle.count=6", "phase_totals.db_query_lifecycle.total_ms=1200", "phase_totals.db_query_lifecycle.max_ms=200"} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
	if out.Len() > 4096 || strings.Count(out.String(), "http_request_observation") != 1 {
		t.Fatal("unbounded or duplicate summary")
	}
	t.Logf("summary_records=1 bytes=%d", out.Len())
}
