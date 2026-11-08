package migrations

import (
	"context"
	"errors"
	"github.com/DATA-DOG/go-sqlmock"
	"testing"
	"time"
)

func TestBulletinExtractionIndexIsConcurrentAndRetryable(t *testing.T) {
	for _, kind := range []string{"missing", "valid", "invalid", "mismatch", "build-failure", "receipt-failure"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			mock.ExpectQuery("SHOW lock_timeout").WillReturnRows(sqlmock.NewRows([]string{"lock_timeout"}).AddRow("0"))
			mock.ExpectExec("SELECT set_config").WillReturnResult(sqlmock.NewResult(0, 1))
			state := sqlmock.NewRows([]string{"valid", "expected"})
			if kind != "missing" && kind != "build-failure" {
				state.AddRow(kind != "invalid", kind != "mismatch")
			}
			mock.ExpectQuery("SELECT i.indisvalid").WillReturnRows(state)
			switch kind {
			case "invalid":
				mock.ExpectExec("DROP INDEX CONCURRENTLY").WillReturnResult(sqlmock.NewResult(0, 0))
				fallthrough
			case "missing", "build-failure":
				build := mock.ExpectExec("CREATE INDEX CONCURRENTLY")
				if kind == "build-failure" {
					build.WillReturnError(context.DeadlineExceeded)
				} else {
					build.WillReturnResult(sqlmock.NewResult(0, 0))
					mock.ExpectQuery("SELECT i.indisvalid").WillReturnRows(sqlmock.NewRows([]string{"valid", "expected"}).AddRow(true, true))
				}
			}
			if kind != "mismatch" && kind != "build-failure" {
				receipt := mock.ExpectExec("INSERT INTO public.schema_migrations").WithArgs(bulletinExtractionIndexMigration)
				if kind == "receipt-failure" {
					receipt.WillReturnError(errors.New("receipt lost"))
				} else {
					receipt.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			mock.ExpectExec("SELECT set_config").WithArgs("0").WillReturnResult(sqlmock.NewResult(0, 1))
			body, err := files.ReadFile(bulletinExtractionIndexMigration)
			if err != nil {
				t.Fatal(err)
			}
			err = runBulletinExtractionIndex(t.Context(), conn, string(body))
			wantError := kind == "mismatch" || kind == "build-failure" || kind == "receipt-failure"
			if (err != nil) != wantError {
				t.Fatalf("error=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBulletinExtractionConcurrentIndexDoesNotBlockWriters(t *testing.T) {
	db := dsrContractDB(t)
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	dsrContractExec(t, db, `DELETE FROM public.schema_migrations WHERE version=$1`, bulletinExtractionIndexMigration)
	dsrContractExec(t, db, `DROP INDEX hhc_web.online_bulletin_extraction_document_created_idx`)
	first, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback()
	if _, err = first.ExecContext(ctx, `LOCK TABLE hhc_web.online_bulletin_extraction_job IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { stop(); conn.Close() }()
	body, err := files.ReadFile(bulletinExtractionIndexMigration)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runBulletinExtractionIndex(ctx, conn, string(body)) }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var building bool
		if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE relation='hhc_web.online_bulletin_extraction_job'::regclass AND mode='ShareUpdateExclusiveLock' AND granted)`).Scan(&building); err != nil {
			t.Fatal(err)
		}
		if building {
			break
		}
		select {
		case err := <-done:
			t.Fatal("index did not wait for the active writer", err)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err = writer.ExecContext(ctx, `SET LOCAL lock_timeout='500ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err = writer.ExecContext(ctx, `LOCK TABLE hhc_web.online_bulletin_extraction_job IN ROW EXCLUSIVE MODE`); err != nil {
		t.Fatal("concurrent build blocked another writer", err)
	}
	if err = writer.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = first.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = Run(ctx, db); err != nil {
		t.Fatal("migration receipt was not retryable", err)
	}
}
