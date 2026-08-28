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

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/contentseed"
	contentmanifest "github.com/HallelujahHomeChurch/hhc-web-api/seeds/content"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type dependencies struct {
	manifest []byte
	getenv   func(string) string
	readFile func(string) ([]byte, error)
	openDB   func(string, string) (*sql.DB, error)
}

type outputReport struct {
	contentseed.Report
	Inventory *contentseed.InventoryReport `json:"inventory,omitempty"`
}

func main() {
	os.Exit(runCLI(context.Background(), os.Args[1:], os.Stdout, os.Stderr, defaultDependencies()))
}

func defaultDependencies() dependencies {
	return dependencies{manifest: contentmanifest.Manifest, getenv: os.Getenv, readFile: os.ReadFile, openDB: sql.Open}
}

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) int {
	if err := run(ctx, args, stdout, deps); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func run(ctx context.Context, args []string, stdout io.Writer, deps dependencies) error {
	flags := flag.NewFlagSet("content-import", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	mode := flags.String("mode", "inventory", "inventory, plan, or apply")
	manifestPath := flags.String("manifest", "", "local/test manifest override")
	confirmation := flags.String("confirmation", "", "seed version confirmation")
	expectedManifestSHA := flags.String("expected-manifest-sha", "", "manifest SHA-256 confirmation")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if *mode != "inventory" && *mode != "plan" && *mode != "apply" {
		return fmt.Errorf("unsupported mode %q", *mode)
	}
	payload := deps.manifest
	if *manifestPath != "" {
		var err error
		payload, err = deps.readFile(*manifestPath)
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}
	}
	manifest, manifestSHA, err := contentseed.Load(payload)
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	if *mode == "apply" && (*confirmation != manifest.SeedVersion || *expectedManifestSHA != manifestSHA) {
		return errors.New("confirmation and expected manifest SHA must match the embedded manifest")
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

	report := outputReport{Report: contentseed.Report{Mode: *mode, SeedVersion: manifest.SeedVersion, ManifestSHA256: manifestSHA}}
	switch *mode {
	case "inventory":
		inventory, err := contentseed.Inventory(ctx, db)
		if err != nil {
			return err
		}
		report.Inventory = &inventory
	case "plan":
		planned, err := contentseed.Plan(ctx, db, manifest)
		if err != nil {
			return err
		}
		report.Inserts = planned.InsertCount
		report.Skips = planned.SkipCount
		report.Conflicts = planned.ConflictCount
	case "apply":
		applied, err := contentseed.Apply(ctx, db, manifest, manifestSHA, "content-seed:"+manifest.SeedVersion)
		if err != nil {
			return err
		}
		report.Report = applied
	}
	if report.Warnings != 0 || report.Conflicts != 0 {
		return fmt.Errorf("%s has %d warnings and %d conflicts", report.Mode, report.Warnings, report.Conflicts)
	}
	return json.NewEncoder(stdout).Encode(report)
}
