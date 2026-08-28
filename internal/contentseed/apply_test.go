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

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
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

func TestApplyLocationsInsertsThenSkipsMatchingProvenanceInCumulativeManifest(t *testing.T) {
	db, state := newSeedTestDB(t)
	taipei := locationSeedTestRecord("taipei")

	report, err := Apply(context.Background(), db, locationSeedTestManifest("locations-v1", taipei), strings.Repeat("a", 64), "content-seed:locations-v1")
	if err != nil {
		t.Fatal(err)
	}
	if report.Inserts != 1 || report.Skips != 0 {
		t.Fatalf("first report = %#v", report)
	}
	state.assertLocationSnapshot(t, "taipei")

	report, err = Apply(context.Background(), db, locationSeedTestManifest("locations-v2", taipei, locationSeedTestRecord("zhongli")), strings.Repeat("b", 64), "content-seed:locations-v2")
	if err != nil {
		t.Fatal(err)
	}
	if report.Inserts != 1 || report.Skips != 1 || report.Conflicts != 0 {
		t.Fatalf("cumulative report = %#v", report)
	}
	state.assertCounts(t, 2, 2, 3)
	state.assertLocationTargets(t, "taipei", "zhongli")
	state.assertLocationSnapshot(t, "zhongli")
	state.assertActors(t, "content-seed:locations-v1", "content-seed:locations-v1", "content-seed:locations-v2", "content-seed:locations-v2")
	state.assertUnlocked(t)
}

func TestPlanLocationRejectsSourceKeyThatDoesNotMatchStableKey(t *testing.T) {
	db, state := newSeedTestDB(t)
	record := locationSeedTestRecord("taipei")
	record.SourceKey = "location:zhongli"

	_, err := Plan(context.Background(), db, locationSeedTestManifest("locations-v1", record))
	if err == nil || !strings.Contains(err.Error(), `sourceKey must equal "location:taipei"`) {
		t.Fatalf("error = %v", err)
	}
	state.assertCounts(t, 0, 0, 0)
}

func TestPlanLocationConflictsWithUnownedNaturalKey(t *testing.T) {
	db, state := newSeedTestDB(t)
	state.targets["taipei"] = "unowned-location-id"

	report, err := Plan(context.Background(), db, locationSeedTestManifest("locations-v1", locationSeedTestRecord("taipei")))
	if err != nil {
		t.Fatal(err)
	}
	if report.InsertCount != 0 || report.SkipCount != 0 || report.ConflictCount != 1 || report.Records[0].Action != ActionConflict {
		t.Fatalf("report = %#v", report)
	}
	state.assertCounts(t, 0, 0, 0)
}

func TestApplyLocationsRollsBackWholeManifestOnTranslationFailure(t *testing.T) {
	db, state := newSeedTestDB(t)
	state.failTranslationAt = 6
	manifest := locationSeedTestManifest("locations-v1", locationSeedTestRecord("taipei"), locationSeedTestRecord("zhongli"))

	_, err := Apply(context.Background(), db, manifest, strings.Repeat("c", 64), "content-seed:locations-v1")
	if err == nil || !strings.Contains(err.Error(), "translation failed") {
		t.Fatalf("error = %v", err)
	}
	state.assertCounts(t, 1, 0, 0)
	state.assertLocationTargets(t)
	if state.attemptStatus(0) != "failed" {
		t.Fatalf("attempt status = %q", state.attemptStatus(0))
	}
	state.assertUnlocked(t)
}

