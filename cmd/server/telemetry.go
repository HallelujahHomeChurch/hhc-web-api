package main

import (
	"context"
	"database/sql"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/dbphase"
	"github.com/felixge/httpsnoop"
	"net/http"
	"net/url"
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
	url        *url.URL
	requestURI string
	host       string
}

type requestTraceMetadataKey struct{}

func newHTTPTraceHandler(next http.Handler, options ...otelhttp.Option) http.Handler {
	// Keep client metadata available to the API without exporting it as trace attributes.
	restoreRequest := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		metadata, _ := request.Context().Value(requestTraceMetadataKey{}).(requestTraceMetadata)
		restored := request.Clone(request.Context())
		restored.RemoteAddr = metadata.remoteAddr
		restored.Header = metadata.header
		restored.URL = metadata.url
		restored.RequestURI = metadata.requestURI
		restored.Host = metadata.host
		if observation, ok := request.Context().Value(observationKey{}).(*requestObservation); ok {
			observation.ctx = request.Context()
		}
		next.ServeHTTP(writer, restored)
	})
	traced := otelhttp.NewHandler(restoreRequest, "hhc-web-api", options...)

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		metadata := requestTraceMetadata{remoteAddr: request.RemoteAddr, header: request.Header, url: request.URL, requestURI: request.RequestURI, host: request.Host}
		context := context.WithValue(request.Context(), requestTraceMetadataKey{}, metadata)
		sanitized := request.Clone(context)
		sanitized.RemoteAddr = ""
		sanitized.URL = &url.URL{Path: "/"}
		sanitized.RequestURI = "/"
		sanitized.Host = ""
		sanitized.Header = request.Header.Clone()
		sanitized.Header.Del("User-Agent")
		sanitized.Header.Del("X-Forwarded-For")
		traced.ServeHTTP(writer, sanitized)
	})
}

func configureTelemetry(ctx context.Context) func(context.Context) error {
	return configureTelemetryWithExporter(ctx, func(ctx context.Context) (sdktrace.SpanExporter, error) { return otlptracegrpc.New(ctx) })
}

func configureTelemetryWithExporter(ctx context.Context, newExporter func(context.Context) (sdktrace.SpanExporter, error)) func(context.Context) error {
	// Propagation remains available when trace export is disabled or unavailable.
	otel.SetTextMapPropagator(propagation.TraceContext{})
	var warnings exportWarnings
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(error) { warnings.record(time.Now()) }))
	if strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) == "" {
		exporterState("disabled_missing_endpoint")
		return func(context.Context) error { return nil }
	}

	initCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	exporter, err := newExporter(initCtx)
	if err != nil {
		exporterState("initialization_failed")
		return func(context.Context) error { return nil }
	}

	serviceResource := resource.NewSchemaless(
		attribute.String("service.name", "hhc-web-api"),
		attribute.String("service.version", envOrDefault("RELEASE", "unknown")),
		attribute.String("deployment.environment.name", envOrDefault("ENVIRONMENT", "production")),
	)

	progress := new(exportProgress)
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(progressExporter{SpanExporter: exporter, progress: progress}),
		sdktrace.WithResource(serviceResource),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(traceSampleRatio()))),
	)
	otel.SetTracerProvider(provider)
	exporterState("initialized")
	return func(ctx context.Context) error {
		err := provider.Shutdown(ctx)
		progress.summary(ctx, err == nil)
		return err
	}
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

// Observe before route authentication and tracing; no incoming URLs or headers are logged.
func observeHTTPEntry(next http.Handler, db *sql.DB) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := (propagation.TraceContext{}).Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, phases := dbphase.Observe(ctx)
		r, observation := observeRequest(r.WithContext(ctx))
		observation.dbPhases = phases
		if db != nil {
			observation.db = db
			observation.poolBefore = db.Stats()
		}
		completed, status := false, 0
		defer func() {
			if id := w.Header().Get("X-HHC-Request-ID"); safeRequestID.MatchString(id) {
				observation.ctx = context.WithValue(observation.ctx, requestKey{}, id)
			}
			observation.finish(status, completed)
		}()
		metrics := httpsnoop.CaptureMetrics(next, w, r)
		status, completed = metrics.Code, true
	})
}

// This boundary observes exporter acknowledgments, not backend storage or SDK queue loss.
type progressExporter struct {
	sdktrace.SpanExporter
	progress *exportProgress
}

func (e progressExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	err := e.SpanExporter.ExportSpans(ctx, spans)
	e.progress.record(time.Now(), len(spans), err != nil)
	return err
}
