package dbphase

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		dsn = os.Getenv("HHW_TEST_DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("disposable PostgreSQL required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" {
		t.Fatal("loopback fixture required")
	}
	return cfg
}
func TestRealPoolWaitAndQuery(t *testing.T) {
	db := Open(*testConfig(t))
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	held, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	before := db.Stats()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	ctx, o := Observe(ctx)
	_, err = db.Conn(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	timing := o.Snapshot()
	if timing.ConnectCount != 0 || timing.ResetCount != 0 || db.Stats().WaitCount-before.WaitCount != 1 {
		t.Fatalf("poolwait attribution %+v", timing)
	}
	held.Close()
	ctx, o = Observe(t.Context())
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	beforeQuery := o.Snapshot()
	started := time.Now()
	_, err = conn.ExecContext(ctx, "SELECT pg_sleep(0.05)")
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) < 40*time.Millisecond || o.Snapshot() != beforeQuery {
		t.Fatal("query attributed to checkout")
	}
}

type gatedConn struct {
	net.Conn
	blocked *atomic.Bool
	closed  chan struct{}
	once    sync.Once
}

func (c *gatedConn) Read(p []byte) (int, error) {
	if c.blocked.Load() {
		select {
		case <-c.closed:
			return 0, net.ErrClosed
		case <-time.After(100 * time.Millisecond):
		}
	}
	return c.Conn.Read(p)
}
func (c *gatedConn) Close() error { c.once.Do(func() { close(c.closed) }); return c.Conn.Close() }
func TestRealResetPingCancellation(t *testing.T) {
	cfg := testConfig(t)
	var blocked atomic.Bool
	cfg.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &gatedConn{Conn: c, blocked: &blocked, closed: make(chan struct{})}, nil
	}
	db := sql.OpenDB(connector{Connector: stdlib.GetConnector(*cfg, stdlib.OptionShouldPing(func(context.Context, stdlib.ShouldPingParams) bool { return true }))})
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	blocked.Store(true)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	ctx, o := Observe(ctx)
	_, err = db.Conn(ctx)
	if err == nil {
		t.Fatal("blocked validation succeeded")
	}
	s := o.Snapshot()
	if s.ResetCount < 1 || s.Reset < 40*time.Millisecond {
		t.Fatalf("reset missing: %+v", s)
	}
}
func TestPGXConnectCancellation(t *testing.T) {
	cfg := testConfig(t)
	cfg.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
	db := Open(*cfg)
	defer db.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	ctx, o := Observe(ctx)
	_, err := db.Conn(ctx)
	if err == nil {
		t.Fatal("blocked connect succeeded")
	}
	s := o.Snapshot()
	if s.Connect < 30*time.Millisecond || s.ResetCount != 0 {
		t.Fatalf("connect attribution %+v", s)
	}
}