func TestApplyConcurrentDifferentSHAsAllowOneSuccessfulVersion(t *testing.T) {
	db, state := newSeedTestDB(t)
	manifest := testManifest()
	manifest.Records = []Record{{Kind: "location", SourceKey: "one", SourcePaths: []string{"source.json"}, Payload: json.RawMessage(`{}`)}}
	planner := func(context.Context, seedQuerier, Manifest) (applyPlan, error) {
		return applyPlan{
			Report:  Report{Inserts: 1},
			Records: []PlannedRecord{{Kind: "location", SourceKey: "one", RecordSHA256: strings.Repeat("1", 64), Action: ActionInsert}},
		}, nil
	}
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	applier := func(ctx context.Context, tx *sql.Tx, record Record) (string, error) {
		ready <- struct{}{}
		<-release
		return successfulTestRecord(ctx, tx, record)
	}
	type result struct {
		report Report
		err    error
	}
	results := make(chan result, 2)
	for _, manifestSHA := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		go func() {
			report, err := apply(context.Background(), db, manifest, manifestSHA, "content-seed:v1", planner, applier)
			results <- result{report: report, err: err}
		}()
	}
	<-ready
	<-ready
	close(release)
	first, second := <-results, <-results
	succeeded, failed := 0, 0
	for _, result := range []result{first, second} {
		if result.err == nil {
			succeeded++
			if result.report.Inserts != 1 {
				t.Fatalf("successful report = %#v", result.report)
			}
			continue
		}
		failed++
		if !strings.Contains(result.err.Error(), "commit content seed run") || !strings.Contains(result.err.Error(), "duplicate successful seed version") {
			t.Fatalf("failed error = %v", result.err)
		}
	}
	if succeeded != 1 || failed != 1 {
		t.Fatalf("succeeded/failed = %d/%d", succeeded, failed)
	}
	state.assertCounts(t, 2, 1, 1)
	if succeededAttempts, failedAttempts := state.statusCounts(); succeededAttempts != 1 || failedAttempts != 1 {
		t.Fatalf("attempt statuses succeeded/failed = %d/%d", succeededAttempts, failedAttempts)
	}
	state.assertUnlocked(t)
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.lockIDs) != 2 || state.lockIDs[0] == state.lockIDs[1] {
		t.Fatalf("lock IDs = %v", state.lockIDs)
	}
}

