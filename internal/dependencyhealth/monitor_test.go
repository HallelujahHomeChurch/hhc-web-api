package dependencyhealth

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

type testDriver struct{}
type testConn struct{}

func (testDriver) Open(string) (driver.Conn, error)  { return testConn{}, nil }
func (testConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("unused") }
func (testConn) Close() error                        { return nil }
func (testConn) Begin() (driver.Tx, error)           { return nil, errors.New("unused") }
func (testConn) Ping(context.Context) error          { return nil }
func init()                                          { sql.Register("dependency-health-test", testDriver{}) }

func TestDatabasePoolAcquisitionDeadlineAndRecovery(t *testing.T) {
	db, err := sql.Open("dependency-health-test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	held, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	phase, _, err := ping(ctx, db)
	if phase != "acquire" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s %v", phase, err)
	}
	held.Close()
	phase, _, err = ping(t.Context(), db)
	if phase != "ping" || err != nil {
		t.Fatalf("%s %v", phase, err)
	}
	if db.Stats().InUse != 0 {
		t.Fatal("leaked connection")
	}
}

func TestObserveClassifiesWithoutRawErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{{nil, "ok"}, {context.DeadlineExceeded, "deadline"}, {context.Canceled, "cancelled"}, {errors.New("private-password-url"), "unavailable"}} {
		var buf bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&buf, nil))
		observe(t.Context(), logger, "db", func(ctx context.Context) (string, int, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("unbounded")
			}
			return "ping", 0, tc.err
		}, &observationState{})
		if !strings.Contains(buf.String(), `"outcome":"`+tc.want+`"`) || strings.Contains(buf.String(), "private-password") {
			t.Fatal(buf.String())
		}
	}
}

func TestDaprRedirectDoesNotForwardToken(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1.0/healthz/outbound" || r.Header.Get("dapr-api-token") != "test-token" {
			t.Error("wrong request")
		}
		http.Redirect(w, r, "/other", 302)
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	phase, status, err := dapr(t.Context(), client, port, "test-token")
	if phase != "response_status" || status != 302 || err == nil || calls != 1 {
		t.Fatalf("phase=%s status=%d err=%v calls=%d", phase, status, err, calls)
	}
}

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type body struct{ closed bool }

func (*body) Read([]byte) (int, error) { return 0, io.EOF }
func (b *body) Close() error           { b.closed = true; return nil }
func TestDaprBodyCloseAndFailurePhase(t *testing.T) {
	for _, status := range []int{204, 500} {
		b := &body{}
		client := &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return &http.Response{StatusCode: status, Body: b}, nil })}
		_, got, err := dapr(t.Context(), client, 3500, "")
		if got != status || !b.closed || (err == nil) != (status == 204) {
			t.Fatal("bad response lifecycle")
		}
	}
	client := &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) { return nil, context.DeadlineExceeded })}
	phase, _, err := dapr(t.Context(), client, 3500, "")
	if phase != "response_headers" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s %v", phase, err)
	}
}
func TestLaunchCancelJoinsBlockedDatabaseCheck(t *testing.T) {
	db, err := sql.Open("dependency-health-test", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	held, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	stop := Launch(t.Context(), db, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	deadline := time.Now().Add(time.Second)
	for db.Stats().WaitCount == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if db.Stats().WaitCount == 0 {
		t.Fatal("monitor did not run")
	}
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("monitor did not join")
	}
}

func TestObservationTransitionsAndPersistentFailureHeartbeat(t *testing.T) {
	var state observationState
	now := time.Now()
	for _, tc := range []struct {
		after   time.Duration
		outcome string
		emit    bool
	}{
		{0, "ok", true}, {30 * time.Second, "ok", false},
		{time.Minute, "unavailable", true}, {90 * time.Second, "unavailable", false},
		{6 * time.Minute, "unavailable", true}, {390 * time.Second, "ok", true},
		{420 * time.Second, "ok", false}, {450 * time.Second, "cancelled", true},
	} {
		if got := state.emit(now.Add(tc.after), tc.outcome); got != tc.emit {
			t.Fatalf("after %s state %s: emit=%v want=%v", tc.after, tc.outcome, got, tc.emit)
		}
	}
}

func TestSamplesContinueWhileRepeatedFailuresAreQuiet(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	state := observationState{}
	calls := 0
	failure := errors.New("private dependency detail")
	check := func(context.Context) (string, int, error) { calls++; return "ping", 0, failure }
	observe(t.Context(), logger, "db", check, &state)
	observe(t.Context(), logger, "db", check, &state)
	if calls != 2 || strings.Count(logs.String(), "dependency_health") != 1 {
		t.Fatal("sampling was suppressed or repeated failure logged")
	}
	state.emitted = state.emitted.Add(-5 * time.Minute)
	observe(t.Context(), logger, "db", check, &state)
	failure = nil
	observe(t.Context(), logger, "db", check, &state)
	if calls != 4 || strings.Count(logs.String(), "dependency_health") != 3 || !strings.Contains(logs.String(), "outcome=ok") || strings.Contains(logs.String(), "private dependency") {
		t.Fatal(logs.String())
	}
}
