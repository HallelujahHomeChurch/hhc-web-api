package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/homev2migration"
)

type fakeOperation struct {
	err                          error
	report                       homev2migration.Report
	applySourceSHA, applyPlanSHA string
}

func (f *fakeOperation) Plan(context.Context) (homev2migration.Report, error) { return f.report, f.err }
func (f *fakeOperation) Apply(_ context.Context, sourceSHA, planSHA, _ string) (homev2migration.Report, error) {
	f.applySourceSHA, f.applyPlanSHA = sourceSHA, planSHA
	f.report.Mode = "apply"
	return f.report, f.err
}

func TestRunEmitsExactlyOnePlanReport(t *testing.T) {
	operation := &fakeOperation{report: homev2migration.Report{Mode: "plan", Updates: 1, SourceSHA256: strings.Repeat("a", 64), PlanSHA256: strings.Repeat("b", 64)}}
	var output bytes.Buffer
	err := run(context.Background(), []string{"--mode=plan"}, &output, testDependencies(operation))
	if err != nil || strings.Count(strings.TrimSpace(output.String()), "\n") != 0 || !strings.Contains(output.String(), `"updates":1`) {
		t.Fatalf("output=%q err=%v", output.String(), err)
	}
}

func TestRunApplyRequiresAndForwardsReviewedHashes(t *testing.T) {
	operation := &fakeOperation{report: homev2migration.Report{Updates: 1}}
	if err := run(context.Background(), []string{"--mode=apply"}, &bytes.Buffer{}, testDependencies(operation)); err == nil {
		t.Fatal("apply without evidence succeeded")
	}
	sourceSHA, planSHA := strings.Repeat("a", 64), strings.Repeat("b", 64)
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--mode=apply", "--expected-source-sha=" + sourceSHA, "--expected-plan-sha=" + planSHA}, &output, testDependencies(operation)); err != nil {
		t.Fatal(err)
	}
	if operation.applySourceSHA != sourceSHA || operation.applyPlanSHA != planSHA || !strings.Contains(output.String(), `"applied":false`) {
		t.Fatalf("operation=%#v", operation)
	}
}

func testDependencies(op operation) dependencies {
	return dependencies{
		getenv:     func(string) string { return "postgres://test" },
		openDB:     func(string, string) (*sql.DB, error) { return sql.Open("pgx", "postgres://test") },
		newService: func(*sql.DB) operation { return op },
	}
}

func TestHomeCommandBoundsAndClosesJobPool(t *testing.T) {
	for _, mode := range []string{"plan", "apply"} {
		for _, failed := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/success", true: "/failure"}[failed], func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				mock.ExpectClose()
				op := &fakeOperation{}
				if failed {
					op.err = errors.New("operation failed")
				}
				deps := testDependencies(op)
				deps.openDB = func(string, string) (*sql.DB, error) { return db, nil }
				deps.newService = func(got *sql.DB) operation {
					if got.Stats().MaxOpenConnections != 1 {
						t.Errorf("max open=%d, want1", got.Stats().MaxOpenConnections)
					}
					return op
				}
				args := []string{"--mode=" + mode}
				if mode == "apply" {
					args = append(args, "--expected-source-sha="+strings.Repeat("a", 64), "--expected-plan-sha="+strings.Repeat("b", 64))
				}
				if err := run(context.Background(), args, &bytes.Buffer{}, deps); !errors.Is(err, op.err) {
					t.Fatalf("error=%v, want%v", err, op.err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
				if err := db.Ping(); err == nil {
					t.Fatal("pool not closed")
				}
			})
		}
	}
}

func TestHomeCommandClosesActualServiceTransactionOnQueryFailure(t *testing.T) {
	for _, mode := range []string{"plan", "apply"} {
		t.Run(mode, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectBegin()
			if mode == "apply" {
				mock.ExpectExec("SELECT pg_advisory_xact_lock").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			want := errors.New("snapshot unavailable")
			mock.ExpectQuery("SELECT").WillReturnError(want)
			mock.ExpectRollback()
			mock.ExpectClose()
			deps := defaultDependencies()
			deps.getenv = func(string) string { return "unused" }
			deps.openDB = func(string, string) (*sql.DB, error) { return db, nil }
			args := []string{"--mode=" + mode}
			if mode == "apply" {
				args = append(args, "--expected-source-sha="+strings.Repeat("a", 64), "--expected-plan-sha="+strings.Repeat("b", 64))
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := run(ctx, args, &bytes.Buffer{}, deps); !errors.Is(err, want) {
				t.Fatalf("error=%v, want%v", err, want)
			}
			if db.Stats().MaxOpenConnections != 1 || db.Stats().InUse != 0 {
				t.Fatalf("pool stats=%+v", db.Stats())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
