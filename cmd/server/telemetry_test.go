package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTraceSampleRatio(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.25")
	if got := traceSampleRatio(); got != 0.25 {
		t.Fatalf("traceSampleRatio() = %v, want 0.25", got)
	}
}

func TestHTTPTraceExcludesClientMetadataWithoutChangingRequest(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	var gotRemoteAddr, gotUserAgent, gotForwardedFor string
	handler := newHTTPTraceHandler(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotRemoteAddr = request.RemoteAddr
		gotUserAgent = request.UserAgent()
		gotForwardedFor = request.Header.Get("X-Forwarded-For")
		writer.WriteHeader(http.StatusNoContent)
	}), otelhttp.WithTracerProvider(provider))

	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.RemoteAddr = "203.0.113.10:4567"
	request.Header.Set("User-Agent", "private-test-agent")
	request.Header.Set("X-Forwarded-For", "198.51.100.5")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if gotRemoteAddr != request.RemoteAddr || gotUserAgent != request.UserAgent() || gotForwardedFor != request.Header.Get("X-Forwarded-For") {
		t.Fatalf("handler request changed: remote=%q user-agent=%q x-forwarded-for=%q", gotRemoteAddr, gotUserAgent, gotForwardedFor)
	}
	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	for _, attribute := range ended[0].Attributes() {
		switch string(attribute.Key) {
		case "client.address", "network.peer.address", "network.peer.port", "user_agent.original":
			t.Errorf("trace contains private attribute %q", attribute.Key)
		}
	}
}

func TestTraceSampleRatioFallsBackForInvalidValues(t *testing.T) {
	for _, value := range []string{"", "invalid", "-0.1", "1.1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("OTEL_TRACES_SAMPLER_ARG", value)
			if got := traceSampleRatio(); got != 0.1 {
				t.Fatalf("traceSampleRatio() = %v, want 0.1", got)
			}
		})
	}
}

func TestUnavailableCollectorDoesNotBlockRequests(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "50")
	shutdown := configureTelemetry(context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = shutdown(ctx)
	}()

	handler := otelhttp.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), "test")
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	response := httptest.NewRecorder()
	started := time.Now()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("request blocked for %s while collector was unavailable", elapsed)
	}
}
