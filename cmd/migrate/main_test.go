package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrationPoolBoundary(t *testing.T) {
	for _, kind := range []string{"success", "failure", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var want error
			if kind == "canceled" {
				cancel()
				want = context.Canceled
			} else {
				mock.ExpectQuery("SELECT pg_try_advisory_lock").WithArgs(sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"locked"}).AddRow(true))
				create := mock.ExpectExec("CREATE TABLE IF NOT EXISTS public.schema_migrations")
				if kind == "failure" {
					want = errors.New("schema unavailable")
					create.WillReturnError(want)
				} else {
					create.WillReturnResult(sqlmock.NewResult(0, 0))
					entries, err := os.ReadDir("../../internal/migrations/sql")
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						if strings.HasSuffix(entry.Name(), ".sql") {
							mock.ExpectQuery("SELECT EXISTS").WithArgs("sql/" + entry.Name()).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
						}
					}
				}
				mock.ExpectExec("SELECT pg_advisory_unlock").WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectClose()
			err = runMigrations(ctx, db)
			if !errors.Is(err, want) {
				t.Fatalf("error=%v, want %v", err, want)
			}
			if got := db.Stats().MaxOpenConnections; got != 1 {
				t.Errorf("max open=%d, want1", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
			if err := db.Ping(); err == nil {
				t.Error("pool still open at return")
			}
		})
	}
}

func TestRecordingPublicIDPlanDoesNotWrite(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT count").WillReturnRows(sqlmock.NewRows([]string{"total", "missing"}).AddRow(7, 3))
	mock.ExpectClose()
	if err := runRecordingPublicIDs(context.Background(), db, false); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if db.Stats().MaxOpenConnections != 1 {
		t.Fatal("unbounded maintenance pool")
	}
}
