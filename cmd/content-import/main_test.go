package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

const cliTestManifest = `{"schemaVersion":1,"seedVersion":"v1","sourceRepo":"repo","sourceCommit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","locales":["zh-Hant","zh-Hans","en","ja","ko"],"sources":[],"records":[]}
`

func TestRunCLIRejectsApplyConfirmationBeforeDatabaseUse(t *testing.T) {
	for _, args := range [][]string{
		{"--mode=apply", "--confirmation=wrong", "--expected-manifest-sha=20988cc4f36618f6751e1013949be5c729240828ffa6d3a66ea406c1b61a1c8b"},
		{"--mode=apply", "--confirmation=2026-08-28-public-content-foundation-v1", "--expected-manifest-sha=wrong"},
	} {
		var stdout, stderr bytes.Buffer
		opened := false
		deps := defaultDependencies()
		deps.getenv = func(string) string { return "postgres://unused" }
		deps.openDB = func(string, string) (*sql.DB, error) {
			opened = true
			return nil, errors.New("must not open database")
		}
		if code := runCLI(context.Background(), args, &stdout, &stderr, deps); code == 0 {
			t.Fatal("exit code = 0")
		}
		if opened {
			t.Fatal("database opened before confirmation validation")
		}
		if stdout.Len() != 0 || stderr.String() != "confirmation and expected manifest SHA must match the embedded manifest\n" {
			t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	}
}

func TestRunCLIPrintsExactlyOnePlanJSONLine(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	deps := defaultDependencies()
	deps.getenv = func(string) string { return "postgres://test" }
	deps.openDB = func(string, string) (*sql.DB, error) { return db, nil }
	var stdout, stderr bytes.Buffer

	if code := runCLI(context.Background(), []string{"--mode=plan"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("exit code=%d stderr=%q", code, stderr.String())
	}
	const want = "{\"mode\":\"plan\",\"seedVersion\":\"2026-08-28-public-content-foundation-v1\",\"manifestSHA256\":\"20988cc4f36618f6751e1013949be5c729240828ffa6d3a66ea406c1b61a1c8b\",\"inserts\":0,\"skips\":0,\"updates\":0,\"deletes\":0,\"warnings\":0,\"conflicts\":0}\n"
	if stdout.String() != want || stderr.Len() != 0 || strings.Count(stdout.String(), "\n") != 1 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunCLIApplyBindsOriginalOverrideBytesAndActor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const manifestSHA = "a5cde6e8f25aff8abb62dea6c983f54f981719e4009712302bdb2d3a0a806e48"
	mock.ExpectExec("SELECT pg_advisory_lock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("(?s)SELECT manifest_sha256.*FROM hhc_web.content_seed_run.*status='succeeded'").WithArgs("v1").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("(?s)INSERT INTO hhc_web.content_seed_run.*VALUES").WithArgs(sqlmock.AnyArg(), "v1", "repo", strings.Repeat("a", 40), manifestSHA, "content-seed:v1").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectBegin()
	mock.ExpectExec("(?s)UPDATE hhc_web.content_seed_run.*status='succeeded'").WithArgs(0, 0, 0, 0, sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec("SELECT pg_advisory_unlock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	deps := defaultDependencies()
	deps.manifest = nil
	deps.readFile = func(path string) ([]byte, error) {
		if path != "local.json" {
			t.Fatalf("manifest path = %q", path)
		}
		return []byte(cliTestManifest), nil
	}
	deps.getenv = func(string) string { return "postgres://test" }
	deps.openDB = func(string, string) (*sql.DB, error) { return db, nil }
	var stdout, stderr bytes.Buffer

	args := []string{"--mode=apply", "--manifest=local.json", "--confirmation=v1", "--expected-manifest-sha=" + manifestSHA}
	if code := runCLI(context.Background(), args, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("exit code=%d stderr=%q", code, stderr.String())
	}
	const want = "{\"mode\":\"apply\",\"seedVersion\":\"v1\",\"manifestSHA256\":\"a5cde6e8f25aff8abb62dea6c983f54f981719e4009712302bdb2d3a0a806e48\",\"inserts\":0,\"skips\":0,\"updates\":0,\"deletes\":0,\"warnings\":0,\"conflicts\":0}\n"
	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRunCLIInventoryIncludesReadOnlySnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT issue_number::text FROM hhc_web.bulletin_issue").WillReturnRows(sqlmock.NewRows([]string{"key"}))
	mock.ExpectQuery("SELECT slug FROM hhc_web.news_item").WillReturnRows(sqlmock.NewRows([]string{"key"}))
	mock.ExpectQuery("SELECT sort_order::text FROM hhc_web.history_event").WillReturnRows(sqlmock.NewRows([]string{"key"}))
	mock.ExpectQuery("SELECT youtube_video_id FROM hhc_web.video_item").WillReturnRows(sqlmock.NewRows([]string{"key"}))
	deps := defaultDependencies()
	deps.getenv = func(string) string { return "postgres://test" }
	deps.openDB = func(string, string) (*sql.DB, error) { return db, nil }
	var stdout, stderr bytes.Buffer

	if code := runCLI(context.Background(), []string{"--mode=inventory"}, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("exit code=%d stderr=%q", code, stderr.String())
	}
	for _, fragment := range []string{`"mode":"inventory"`, `"updates":0`, `"deletes":0`, `"inventory":{"bulletins":{"count":0`, `"news":{"count":0`, `"history":{"count":0`, `"videos":{"count":0`} {
		if !strings.Contains(stdout.String(), fragment) {
			t.Fatalf("stdout missing %q: %s", fragment, stdout.String())
		}
	}
	if strings.Count(stdout.String(), "\n") != 1 || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
