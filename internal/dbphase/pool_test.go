package dbphase

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

type blockedConnector struct{}

func (blockedConnector) Driver() driver.Driver { return nil }
func (blockedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestConnectCancellationObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	ctx, obs := Observe(ctx)
	_, err := (connector{Connector: blockedConnector{}}).Connect(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	s := obs.Snapshot()
	if s.Connect < 15*time.Millisecond || s.Reset != 0 || s.ConnectCount != 1 {
		t.Fatalf("%+v", s)
	}
}
