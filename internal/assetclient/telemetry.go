package assetclient

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// privateTransportProvider sanitizes only telemetry, never HTTP requests, bodies,
// redirects or returned errors. otelhttp's URL attributes and error descriptions
// can contain signed queries; its public filters cannot selectively redact them.
type privateTransportProvider struct{ trace.TracerProvider }

func (p privateTransportProvider) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return privateTransportTracer{p.TracerProvider.Tracer(name, opts...)}
}

type privateTransportTracer struct{ trace.Tracer }

func (t privateTransportTracer) Start(ctx context.Context, _ string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	safe := []trace.SpanStartOption{trace.WithSpanKind(cfg.SpanKind()), trace.WithTimestamp(cfg.Timestamp()), trace.WithAttributes(privateTransportAttributes(cfg.Attributes())...)}
	if cfg.NewRoot() {
		safe = append(safe, trace.WithNewRoot())
	}
	for _, link := range cfg.Links() {
		link.Attributes = nil
		safe = append(safe, trace.WithLinks(link))
	}
	ctx, span := t.Tracer.Start(ctx, "asset.http", safe...)
	wrapped := privateTransportSpan{span}
	return trace.ContextWithSpan(ctx, wrapped), wrapped
}

type privateTransportSpan struct{ trace.Span }

func (s privateTransportSpan) SetAttributes(attrs ...attribute.KeyValue) {
	s.Span.SetAttributes(privateTransportAttributes(attrs)...)
}
func (s privateTransportSpan) SetStatus(code codes.Code, _ string) { s.Span.SetStatus(code, "") }
func (s privateTransportSpan) AddLink(link trace.Link)             { link.Attributes = nil; s.Span.AddLink(link) }
func (s privateTransportSpan) SetName(string)                      { s.Span.SetName("asset.http") }
func (s privateTransportSpan) RecordError(err error, _ ...trace.EventOption) {
	if err != nil {
		s.Span.RecordError(errors.New("transport failure"))
	}
}
func (s privateTransportSpan) AddEvent(_ string, opts ...trace.EventOption) {
	cfg := trace.NewEventConfig(opts...)
	s.Span.AddEvent("asset.http.event", trace.WithTimestamp(cfg.Timestamp()), trace.WithAttributes(privateTransportAttributes(cfg.Attributes())...))
}

// Keep a finite operational vocabulary. URLs, destinations, identifiers, headers
// and free-form error text are intentionally absent, including on body failures.
func privateTransportAttributes(attrs []attribute.KeyValue) []attribute.KeyValue {
	safe := make([]attribute.KeyValue, 0, len(attrs))
	for _, a := range attrs {
		switch a.Key {
		case "http.request.method":
			method := a.Value.AsString()
			switch method {
			case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS", "CONNECT", "TRACE":
			default:
				method = "_OTHER"
			}
			safe = append(safe, attribute.String(string(a.Key), method))
		case "http.response.status_code":
			if code := a.Value.AsInt64(); code >= 100 && code <= 599 {
				safe = append(safe, attribute.Int64(string(a.Key), code))
			}
		case "network.protocol.version":
			if version := a.Value.AsString(); version == "1.0" || version == "1.1" || version == "2" || version == "3" {
				safe = append(safe, attribute.String(string(a.Key), version))
			}
		case "network.protocol.name":
			if a.Value.AsString() == "http" {
				safe = append(safe, attribute.String(string(a.Key), "http"))
			}
		case "error.type":
			safe = append(safe, attribute.String("error.type", "request_error"))
		}
	}
	return safe
}
