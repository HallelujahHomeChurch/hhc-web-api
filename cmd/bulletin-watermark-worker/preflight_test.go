package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/google/uuid"
)

type fakePreflightDB struct {
	pingErr  error
	missing  int
	queryErr error
	schema   string
	asked    []tablePrivilege
}

func (f *fakePreflightDB) PingContext(context.Context) error { return f.pingErr }
func (f *fakePreflightDB) MissingPrivileges(_ context.Context, schema string, privileges []tablePrivilege) (int, error) {
	f.schema, f.asked = schema, privileges
	return f.missing, f.queryErr
}

type fakePreflightQueue struct{ err error }

func (f fakePreflightQueue) Peek(context.Context) error { return f.err }

type fakePreflightAssets struct {
	err error
	ids []string
}

func (f *fakePreflightAssets) Get(_ context.Context, id string) (assetclient.Asset, error) {
	f.ids = append(f.ids, id)
	return assetclient.Asset{}, f.err
}

func capturePreflight(t *testing.T, deps preflightDeps) (bool, map[string]any) {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))
	defer slog.SetDefault(previous)
	ok := runPreflight(context.Background(), deps)
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("preflight must log exactly one line, got %d", len(lines))
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatal(err)
	}
	if record["msg"] != preflightLog {
		t.Fatalf("unexpected message %v", record["msg"])
	}
	return ok, record
}

func passingDeps() preflightDeps {
	return preflightDeps{
		db:       &fakePreflightDB{},
		queue:    fakePreflightQueue{},
		assets:   &fakePreflightAssets{err: assetclient.ErrNotFound},
		randomID: func() (string, error) { return "0123456789abcdef0123456789abcdef", nil },
	}
}

func TestPreflightPassesOnlyWhenEveryCheckPasses(t *testing.T) {
	ok, record := capturePreflight(t, passingDeps())
	if !ok || record["ok"] != true || record["level"] != "INFO" {
		t.Fatalf("passing preflight: %v", record)
	}
	for _, name := range []string{"config", "database", "privileges", "queue", "asset"} {
		if record[name] != classOK {
			t.Fatalf("%s = %v", name, record[name])
		}
	}
}

func TestPreflightClassifiesEachFailure(t *testing.T) {
	denied := &azcore.ResponseError{StatusCode: http.StatusForbidden}
	cases := map[string]struct {
		mutate func(*preflightDeps)
		check  string
		class  string
	}{
		"invalid config skips remote checks": {func(d *preflightDeps) { d.configErr = errors.New("DATABASE_URL is required") }, "config", "invalid"},
		"database unreachable":               {func(d *preflightDeps) { d.db = &fakePreflightDB{pingErr: errors.New("dial")} }, "database", "unreachable"},
		"database not configured":            {func(d *preflightDeps) { d.db = nil }, "database", "unavailable"},
		"missing table privilege":            {func(d *preflightDeps) { d.db = &fakePreflightDB{missing: 1} }, "privileges", "missing"},
		"privilege query failure":            {func(d *preflightDeps) { d.db = &fakePreflightDB{queryErr: errors.New("relation")} }, "privileges", "query_failed"},
		"queue denied":                       {func(d *preflightDeps) { d.queue = fakePreflightQueue{err: denied} }, "queue", "denied"},
		"queue missing":                      {func(d *preflightDeps) { d.queue = fakePreflightQueue{err: &azcore.ResponseError{StatusCode: 404}} }, "queue", "not_found"},
		"queue transport":                    {func(d *preflightDeps) { d.queue = fakePreflightQueue{err: errors.New("dial")} }, "queue", "unavailable"},
		"queue client missing":               {func(d *preflightDeps) { d.queue = nil }, "queue", "unavailable"},
		"asset unauthenticated":              {func(d *preflightDeps) { d.assets = &fakePreflightAssets{err: errors.New("asset api status 401: denied")} }, "asset", "denied"},
		"asset forbidden caller":             {func(d *preflightDeps) { d.assets = &fakePreflightAssets{err: errors.New("asset api status 403: {}")} }, "asset", "denied"},
		"asset forbidden sentinel":           {func(d *preflightDeps) { d.assets = &fakePreflightAssets{err: assetclient.ErrForbidden} }, "asset", "denied"},
		"asset unavailable":                  {func(d *preflightDeps) { d.assets = &fakePreflightAssets{err: fmt.Errorf("%w: 502", assetclient.ErrUnavailable)} }, "asset", "unavailable"},
		"asset random id found":              {func(d *preflightDeps) { d.assets = &fakePreflightAssets{} }, "asset", "unexpected_found"},
		"asset other status":                 {func(d *preflightDeps) { d.assets = &fakePreflightAssets{err: errors.New("asset api status 400: bad")} }, "asset", "unexpected_status"},
		"asset id generator failure":         {func(d *preflightDeps) { d.randomID = func() (string, error) { return "", errors.New("entropy") } }, "asset", "unavailable"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			deps := passingDeps()
			tc.mutate(&deps)
			ok, record := capturePreflight(t, deps)
			if ok || record["ok"] != false || record["level"] != "ERROR" {
				t.Fatalf("failure accepted: %v", record)
			}
			if record[tc.check] != tc.class {
				t.Fatalf("%s = %v, want %s", tc.check, record[tc.check], tc.class)
			}
			if tc.check == "config" {
				for _, name := range []string{"database", "privileges", "queue", "asset"} {
					if record[name] != classSkipped {
						t.Fatalf("%s ran after invalid config", name)
					}
				}
			}
		})
	}
}

