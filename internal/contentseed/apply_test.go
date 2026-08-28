package contentseed

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func TestApplyStopsBeforeWritesOnWarningsOrConflicts(t *testing.T) {
	for _, test := range []struct {
		name      string
		warnings  int
		conflicts int
	}{
		{name: "warnings", warnings: 1},
		{name: "conflicts", conflicts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, state := newSeedTestDB(t)
			report, err := apply(context.Background(), db, testManifest(), strings.Repeat("a", 64), "content-seed:v1", func(context.Context, seedQuerier, Manifest) (applyPlan, error) {
				return applyPlan{Report: Report{Inserts: 1, Warnings: test.warnings, Conflicts: test.conflicts}}, nil
			}, successfulTestRecord)
			if err == nil {
				t.Fatal("expected preflight error")
			}
			if report.Conflicts != test.conflicts || report.Warnings != test.warnings {
				t.Fatalf("report = %#v", report)
			}
			if report.Conflicts == 0 && report.Warnings == 0 {
				t.Fatal("apply must stop before inserts")
			}
			state.assertCounts(t, 0, 0, 0)
			state.assertUnlocked(t)
		})
	}
}

func TestApplyReturnsPriorSuccessfulResultForSameVersionAndSHA(t *testing.T) {
	db, state := newSeedTestDB(t)
	manifestSHA := strings.Repeat("b", 64)
	state.successful["v1"] = seedTestSuccess{manifestSHA: manifestSHA, inserts: 2, skips: 3}

	report, err := Apply(context.Background(), db, testManifest(), manifestSHA, "content-seed:v1")
	if err != nil {
		t.Fatal(err)
	}
	if report != (Report{Mode: "apply", SeedVersion: "v1", ManifestSHA256: manifestSHA, Inserts: 2, Skips: 3}) {
		t.Fatalf("report = %#v", report)
	}
	state.assertCounts(t, 0, 0, 0)
	state.assertUnlocked(t)
}

func TestApplyRejectsSuccessfulVersionWithDifferentSHA(t *testing.T) {
	db, state := newSeedTestDB(t)
	state.successful["v1"] = seedTestSuccess{manifestSHA: strings.Repeat("a", 64)}

	_, err := Apply(context.Background(), db, testManifest(), strings.Repeat("b", 64), "content-seed:v1")
	if err == nil || !strings.Contains(err.Error(), "already succeeded with a different manifest SHA") {
		t.Fatalf("error = %v", err)
	}
	state.assertCounts(t, 0, 0, 0)
	state.assertUnlocked(t)
}

func TestApplyCommitsDomainAndProvenanceAtomically(t *testing.T) {
	db, state := newSeedTestDB(t)
	manifest := testManifest()
	manifest.Records = []Record{
		{Kind: "location", SourceKey: "one", SourcePaths: []string{"source.json"}, Payload: json.RawMessage(`{}`)},
		{Kind: "location", SourceKey: "two", SourcePaths: []string{"source.json"}, Payload: json.RawMessage(`{}`)},
	}
	planned := []PlannedRecord{
		{Kind: "location", SourceKey: "one", RecordSHA256: strings.Repeat("1", 64), Action: ActionInsert},
		{Kind: "location", SourceKey: "two", RecordSHA256: strings.Repeat("2", 64), Action: ActionInsert},
	}

	report, err := apply(context.Background(), db, manifest, strings.Repeat("c", 64), "content-seed:v1", func(context.Context, seedQuerier, Manifest) (applyPlan, error) {
		return applyPlan{Report: Report{Inserts: 2}, Records: planned}, nil
	}, successfulTestRecord)
	if err != nil {
		t.Fatal(err)
	}
	if report.Inserts != 2 || report.Skips != 0 || report.Updates != 0 || report.Deletes != 0 || report.Warnings != 0 || report.Conflicts != 0 {
		t.Fatalf("report = %#v", report)
	}
	state.assertCounts(t, 1, 2, 2)
	state.assertUnlocked(t)
}

