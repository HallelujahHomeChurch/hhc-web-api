package diagnostics

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestConcurrentAggregationAndReset(t *testing.T) {
	r := &Recorder{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 1000 {
				r.observe(ProjectionPoll, 25*time.Millisecond, true, false)
			}
		})
	}
	wg.Wait()
	raw, saturated := r.summary()
	var got map[string]counters
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if saturated || got["projection_poll"] != (counters{20000, 500000, 25, 20000, 0, 0, 0, 20000, 0, 0}) {
		t.Fatalf("unexpected counters %s", raw)
	}
	raw, _ = r.summary()
	json.Unmarshal([]byte(raw), &got)
	if got["projection_poll"][0] != 0 {
		t.Fatal("window did not reset")
	}
}

func TestTraceCorrelationAndPrivacy(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(old)
	defer provider.Shutdown(context.Background())
	root, parent := provider.Tracer("test").Start(context.Background(), "request")
	r := &Recorder{}
	ctx, finish := Start(WithRecorder(root, r), ProjectionPoll)
	_, child := otel.Tracer("test").Start(ctx, "asset-call")
	child.End()
	finish(errors.New("token=SECRET https://private/signed?user=capture-id SELECT private"))
	Discard(WithRecorder(root, r), ProjectionPoll)
	parent.End()
	spans := recorder.Ended()
	if len(spans) != 3 || spans[1].Parent().SpanID() != parent.SpanContext().SpanID() || spans[0].Parent().SpanID() != spans[1].SpanContext().SpanID() {
		t.Fatal("stage lost parent or dependency correlation")
	}
	var text strings.Builder
	for _, span := range spans {
		text.WriteString(span.Name())
		for _, a := range span.Attributes() {
			text.WriteString(a.Value.AsString())
		}
	}
	raw, _ := r.summary()
	text.WriteString(raw)
	for _, secret := range []string{"SECRET", "private", "capture-id", "SELECT"} {
		if strings.Contains(text.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if len(spans[1].Events()) != 0 {
		t.Fatal("error details exported")
	}
}

func TestCancellationAndDisabledContext(t *testing.T) {
	r := &Recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, finish := Start(WithRecorder(ctx, r), BroadcastLock)
	finish(context.DeadlineExceeded)
	if r.values[BroadcastLock][4] != 1 {
		t.Fatal("cancel not counted")
	}
	plain := context.Background()
	returned, noop := Start(plain, ProjectionPoll)
	noop(errors.New("secret"))
	Discard(plain, ProjectionPoll)
	if returned != plain {
		t.Fatal("disabled context changed")
	}
	done := make(chan struct{})
	go func() {
		r.run(ctx, func() sql.DBStats { return sql.DBStats{} }, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), "unknown", make(chan time.Time), time.Now)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop worker")
	}
	_, stop := Launch(context.Background(), func() sql.DBStats { return sql.DBStats{} }, slog.Default(), "unknown")
	stop()
}

func TestMaximumSerializedSizeAndSaturation(t *testing.T) {
	r := &Recorder{}
	for stage := Stage(0); stage < stageCount; stage++ {
		for field := range r.values[stage] {
			r.values[stage][field] = math.MaxUint32
		}
	}
	r.observe(ProjectionPoll, time.Duration(math.MaxInt64), true, true)
	raw, saturated := r.summary()
	if !saturated {
		t.Fatal("overflow not reported")
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	logger.Info("cms_stages", "release", strings.Repeat("a", 40), "interval_ms", int64(math.MaxInt64), "saturated", saturated, "data", raw)
	t.Logf("worst-case stage text record: %d bytes", buf.Len())
	if buf.Len() > 1024 {
		t.Fatalf("stage summary %d bytes >1024", buf.Len())
	}
	buf.Reset()
	logger.Info("cms_db_pool", "release", strings.Repeat("a", 40), "interval_ms", int64(math.MaxInt64), "max", math.MaxInt64, "open", math.MaxInt64, "in_use", math.MaxInt64, "idle", math.MaxInt64, "waits", int64(math.MaxInt64), "wait_ms", int64(math.MaxInt64), "reset", true, "initial", true)
	t.Logf("worst-case DB text record: %d bytes", buf.Len())
	if buf.Len() > 512 {
		t.Fatalf("DB summary %d bytes >512", buf.Len())
	}
}

// The worker is synchronous only with its sink; request accounting remains independent.
type blockingHandler struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *blockingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *blockingHandler) Handle(context.Context, slog.Record) error {
	h.once.Do(func() { close(h.entered) })
	<-h.release
	return nil
}
func (h *blockingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *blockingHandler) WithGroup(string) slog.Handler      { return h }
func TestBlockedSinkDoesNotBlockAccounting(t *testing.T) {
	r := &Recorder{}
	h := &blockingHandler{entered: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.run(ctx, func() sql.DBStats { return sql.DBStats{} }, slog.New(h), "secret-release-value", ticks, time.Now)
	}()
	ticks <- time.Now().Add(time.Minute)
	<-h.entered
	recorded := make(chan struct{})
	go func() {
		for range 10000 {
			r.observe(ProjectionPoll, time.Second, true, false)
		}
		close(recorded)
	}()
	select {
	case <-recorded:
	case <-time.After(time.Second):
		t.Error("request accounting blocked on log sink")
	}
	cancel()
	close(h.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker failed to stop after sink released")
	}
}

type captureHandler struct{ records chan slog.Record }

func (h captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records <- r.Clone()
	return nil
}
func (h captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h captureHandler) WithGroup(string) slog.Handler      { return h }
func TestDBStatsDeltasResetAndRate(t *testing.T) {
	r := &Recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks := make(chan time.Time)
	records := make(chan slog.Record, 10)
	done := make(chan struct{})
	initialized := make(chan struct{})
	calls := 0
	fakeNow := time.Now()
	stats := func() sql.DBStats {
		calls++
		switch calls {
		case 1:
			close(initialized)
			return sql.DBStats{WaitCount: 5, WaitDuration: 2 * time.Second}
		case 2:
			return sql.DBStats{WaitCount: 8, WaitDuration: 3 * time.Second}
		default:
			return sql.DBStats{WaitCount: 1, WaitDuration: time.Millisecond}
		}
	}
	go func() {
		defer close(done)
		r.run(ctx, stats, slog.New(captureHandler{records}), "https://secret", ticks, func() time.Time { return fakeNow })
	}()
	<-initialized
	fakeNow = fakeNow.Add(15 * time.Second)
	ticks <- fakeNow
	first := <-records
	attrs := map[string]slog.Value{}
	first.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value; return true })
	if attrs["waits"].Int64() != 3 || attrs["wait_ms"].Int64() != 1000 || attrs["reset"].Bool() || attrs["release"].String() != "unknown" {
		t.Fatal(attrs)
	}
	fakeNow = fakeNow.Add(46 * time.Second)
	// A queued ticker timestamp must not understate elapsed time after a slow sink.
	ticks <- fakeNow.Add(-45 * time.Second)
	second := <-records
	second.Attrs(func(a slog.Attr) bool { attrs[a.Key] = a.Value; return true })
	if !attrs["reset"].Bool() || attrs["waits"].Int64() != 1 || attrs["wait_ms"].Int64() != 1 || attrs["interval_ms"].Int64() != 46000 {
		t.Fatal(attrs)
	}
	if record := <-records; record.Message != "cms_stages" {
		t.Fatal("missing periodic summary")
	}
	cancel()
	<-done
	if len(records) != 0 {
		t.Fatal("unexpected final flush")
	}
}

func TestLaunchBoundsShutdownWithBlockedSink(t *testing.T) {
	h := &blockingHandler{entered: make(chan struct{}), release: make(chan struct{})}
	_, stop := Launch(context.Background(), func() sql.DBStats { return sql.DBStats{} }, slog.New(h), "unknown")
	defer func() { close(h.release); stop() }()
	select {
	case <-h.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("summary worker never entered sink")
	}
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown waited for blocked console")
	}
}