func TestApplyCancellationAfterLockStillUnlocks(t *testing.T) {
	db, state := newSeedTestDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	state.cancelAfterLock = cancel

	_, err := Apply(ctx, db, testManifest(), strings.Repeat("f", 64), "content-seed:v1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	state.assertCounts(t, 0, 0, 0)
	state.assertUnlocked(t)
}

func TestApplySQLFailureRollsBackAndMarksAttemptFailed(t *testing.T) {
	for _, operation := range []string{"provenance", "success"} {
		t.Run(operation, func(t *testing.T) {
			db, state := newSeedTestDB(t)
			injected := fmt.Errorf("%s failed", operation)
			state.failures[operation] = injected
			manifest := testManifest()
			manifest.Records = []Record{{Kind: "location", SourceKey: "one", SourcePaths: []string{"source.json"}, Payload: json.RawMessage(`{}`)}}
			planner := func(context.Context, seedQuerier, Manifest) (applyPlan, error) {
				return applyPlan{
					Report:  Report{Inserts: 1},
					Records: []PlannedRecord{{Kind: "location", SourceKey: "one", RecordSHA256: strings.Repeat("1", 64), Action: ActionInsert}},
				}, nil
			}

			report, err := apply(context.Background(), db, manifest, strings.Repeat("1", 64), "content-seed:v1", planner, successfulTestRecord)
			if !errors.Is(err, injected) {
				t.Fatalf("error = %v", err)
			}
			if report.Inserts != 1 || report.Updates != 0 || report.Deletes != 0 {
				t.Fatalf("report = %#v", report)
			}
			state.assertCounts(t, 1, 0, 0)
			if state.attemptStatus(0) != "failed" {
				t.Fatalf("attempt status = %q", state.attemptStatus(0))
			}
			state.assertUnlocked(t)
		})
	}
}

func TestApplyUnlockFailureIsReturnedAndConnectionReleased(t *testing.T) {
	t.Run("successful apply", func(t *testing.T) {
		db, state := newSeedTestDB(t)
		unlockErr := errors.New("unlock failed")
		state.failures["unlock"] = unlockErr

		report, err := Apply(context.Background(), db, testManifest(), strings.Repeat("2", 64), "content-seed:v1")
		if !errors.Is(err, unlockErr) || report.Mode != "apply" {
			t.Fatalf("report=%#v error=%v", report, err)
		}
		state.assertCounts(t, 1, 0, 0)
		if state.attemptStatus(0) != "succeeded" {
			t.Fatalf("attempt status = %q", state.attemptStatus(0))
		}
		state.assertUnlocked(t)
	})

	t.Run("joined with primary error", func(t *testing.T) {
		db, state := newSeedTestDB(t)
		primaryErr := errors.New("plan failed")
		unlockErr := errors.New("unlock failed")
		state.failures["unlock"] = unlockErr

		_, err := apply(context.Background(), db, testManifest(), strings.Repeat("3", 64), "content-seed:v1", func(context.Context, seedQuerier, Manifest) (applyPlan, error) {
			return applyPlan{}, primaryErr
		}, successfulTestRecord)
		if !errors.Is(err, primaryErr) || !errors.Is(err, unlockErr) {
			t.Fatalf("error = %v", err)
		}
		state.assertCounts(t, 0, 0, 0)
		state.assertUnlocked(t)
	})
}

func TestApplyTargetKindsFailClosed(t *testing.T) {
	for _, kind := range []string{"site_layout", "page"} {
		t.Run(kind, func(t *testing.T) {
			db, state := newSeedTestDB(t)
			manifest := testManifest()
			manifest.Records = []Record{{Kind: kind, SourceKey: "one", SourcePaths: []string{"source.json"}, Payload: json.RawMessage(`{}`)}}
			_, err := Apply(context.Background(), db, manifest, strings.Repeat("e", 64), "content-seed:v1")
			want := fmt.Sprintf("target kind %q is not released", kind)
			if err == nil || err.Error() != want {
				t.Fatalf("error = %v, want %q", err, want)
			}
			state.assertCounts(t, 0, 0, 0)
			state.assertUnlocked(t)
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

func locationSeedTestManifest(version string, records ...Record) Manifest {
	return Manifest{
		SeedVersion:  version,
		SourceRepo:   "repo",
		SourceCommit: strings.Repeat("a", 40),
		Sources:      []Source{{Path: "src/features/locations/mock-data.ts", SHA256: strings.Repeat("b", 64)}},
		Records:      records,
	}
}

func locationSeedTestRecord(stableKey string) Record {
	return Record{
		Kind:        "location",
		SourceKey:   "location:" + stableKey,
		SourcePaths: []string{"src/features/locations/mock-data.ts"},
		Payload: json.RawMessage(fmt.Sprintf(`{
			"stableKey":%q,
			"mapHref":"https://maps.app.goo.gl/fDus6nVswbuhSEAd8",
			"sortOrder":10,
			"translations":[
				{"locale":"zh-Hant","name":"名稱","address":"地址"},
				{"locale":"zh-Hans","name":"名称","address":"地址"},
				{"locale":"en","name":"Name","address":"Address"},
				{"locale":"ja","name":"名前","address":"Address"},
				{"locale":"ko","name":"이름","address":"Address"}
			]
		}`, stableKey)),
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
	mu                sync.Mutex
	locks             int
	lockIDs           []int64
	attempts          []seedTestAttempt
	successful        map[string]seedTestSuccess
	domain            []string
	provenance        []seedTestProvenance
	targets           map[string]string
	revisions         map[string]json.RawMessage
	translations      map[string]int
	actors            []string
	failTranslationAt int
	failures          map[string]error
	cancelAfterLock   context.CancelFunc
}

func newSeedTestDB(t *testing.T) (*sql.DB, *seedTestState) {
	t.Helper()
	state := &seedTestState{successful: make(map[string]seedTestSuccess), targets: make(map[string]string), revisions: make(map[string]json.RawMessage), translations: make(map[string]int), failures: make(map[string]error)}
	db := sql.OpenDB(seedTestConnector{state: state})
	db.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db, state
}

func (state *seedTestState) assertLocationTargets(t *testing.T, keys ...string) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.targets) != len(keys) {
		t.Fatalf("location target count = %d, want %d", len(state.targets), len(keys))
	}
	for _, key := range keys {
		if state.targets[key] == "" {
			t.Fatalf("location target %q missing", key)
		}
	}
}

func (state *seedTestState) assertLocationSnapshot(t *testing.T, stableKey string) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	id := state.targets[stableKey]
	var snapshot content.Item
	if err := json.Unmarshal(state.revisions[id], &snapshot); err != nil {
		t.Fatalf("location %q revision snapshot: %v", stableKey, err)
	}
	if snapshot.LocationKey != stableKey || snapshot.MapHref != "https://maps.app.goo.gl/fDus6nVswbuhSEAd8" || snapshot.SortOrder != 10 || state.translations[id] != 5 {
		t.Fatalf("location %q snapshot=%#v translations=%d", stableKey, snapshot, state.translations[id])
	}
	want := []content.Translation{
		{Locale: "zh-Hant", Title: "名稱", Body: "地址"},
		{Locale: "zh-Hans", Title: "名称", Body: "地址"},
		{Locale: "en", Title: "Name", Body: "Address"},
		{Locale: "ja", Title: "名前", Body: "Address"},
		{Locale: "ko", Title: "이름", Body: "Address"},
	}
	if fmt.Sprint(snapshot.Translations) != fmt.Sprint(want) {
		t.Fatalf("location %q translations=%#v", stableKey, snapshot.Translations)
	}
}

func (state *seedTestState) assertActors(t *testing.T, actors ...string) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	if fmt.Sprint(state.actors) != fmt.Sprint(actors) {
		t.Fatalf("actors = %v, want %v", state.actors, actors)
	}
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

func (state *seedTestState) statusCounts() (succeeded, failed int) {
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, attempt := range state.attempts {
		switch attempt.status {
		case "succeeded":
			succeeded++
		case "failed":
			failed++
		}
	}
	return succeeded, failed
}

type seedTestConnector struct{ state *seedTestState }

func (connector seedTestConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	locks int
}

func (conn *seedTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (conn *seedTestConn) Close() error {
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	conn.state.locks -= conn.locks
	conn.locks = 0
	return nil
}

func (conn *seedTestConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}

func (conn *seedTestConn) BeginTx(ctx context.Context, _ driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if conn.tx != nil {
		return nil, errors.New("transaction already active")
	}
	conn.tx = &seedTestTxState{targets: make(map[string]string), revisions: make(map[string]json.RawMessage), translations: make(map[string]int)}
	return seedTestTx{conn: conn}, nil
}

func (conn *seedTestConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	switch {
	case strings.Contains(query, "FROM hhc_web.content_seed_run") && strings.Contains(query, "status='succeeded'"):
		if err := conn.state.failures["lookup"]; err != nil {
			return nil, err
		}
		success, ok := conn.state.successful[args[0].Value.(string)]
		rows := &seedTestRows{columns: []string{"manifest_sha256", "inserted_count", "skipped_count", "warning_count", "conflict_count"}}
		if ok {
			rows.values = [][]driver.Value{{success.manifestSHA, int64(success.inserts), int64(success.skips), int64(success.warnings), int64(success.conflicts)}}
		}
		return rows, nil
	case strings.Contains(query, "FROM hhc_web.location_item"):
		rows := &seedTestRows{columns: []string{"content_id"}}
		if targetID := conn.state.targets[args[0].Value.(string)]; targetID != "" {
			rows.values = [][]driver.Value{{targetID}}
		}
		return rows, nil
	case strings.Contains(query, "FROM hhc_web.content_seed_source"):
		matching := false
		for _, provenance := range conn.state.provenance {
			if provenance.kind == args[0].Value && provenance.sourceKey == args[1].Value && provenance.recordHash == args[2].Value && provenance.targetID == args[3].Value {
				matching = true
			}
		}
		return &seedTestRows{columns: []string{"exists"}, values: [][]driver.Value{{matching}}}, nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

func (conn *seedTestConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn.state.mu.Lock()
	defer conn.state.mu.Unlock()
	operation := seedTestOperation(query)
	if err := conn.state.failures[operation]; err != nil {
		return nil, err
	}
	switch {
	case strings.Contains(query, "pg_advisory_lock"):
		conn.state.locks++
		conn.locks++
		conn.state.lockIDs = append(conn.state.lockIDs, args[0].Value.(int64))
		if conn.state.cancelAfterLock != nil {
			conn.state.cancelAfterLock()
		}
	case strings.Contains(query, "pg_advisory_unlock"):
		conn.state.locks--
		conn.locks--
	case strings.Contains(query, "INSERT INTO hhc_web.content_seed_run"):
		conn.state.attempts = append(conn.state.attempts, seedTestAttempt{
			id: args[0].Value.(string), seedVersion: args[1].Value.(string), manifestSHA: args[4].Value.(string), status: "started",
		})
	case strings.Contains(query, "INSERT INTO test_domain"):
		conn.tx.domain = append(conn.tx.domain, args[0].Value.(string))
	case strings.Contains(query, "INSERT INTO hhc_web.content_entry"):
		conn.tx.domain = append(conn.tx.domain, args[2].Value.(string))
		conn.tx.actors = append(conn.tx.actors, args[4].Value.(string))
	case strings.Contains(query, "INSERT INTO hhc_web.content_translation"):
		conn.tx.translationCount++
		conn.tx.translations[args[0].Value.(string)]++
		if conn.state.failTranslationAt == conn.tx.translationCount {
			return nil, errors.New("translation failed")
		}
	case strings.Contains(query, "INSERT INTO hhc_web.location_item"):
		conn.tx.targets[args[1].Value.(string)] = args[0].Value.(string)
	case strings.Contains(query, "INSERT INTO hhc_web.content_revision"):
		conn.tx.actors = append(conn.tx.actors, args[2].Value.(string))
		snapshot, ok := args[1].Value.([]byte)
		if !ok {
			return nil, fmt.Errorf("revision snapshot has type %T", args[1].Value)
		}
		conn.tx.revisions[args[0].Value.(string)] = append(json.RawMessage(nil), snapshot...)
	case strings.Contains(query, "INSERT INTO hhc_web.content_seed_source"):
		conn.tx.provenance = append(conn.tx.provenance, seedTestProvenance{sourceKey: args[3].Value.(string), recordHash: args[5].Value.(string), kind: args[6].Value.(string), targetID: args[7].Value.(string)})
	case strings.Contains(query, "SET status='succeeded'"):
		conn.tx.success = &seedTestAttempt{id: args[4].Value.(string), status: "succeeded", inserts: int(args[0].Value.(int64)), skips: int(args[1].Value.(int64)), warnings: int(args[2].Value.(int64)), conflicts: int(args[3].Value.(int64))}
	case strings.Contains(query, "SET status='failed'"):
		conn.setAttemptStatus(args[0].Value.(string), "failed", nil)
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
	return driver.RowsAffected(1), nil
}

func seedTestOperation(query string) string {
	switch {
	case strings.Contains(query, "pg_advisory_lock"):
		return "lock"
	case strings.Contains(query, "pg_advisory_unlock"):
		return "unlock"
	case strings.Contains(query, "INSERT INTO hhc_web.content_seed_source"):
		return "provenance"
	case strings.Contains(query, "SET status='succeeded'"):
		return "success"
	case strings.Contains(query, "SET status='failed'"):
		return "failure"
	case strings.Contains(query, "INSERT INTO hhc_web.content_seed_run"):
		return "attempt"
	case strings.Contains(query, "INSERT INTO test_domain"):
		return "domain"
	case strings.Contains(query, "INSERT INTO hhc_web.content_entry"):
		return "content entry"
	case strings.Contains(query, "INSERT INTO hhc_web.content_translation"):
		return "translation"
	case strings.Contains(query, "INSERT INTO hhc_web.location_item"):
		return "location"
	case strings.Contains(query, "INSERT INTO hhc_web.content_revision"):
		return "revision"
	default:
		return "unexpected"
	}
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
	domain           []string
	provenance       []seedTestProvenance
	targets          map[string]string
	revisions        map[string]json.RawMessage
	translations     map[string]int
	actors           []string
	translationCount int
	success          *seedTestAttempt
}

type seedTestProvenance struct {
	sourceKey  string
	recordHash string
	kind       string
	targetID   string
}

type seedTestTx struct{ conn *seedTestConn }

func (tx seedTestTx) Commit() error {
	tx.conn.state.mu.Lock()
	defer tx.conn.state.mu.Unlock()
	if tx.conn.tx.success != nil {
		for _, attempt := range tx.conn.state.attempts {
			if attempt.id == tx.conn.tx.success.id {
				if _, exists := tx.conn.state.successful[attempt.seedVersion]; exists {
					tx.conn.tx = nil
					return errors.New("duplicate successful seed version")
				}
				break
			}
		}
	}
	tx.conn.state.domain = append(tx.conn.state.domain, tx.conn.tx.domain...)
	tx.conn.state.provenance = append(tx.conn.state.provenance, tx.conn.tx.provenance...)
	for key, targetID := range tx.conn.tx.targets {
		tx.conn.state.targets[key] = targetID
	}
	for id, snapshot := range tx.conn.tx.revisions {
		tx.conn.state.revisions[id] = snapshot
	}
	for id, count := range tx.conn.tx.translations {
		tx.conn.state.translations[id] += count
	}
	tx.conn.state.actors = append(tx.conn.state.actors, tx.conn.tx.actors...)
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
