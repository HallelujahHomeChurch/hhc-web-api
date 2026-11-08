package assetclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func privateTraceFixture(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	spans := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	old, oldPropagation := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(old)
		otel.SetTextMapPropagator(oldPropagation)
		provider.Shutdown(context.Background())
	})
	return spans
}
func assertPrivateAssetSpans(t *testing.T, spans *tracetest.SpanRecorder) {
	t.Helper()
	if len(spans.Ended()) == 0 {
		t.Fatal("no spans exported")
	}
	for _, span := range spans.Ended() {
		values := []string{span.Name(), span.Status().Description}
		for _, a := range span.Attributes() {
			values = append(values, a.Value.Emit())
		}
		for _, event := range span.Events() {
			values = append(values, event.Name)
			for _, a := range event.Attributes {
				values = append(values, a.Value.Emit())
			}
		}
		for _, value := range values {
			for _, sensitive := range []string{"synthetic-secret", "capture-private", "sig=", "user-private", "pass-private"} {
				if strings.Contains(value, sensitive) {
					t.Fatal("sensitive fixture leaked in dependency trace")
				}
			}
		}
	}
}

func TestAssetTracePrivacyPreservesUploadAndRedirect(t *testing.T) {
	spans := privateTraceFixture(t)
	seen := 0
	var rootTrace string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen++
		body, _ := io.ReadAll(r.Body)
		user, password, ok := r.BasicAuth()
		if r.Method != http.MethodPut || string(body) != "body" || r.Header.Get("X-Upload-Test") != "synthetic-secret" || !ok || user != "user-private" || password != "pass-private" {
			t.Error("upload HTTP semantics changed")
		}
		if !strings.Contains(r.Header.Get("Traceparent"), rootTrace) {
			t.Error("trace propagation lost")
		}
		if seen == 1 {
			if r.URL.RequestURI() != "/capture-private?sig=synthetic-secret" {
				t.Error("initial URL changed")
			}
			w.Header().Set("Location", "/capture-private/redirect?sig=synthetic-secret-2")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		if r.URL.RequestURI() != "/capture-private/redirect?sig=synthetic-secret-2" {
			t.Error("redirect URL changed")
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	ctx, parent := otel.Tracer("test").Start(context.Background(), "test-parent")
	rootTrace = parent.SpanContext().TraceID().String()
	target := strings.Replace(server.URL, "http://", "http://user-private:pass-private@", 1) + "/capture-private?sig=synthetic-secret"
	client := New(server.URL, "test", "test")
	err := client.Upload(ctx, UploadTarget{URL: target, Method: http.MethodPut, Headers: map[string]string{"X-Upload-Test": "synthetic-secret"}}, strings.NewReader("body"), 4)
	parent.End()
	if err != nil || seen != 2 {
		t.Fatal("redirected upload did not succeed")
	}
	if len(spans.Ended()) != 3 {
		t.Fatal("redirect/dependency span count changed")
	}
	for _, span := range spans.Ended() {
		if span.Name() != "test-parent" && span.Parent().SpanID() != parent.SpanContext().SpanID() {
			t.Fatal("dependency parent changed")
		}
	}
	statuses := map[int64]bool{}
	for _, span := range spans.Ended() {
		if span.Name() == "asset.http" {
			if span.SpanKind() != trace.SpanKindClient {
				t.Fatal("client span kind lost")
			}
			method := ""
			for _, a := range span.Attributes() {
				if a.Key == "http.request.method" {
					method = a.Value.AsString()
				}
				if a.Key == "http.response.status_code" {
					statuses[a.Value.AsInt64()] = true
				}
			}
			if method != "PUT" {
				t.Fatal("operational method lost")
			}
		}
	}
	if !statuses[307] || !statuses[201] {
		t.Fatal("operational status lost")
	}
	assertPrivateAssetSpans(t, spans)
}

type privateRoundTripper func(*http.Request) (*http.Response, error)

func (f privateRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type privateErrorBody struct {
	err    error
	closed bool
}

func (b *privateErrorBody) Read([]byte) (int, error) { return 0, b.err }
func (b *privateErrorBody) Close() error             { b.closed = true; return nil }

func TestAssetTracePrivacyPreservesErrors(t *testing.T) {
	for _, bodyFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "transport", true: "body"}[bodyFailure], func(t *testing.T) {
			spans := privateTraceFixture(t)
			original := errors.New("synthetic-secret URL?sig=synthetic-secret capture-private")
			body := &privateErrorBody{err: original}
			old := http.DefaultTransport
			http.DefaultTransport = privateRoundTripper(func(r *http.Request) (*http.Response, error) {
				if r.URL.RawQuery != "sig=synthetic-secret" || r.URL.Path != "/capture-private" {
					t.Error("request URL changed")
				}
				if !bodyFailure {
					return nil, original
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body, Request: r}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = old })
			client := New("http://example.invalid", "test", "test")
			req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/capture-private?sig=synthetic-secret", nil)
			response, err := client.http.Do(req)
			if bodyFailure {
				if err != nil {
					t.Fatal("response failed before body")
				}
				_, err = io.ReadAll(response.Body)
				response.Body.Close()
				if !body.closed {
					t.Fatal("body close not forwarded")
				}
			}
			if !errors.Is(err, original) {
				t.Fatal("caller error identity changed")
			}
			if spans.Ended()[0].Status().Code != codes.Error {
				t.Fatal("error status lost")
			}
			assertPrivateAssetSpans(t, spans)
		})
	}
}

func TestAssetTracePrivacyPreservesCancellation(t *testing.T) {
	spans := privateTraceFixture(t)
	old := http.DefaultTransport
	http.DefaultTransport = privateRoundTripper(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
	defer func() { http.DefaultTransport = old }()
	client := New("http://example.invalid", "test", "test")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.invalid/capture-private?sig=synthetic-secret", nil)
	_, err := client.http.Do(req)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation identity changed")
	}
	assertPrivateAssetSpans(t, spans)
}

func TestAssetTracePrivacyFiltersInitialAndErrorMetadata(t *testing.T) {
	spans := privateTraceFixture(t)
	provider := privateTransportProvider{otel.GetTracerProvider()}
	_, span := provider.Tracer("test").Start(context.Background(), "capture-private", trace.WithAttributes(attribute.String("url.full", "?sig=synthetic-secret")))
	span.AddEvent("capture-private", trace.WithAttributes(attribute.String("token", "synthetic-secret")))
	span.RecordError(errors.New("synthetic-secret"), trace.WithAttributes(attribute.String("token", "synthetic-secret")))
	span.SetName("capture-private")
	span.End()
	assertPrivateAssetSpans(t, spans)
}