func TestApplyRollsBackAllRecordsAndCanRetryFailedVersion(t *testing.T) {
	db, state := newSeedTestDB(t)
	manifest := testManifest()
	manifest.Records = []Record{
		{Kind: "location", SourceKey: "one", SourcePaths: []string{"source.json"}, Payload: json.RawMessage(`{}`)},
		{Kind: "location", SourceKey: "two", SourcePaths: []string{"source.json"}, Payload: json.RawMessage(`{}`)},
	}
	planned := []PlannedRecord{
		{Kind: "location", SourceKey: "one", RecordSHA256: strings.Repeat("1", 64), Action: ActionInsert},
		{Kind: "location", SourceKey: "two", RecordSHA256: strings.Repeat("2", 64), Action: ActionInsert},
	}
	planner := func(context.Context, seedQuerier, Manifest) (applyPlan, error) {
		return applyPlan{Report: Report{Inserts: 2}, Records: planned}, nil
	}
	failSecond := func(ctx context.Context, tx *sql.Tx, record Record) (string, error) {
		if record.SourceKey == "two" {
			return "", errors.New("record failed")
		}
		return successfulTestRecord(ctx, tx, record)
	}
	manifestSHA := strings.Repeat("d", 64)

	if _, err := apply(context.Background(), db, manifest, manifestSHA, "content-seed:v1", planner, failSecond); err == nil {
		t.Fatal("expected record failure")
	}
	state.assertCounts(t, 1, 0, 0)
	if state.attemptStatus(0) != "failed" {
		t.Fatalf("attempt status = %q", state.attemptStatus(0))
	}

	report, err := apply(context.Background(), db, manifest, manifestSHA, "content-seed:v1", planner, successfulTestRecord)
	if err != nil {
		t.Fatal(err)
	}
	if report.Inserts != 2 || report.Updates != 0 || report.Deletes != 0 {
		t.Fatalf("report = %#v", report)
	}
	state.assertCounts(t, 2, 2, 2)
	if state.attemptStatus(1) != "succeeded" {
		t.Fatalf("retry status = %q", state.attemptStatus(1))
	}
	state.assertUnlocked(t)
	if len(state.lockIDs) != 2 || state.lockIDs[0] != state.lockIDs[1] {
		t.Fatalf("lock IDs = %v", state.lockIDs)
	}
}

func TestApplyTargetKindsFailClosed(t *testing.T) {
	for _, kind := range []string{"location", "site_layout", "page"} {
		t.Run(kind, func(t *testing.T) {
			_, err := applyRecord(context.Background(), nil, Record{Kind: kind})
			want := fmt.Sprintf("target kind %q is not released", kind)
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
		})
	}
	_, err := applyRecord(context.Background(), nil, Record{Kind: "other"})
	if err == nil || err.Error() != `unsupported target kind "other"` {
		t.Fatalf("unsupported error = %v", err)
	}
}

