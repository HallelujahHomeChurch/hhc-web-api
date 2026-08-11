package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTranslationRouteExtendsOnlyItsWriteDeadlineThroughTraceChain(t *testing.T) {
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	previewer := serverTranslationPreviewer{}
	handler := httpapi.NewWithTranslation(nil, nil, nil, nil, "api-gateway", "", true, previewer, 50*time.Second, func() time.Time { return now })
	traced := newHTTPTraceHandler(handler.Routes())

	translationRequest := httptest.NewRequest(http.MethodPost, "/api/admin/content/videos/10000000-0000-4000-8000-000000000001/translation-previews/ja", strings.NewReader(`{"sourceLocale":"zh-Hant","replaceExisting":false}`))
	translationRequest.Header.Set("Dapr-Caller-App-Id", "api-gateway")
	translationRequest.Header.Set("X-HHC-User-ID", "user-1")
	translationRequest.Header.Set("X-HHC-Auth-Provider", "account-api")
	translationRequest.Header.Set("X-HHC-Scopes", "cms:write")
	translationRequest.Header.Set("If-Match", `"7"`)
	translationWriter := newServerDeadlineWriter(now.Add(30 * time.Second))
	traced.ServeHTTP(translationWriter, translationRequest)
	if translationWriter.Code != http.StatusOK || translationWriter.deadlineCalls != 1 || !translationWriter.deadline.Equal(now.Add(50*time.Second)) {
		t.Fatalf("translation status=%d deadline=%s calls=%d body=%s", translationWriter.Code, translationWriter.deadline, translationWriter.deadlineCalls, translationWriter.Body.String())
	}

	ordinaryWriter := newServerDeadlineWriter(now.Add(30 * time.Second))
	traced.ServeHTTP(ordinaryWriter, httptest.NewRequest(http.MethodGet, "/health", nil))
	if ordinaryWriter.Code != http.StatusOK || ordinaryWriter.deadlineCalls != 0 || !ordinaryWriter.deadline.Equal(now.Add(30*time.Second)) {
		t.Fatalf("ordinary status=%d deadline=%s calls=%d", ordinaryWriter.Code, ordinaryWriter.deadline, ordinaryWriter.deadlineCalls)
	}
}

type serverTranslationPreviewer struct{}

func (serverTranslationPreviewer) Preview(context.Context, translation.PreviewRequest) (translation.Preview, error) {
	return translation.Preview{SourceLocale: "zh-Hant", TargetLocale: "ja", SourceVersion: 7, Translation: map[string]string{"title": "translated"}}, nil
}

type serverDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadline      time.Time
	deadlineCalls int
}

func newServerDeadlineWriter(initial time.Time) *serverDeadlineWriter {
	return &serverDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), deadline: initial}
}

func (w *serverDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlineCalls++
	w.deadline = deadline
	return nil
}

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