func TestPreflightLogsNoIdentifiersOrErrorText(t *testing.T) {
	deps := passingDeps()
	assets := &fakePreflightAssets{err: errors.New("asset api status 403: secret-caller-detail")}
	deps.assets = assets
	deps.randomID = randomAssetID
	deps.db = &fakePreflightDB{pingErr: errors.New("postgres://user:password@host/db")}
	_, record := capturePreflight(t, deps)
	encoded, _ := json.Marshal(record)
	for _, leaked := range []string{"secret-caller-detail", "password", assets.ids[0]} {
		if strings.Contains(string(encoded), leaked) {
			t.Fatalf("preflight log leaked %q", leaked)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(assets.ids[0]) {
		t.Fatalf("probe id is not 32 random hex characters")
	}
}

func TestPreflightAsksForExactWorkerPrivileges(t *testing.T) {
	deps := passingDeps()
	db := &fakePreflightDB{}
	deps.db = db
	capturePreflight(t, deps)
	if db.schema != "hhc_web" || len(db.asked) != len(workerPrivileges) {
		t.Fatalf("privilege set: %s %v", db.schema, db.asked)
	}
	want := map[string]string{
		"hhc_web.bulletin_download_job":            "SELECT,UPDATE",
		"hhc_web.bulletin_watermark_receipt":       "SELECT,INSERT,UPDATE",
		"hhc_web.bulletin_watermark_subject":       "SELECT",
		"hhc_web.bulletin_watermark_investigation": "SELECT,UPDATE",
	}
	got := map[string][]string{}
	for _, p := range db.asked {
		got[p.table] = append(got[p.table], p.privilege)
	}
	for table, privileges := range want {
		if strings.Join(got[table], ",") != privileges {
			t.Fatalf("%s: %v", table, got[table])
		}
	}
	if len(got) != len(want) {
		t.Fatalf("unexpected tables: %v", got)
	}
}

// The SQL checker against a disposable database: a role with exactly the
// granted privileges passes, and removing one is detected. FOR SHARE needs
// UPDATE, which is why the receipt table requires it.
func TestSQLPreflightDatabaseCountsMissingPrivileges(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	schema, role := "preflight_"+suffix, "preflight_"+suffix
	setup := []string{
		`CREATE SCHEMA ` + schema,
		`CREATE SCHEMA ` + schema + `_closed`,
		`CREATE TABLE ` + schema + `.jobs(id int)`,
		`CREATE TABLE ` + schema + `.receipts(id int)`,
		`CREATE ROLE ` + role,
		`GRANT USAGE ON SCHEMA ` + schema + ` TO ` + role,
		`GRANT SELECT, UPDATE ON ` + schema + `.jobs TO ` + role,
		`GRANT SELECT, INSERT ON ` + schema + `.receipts TO ` + role,
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `RESET ROLE`)
		_, _ = db.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		_, _ = db.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+`_closed CASCADE`)
		_, _ = db.ExecContext(ctx, `DROP ROLE IF EXISTS `+role)
	})
	for _, statement := range setup {
		if _, err = db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.ExecContext(ctx, `SET ROLE `+role); err != nil {
		t.Fatal(err)
	}
	checker := sqlPreflightDatabase{db: db}
	granted := []tablePrivilege{{schema + ".jobs", "SELECT"}, {schema + ".jobs", "UPDATE"}, {schema + ".receipts", "SELECT"}, {schema + ".receipts", "INSERT"}}
	if missing, err := checker.MissingPrivileges(ctx, schema, granted); err != nil || missing != 0 {
		t.Fatalf("granted privileges: %d %v", missing, err)
	}
	if missing, err := checker.MissingPrivileges(ctx, schema, append(granted, tablePrivilege{schema + ".receipts", "UPDATE"})); err != nil || missing != 1 {
		t.Fatalf("missing receipt UPDATE: %d %v", missing, err)
	}
	if _, err := db.ExecContext(ctx, `SELECT id FROM `+schema+`.receipts FOR SHARE`); err == nil {
		t.Fatal("FOR SHARE succeeded without UPDATE; the receipt UPDATE requirement is stale")
	}
	if missing, err := checker.MissingPrivileges(ctx, schema+"_closed", nil); err != nil || missing != 1 {
		t.Fatalf("missing schema usage: %d %v", missing, err)
	}
	if _, err := checker.MissingPrivileges(ctx, schema, []tablePrivilege{{schema + ".absent", "SELECT"}}); err == nil {
		t.Fatal("unknown table did not fail the query")
	}
}
