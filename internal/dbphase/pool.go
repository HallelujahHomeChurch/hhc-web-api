// Package dbphase observes pgx checkout phases without changing driver behavior.
package dbphase

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/multitracer"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"sync"
	"time"
)

type key struct{}
type Timings struct {
	Connect, Reset           time.Duration
	ConnectCount, ResetCount int
}
type Observation struct {
	mu      sync.Mutex
	timings Timings
}

func Observe(ctx context.Context) (context.Context, *Observation) {
	o := new(Observation)
	return context.WithValue(ctx, key{}, o), o
}
func (o *Observation) Snapshot() Timings { o.mu.Lock(); defer o.mu.Unlock(); return o.timings }
func record(ctx context.Context, phase string, started time.Time) {
	o, _ := ctx.Value(key{}).(*Observation)
	if o == nil {
		return
	}
	elapsed := time.Since(started)
	o.mu.Lock()
	defer o.mu.Unlock()
	if phase == "connect" {
		o.timings.Connect += elapsed
		o.timings.ConnectCount++
	} else {
		o.timings.Reset += elapsed
		o.timings.ResetCount++
	}
}

// Open is for runtime pools. Raw sees an embedded stdlib.Conn wrapper; migration
// code requiring the concrete *stdlib.Conn must retain its own unwrapped pool.
type Reporter func(context.Context, string, time.Duration, bool)

func Open(config pgx.ConnConfig) *sql.DB { return OpenReported(config, nil) }

// OpenReported retains runtime phase evidence; nil keeps observation-only behavior.
func OpenReported(config pgx.ConnConfig, report Reporter) *sql.DB {
	if report == nil {
		report = func(context.Context, string, time.Duration, bool) {}
	} else {
		config = *config.Copy()
		if revision := os.Getenv("CONTAINER_APP_REVISION"); revision != "" {
			valid := true
			for _, b := range []byte(revision) {
				if b < 32 || b > 126 {
					valid = false
					break
				}
			}
			if !valid {
				sum := sha256.Sum256([]byte(revision))
				revision = fmt.Sprintf("revision-%x", sum[:8])
			} else if len(revision) > 63 {
				sum := sha256.Sum256([]byte(revision))
				revision = fmt.Sprintf("%.46s-%x", revision, sum[:8])
			}
			config.RuntimeParams["application_name"] = revision
		}
		tracer := queryTracer{report: report}
		if config.Tracer == nil {
			config.Tracer = tracer
		} else {
			config.Tracer = multitracer.New(config.Tracer, tracer)
		}
	}
	return sql.OpenDB(connector{stdlib.GetConnector(config), report})
}

type connector struct {
	driver.Connector
	report Reporter
}

func (c connector) Connect(ctx context.Context) (driver.Conn, error) {
	started := time.Now()
	defer record(ctx, "connect", started)
	raw, err := c.Connector.Connect(ctx)
	if c.report != nil {
		c.report(ctx, "db_connect", time.Since(started), err != nil)
	}
	if err != nil {
		return nil, err
	}
	return &conn{raw.(*stdlib.Conn), c.report}, nil
}

// Embedding preserves pgx's optional driver interfaces, including NamedValueChecker.
type conn struct {
	*stdlib.Conn
	report Reporter
}

func (c *conn) ResetSession(ctx context.Context) error {
	started := time.Now()
	defer record(ctx, "reset", started)
	err := c.Conn.ResetSession(ctx)
	if c.report != nil {
		c.report(ctx, "db_reset", time.Since(started), err != nil)
	}
	return err
}

var _ driver.ConnBeginTx = (*conn)(nil)
var _ driver.ConnPrepareContext = (*conn)(nil)
var _ driver.ExecerContext = (*conn)(nil)
var _ driver.QueryerContext = (*conn)(nil)
var _ driver.Pinger = (*conn)(nil)
var _ driver.SessionResetter = (*conn)(nil)
var _ driver.NamedValueChecker = (*conn)(nil)

type queryKey struct{}
type queryTracer struct{ report Reporter }

func (t queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryKey{}, time.Now())
}
func (t queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	started, ok := ctx.Value(queryKey{}).(time.Time)
	if !ok {
		return
	}
	// pgx ends this at rows closure: includes result consumption, not server-only SQL time.
	t.report(ctx, "db_query_lifecycle", time.Since(started), data.Err != nil)
}
