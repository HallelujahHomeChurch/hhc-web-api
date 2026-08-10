package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

const defaultTraceSampleRatio = 0.1

type requestTraceMetadata struct {
	remoteAddr string
	header     http.Header
}

type requestTraceMetadataKey struct{}

func newHTTPTraceHandler(next http.Handler, options ...otelhttp.Option) http.Handler {
	// Keep client metadata available to the API without exporting it as trace attributes.
	restoreRequest := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		metadata, _ := request.Context().Value(requestTraceMetadataKey{}).(requestTraceMetadata)
		restored := request.Clone(request.Context())
		restored.RemoteAddr = metadata.remoteAddr
		restored.Header = metadata.header
		next.ServeHTTP(writer, restored)
	})
	traced := otelhttp.NewHandler(restoreRequest, "hhc-web-api", options...)

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		metadata := requestTraceMetadata{remoteAddr: request.RemoteAddr, header: request.Header}
		context := context.WithValue(request.Context(), requestTraceMetadataKey{}, metadata)
		sanitized := request.Clone(context)
		sanitized.RemoteAddr = ""
		sanitized.Header = request.Header.Clone()
		sanitized.Header.Del("User-Agent")
		sanitized.Header.Del("X-Forwarded-For")
		traced.ServeHTTP(writer, sanitized)
	})
}

func configureTelemetry(ctx context.Context) func(context.Context) error {
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) == "" {
		return func(context.Context) error { return nil }
	}

	initCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	exporter, err := otlptracegrpc.New(initCtx)
	if err != nil {
		slog.Warn("OpenTelemetry exporter disabled", "error", err)
		return func(context.Context) error { return nil }
	}

	serviceResource, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", "hhc-web-api"),
		attribute.String("service.version", envOrDefault("RELEASE", "unknown")),
		attribute.String("deployment.environment.name", envOrDefault("ENVIRONMENT", "production")),
	))
	if err != nil {
		slog.Warn("OpenTelemetry resource disabled", "error", err)
		return func(context.Context) error { return nil }
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(serviceResource),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(traceSampleRatio()))),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return provider.Shutdown
}

func traceSampleRatio() float64 {
	ratio, err := strconv.ParseFloat(os.Getenv("OTEL_TRACES_SAMPLER_ARG"), 64)
	if err != nil || ratio < 0 || ratio > 1 {
		return defaultTraceSampleRatio
	}
	return ratio
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
