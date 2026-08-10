package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func TestTraceSampleRatio(t *testing.T) {
	t.Setenv("OTEL_TRACES_SAMPLER_ARG", "0.25")
	if got := traceSampleRatio(); got != 0.25 {
		t.Fatalf("traceSampleRatio() = %v, want 0.25", got)
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
