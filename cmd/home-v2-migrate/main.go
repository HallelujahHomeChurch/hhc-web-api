package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/homev2migration"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type operation interface {
	Plan(context.Context) (homev2migration.Report, error)
	Apply(context.Context, string, string, string) (homev2migration.Report, error)
}

type dependencies struct {
	getenv     func(string) string
	openDB     func(string, string) (*sql.DB, error)
	newService func(*sql.DB) operation
}

func main() {
	os.Exit(runCLI(context.Background(), os.Args[1:], os.Stdout, os.Stderr, defaultDependencies()))
}

func defaultDependencies() dependencies {
	return dependencies{getenv: os.Getenv, openDB: sql.Open, newService: func(db *sql.DB) operation { return homev2migration.New(db) }}
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) int {
	if err := run(ctx, args, stdout, deps); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, stdout io.Writer, deps dependencies) error {
	flags := flag.NewFlagSet("home-v2-migrate", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	mode := flags.String("mode", "plan", "plan or apply")
	expectedSourceSHA := flags.String("expected-source-sha", "", "reviewed source evidence SHA-256")
	expectedPlanSHA := flags.String("expected-plan-sha", "", "reviewed plan SHA-256")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *mode != "plan" && *mode != "apply" {
		return fmt.Errorf("unsupported mode %q", *mode)
	}
	if *mode == "apply" && (len(*expectedSourceSHA) != 64 || len(*expectedPlanSHA) != 64) {
		return errors.New("apply requires reviewed source and plan SHA-256 values")
	}
	databaseURL := strings.TrimSpace(deps.getenv("DATABASE_URL"))
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	db, err := deps.openDB("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	service := deps.newService(db)
	var report homev2migration.Report
	if *mode == "plan" {
		report, err = service.Plan(ctx)
	} else {
		report, err = service.Apply(ctx, *expectedSourceSHA, *expectedPlanSHA, "home-v2-migration")
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(report)
}
