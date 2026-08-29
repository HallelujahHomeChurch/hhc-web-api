package main

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/homev2migration"
)

type fakeOperation struct {
	report                       homev2migration.Report
	applySourceSHA, applyPlanSHA string
}

func (f *fakeOperation) Plan(context.Context) (homev2migration.Report, error) { return f.report, nil }
func (f *fakeOperation) Apply(_ context.Context, sourceSHA, planSHA, _ string) (homev2migration.Report, error) {
	f.applySourceSHA, f.applyPlanSHA = sourceSHA, planSHA
	f.report.Mode = "apply"
	return f.report, nil
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