func successfulTestRecord(ctx context.Context, tx *sql.Tx, record Record) (string, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO test_domain(source_key) VALUES($1)`, record.SourceKey); err != nil {
		return "", err
	}
	return "target-" + record.SourceKey, nil
}

func testManifest() Manifest {
	return Manifest{
		SeedVersion:  "v1",
		SourceRepo:   "repo",
		SourceCommit: strings.Repeat("a", 40),
		Sources:      []Source{{Path: "source.json", SHA256: strings.Repeat("b", 64)}},
	}
}

type seedTestSuccess struct {
	manifestSHA string
	inserts     int
	skips       int
	warnings    int
	conflicts   int
}

type seedTestAttempt struct {
	id          string
	seedVersion string
	manifestSHA string
	status      string
	inserts     int
	skips       int
	warnings    int
	conflicts   int
}

type seedTestState struct {
	mu         sync.Mutex
	locks      int
	lockIDs    []int64
	attempts   []seedTestAttempt
	successful map[string]seedTestSuccess
	domain     []string
	provenance []string
}

func newSeedTestDB(t *testing.T) (*sql.DB, *seedTestState) {
	t.Helper()
	state := &seedTestState{successful: make(map[string]seedTestSuccess)}
	db := sql.OpenDB(seedTestConnector{state: state})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, state
}

func (state *seedTestState) assertCounts(t *testing.T, attempts, domain, provenance int) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.attempts) != attempts || len(state.domain) != domain || len(state.provenance) != provenance {
		t.Fatalf("counts attempts/domain/provenance = %d/%d/%d, want %d/%d/%d", len(state.attempts), len(state.domain), len(state.provenance), attempts, domain, provenance)
	}
}

func (state *seedTestState) assertUnlocked(t *testing.T) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.locks != 0 {
		t.Fatalf("advisory locks held = %d", state.locks)
	}
}

func (state *seedTestState) attemptStatus(index int) string {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.attempts[index].status
}

type seedTestConnector struct{ state *seedTestState }

func (connector seedTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &seedTestConn{state: connector.state}, nil
}

func (connector seedTestConnector) Driver() driver.Driver {
	return seedTestDriver{state: connector.state}
}

type seedTestDriver struct{ state *seedTestState }

func (testDriver seedTestDriver) Open(string) (driver.Conn, error) {
	return &seedTestConn{state: testDriver.state}, nil
}

type seedTestConn struct {
	state *seedTestState
	tx    *seedTestTxState
}

func (conn *seedTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (conn *seedTestConn) Close() error { return nil }

func (conn *seedTestConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}

func (conn *seedTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if conn.tx != nil {
		return nil, errors.New("transaction already active")
	}
	conn.tx = &seedTestTxState{}
	return seedTestTx{conn: conn}, nil
}

func (conn *seedTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "FROM hhc_web.content_seed_run") || !strings.Contains(query, "status='succeeded'") {
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
	seedVersion := args[0].Value.(string)
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	success, ok := conn.state.successful[seedVersion]
	rows := &seedTestRows{columns: []string{"manifest_sha256", "inserted_count", "skipped_count", "warning_count", "conflict_count"}}
	if ok {
		rows.values = [][]driver.Value{{success.manifestSHA, int64(success.inserts), int64(success.skips), int64(success.warnings), int64(success.conflicts)}}
	}
	return rows, nil
}

func (conn *seedTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	switch {
	case strings.Contains(query, "pg_advisory_lock"):
		conn.state.locks++
		conn.state.lockIDs = append(conn.state.lockIDs, args[0].Value.(int64))
	case strings.Contains(query, "pg_advisory_unlock"):
		conn.state.locks--
	case strings.Contains(query, "INSERT INTO hhc_web.content_seed_run"):
		conn.state.attempts = append(conn.state.attempts, seedTestAttempt{
			id: args[0].Value.(string), seedVersion: args[1].Value.(string), manifestSHA: args[4].Value.(string), status: "started",
		})
	case strings.Contains(query, "INSERT INTO test_domain"):
		conn.tx.domain = append(conn.tx.domain, args[0].Value.(string))
	case strings.Contains(query, "INSERT INTO hhc_web.content_seed_source"):
		conn.tx.provenance = append(conn.tx.provenance, args[3].Value.(string))
	case strings.Contains(query, "SET status='succeeded'"):
		conn.tx.success = &seedTestAttempt{id: args[4].Value.(string), status: "succeeded", inserts: int(args[0].Value.(int64)), skips: int(args[1].Value.(int64)), warnings: int(args[2].Value.(int64)), conflicts: int(args[3].Value.(int64))}
	case strings.Contains(query, "SET status='failed'"):
		conn.setAttemptStatus(args[0].Value.(string), "failed", nil)
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
	return driver.RowsAffected(1), nil
}

func (conn *seedTestConn) setAttemptStatus(id, status string, counts *seedTestAttempt) {
	for i := range conn.state.attempts {
		if conn.state.attempts[i].id != id {
			continue
		}
		conn.state.attempts[i].status = status
		if counts != nil {
			conn.state.attempts[i].inserts = counts.inserts
			conn.state.attempts[i].skips = counts.skips
			conn.state.attempts[i].warnings = counts.warnings
			conn.state.attempts[i].conflicts = counts.conflicts
			conn.state.successful[conn.state.attempts[i].seedVersion] = seedTestSuccess{
				manifestSHA: conn.state.attempts[i].manifestSHA, inserts: counts.inserts, skips: counts.skips, warnings: counts.warnings, conflicts: counts.conflicts,
			}
		}
		return
	}
}

type seedTestTxState struct {
	domain     []string
	provenance []string
	success    *seedTestAttempt
}

type seedTestTx struct{ conn *seedTestConn }

func (tx seedTestTx) Commit() error {
	tx.conn.state.mu.Lock()
	defer tx.conn.state.mu.Unlock()
	tx.conn.state.domain = append(tx.conn.state.domain, tx.conn.tx.domain...)
	tx.conn.state.provenance = append(tx.conn.state.provenance, tx.conn.tx.provenance...)
	if tx.conn.tx.success != nil {
		tx.conn.setAttemptStatus(tx.conn.tx.success.id, "succeeded", tx.conn.tx.success)
	}
	tx.conn.tx = nil
	return nil
}

func (tx seedTestTx) Rollback() error {
	tx.conn.tx = nil
	return nil
}

type seedTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (rows *seedTestRows) Columns() []string { return rows.columns }
func (rows *seedTestRows) Close() error      { return nil }
func (rows *seedTestRows) Next(dest []driver.Value) error {
	if rows.index == len(rows.values) {
		return io.EOF
	}
	copy(dest, rows.values[rows.index])
	rows.index++
	return nil
}
