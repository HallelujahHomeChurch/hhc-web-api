package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/dbbudget"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/logging"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	publicIDs := flag.String("recording-public-ids", "", "plan counts or apply one batch of at most 100; does not run schema migrations")
	flag.Parse()
	if *publicIDs != "" && *publicIDs != "plan" && *publicIDs != "apply" {
		fmt.Fprintln(os.Stderr, "recording-public-ids must be plan or apply")
		os.Exit(2)
	}
	if err := logging.Init(); err != nil {
		slog.Error("invalid logging configuration", "error", err)
		os.Exit(1)
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	db, err := dbbudget.Open(databaseURL, "hhc_web_migrate", 1, 1, 1)
	if err != nil {
		slog.Error("open database", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := runMigrations
	if *publicIDs != "" {
		run = func(ctx context.Context, db *sql.DB) error {
			return runRecordingPublicIDs(ctx, db, *publicIDs == "apply")
		}
	}
	if err := run(ctx, db); err != nil {
		slog.Error("apply migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations complete")
}

func runMigrations(ctx context.Context, db *sql.DB) error {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	return migrations.Run(ctx, db)
}

func runRecordingPublicIDs(ctx context.Context, db *sql.DB, apply bool) error {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	repo := postgres.NewRecordingRepository(db)
	total, missing, err := repo.PlanRecordingPublicIDs(ctx)
	if err != nil {
		return err
	}
	slog.Info("recording public ID plan", "total", total, "missing", missing)
	if !apply {
		return nil
	}
	assigned, err := repo.BackfillRecordingPublicIDs(ctx, 100)
	if err != nil {
		return err
	}
	_, missing, err = repo.PlanRecordingPublicIDs(ctx)
	if err != nil {
		return err
	}
	slog.Info("recording public ID batch", "assigned", assigned, "missing", missing)
	return nil
}
