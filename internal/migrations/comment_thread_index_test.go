package migrations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCommentThreadIndexIsConcurrentAndRetryable(t *testing.T) {
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
				receipt := mock.ExpectExec("INSERT INTO public.schema_migrations").WithArgs(commentThreadMigration)
				if kind == "receipt-failure" {
					receipt.WillReturnError(errors.New("receipt lost"))
				} else {
					receipt.WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			mock.ExpectExec("SELECT set_config").WithArgs("0").WillReturnResult(sqlmock.NewResult(0, 1))
			body, err := files.ReadFile(commentThreadMigration)
			if err != nil {
				t.Fatal(err)
			}
			err = runCommentThreadIndex(t.Context(), conn, string(body))
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

func TestCommentThreadConcurrentBuildAllowsWritersAndReusesValidIndex(t *testing.T) {
	db := dsrContractDB(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(4)
	dsrContractExec(t, db, `DELETE FROM public.schema_migrations WHERE version=$1`, commentThreadMigration)
	dsrContractExec(t, db, `DROP INDEX hhc_web.member_recording_comment_direct_children_idx`)
	dsrContractExec(t, db, `INSERT INTO hhc_web.member_recording(id,create_title,draft_expires_at,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES('00000000-0000-4000-8000-000000000001','Synthetic index test',now()+interval '1 day','index-test','test','test',now(),now())`)
	first, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback()
	insert := `INSERT INTO hhc_web.member_recording_comment(id,recording_id,body,created_at) VALUES($1,'00000000-0000-4000-8000-000000000001','Synthetic comment',now())`
	if _, err = first.ExecContext(ctx, insert, "10000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var pid int
	if err = conn.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	body, err := files.ReadFile(commentThreadMigration)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runCommentThreadIndex(ctx, conn, string(body)); close(done) }()
	defer func() {
		cancel()
		_ = first.Rollback()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var acquired bool
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE pid=$1 AND relation='hhc_web.member_recording_comment'::regclass AND mode IN ('ShareLock','ShareUpdateExclusiveLock'))`, pid).Scan(&acquired)
		if err != nil {
			t.Fatal(err)
		}
		if acquired {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("index build ended before writer overlap: %v", err)
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	writeCtx, stop := context.WithTimeout(ctx, 250*time.Millisecond)
	_, err = db.ExecContext(writeCtx, insert, "10000000-0000-4000-8000-000000000002")
	stop()
	if err != nil {
		t.Fatalf("index blocked ordinary comment write: %v", err)
	}
	if err = first.Rollback(); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var oid, before string
	if err = conn.QueryRowContext(ctx, `SELECT 'hhc_web.member_recording_comment_direct_children_idx'::regclass::oid::text`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = runCommentThreadIndex(ctx, conn, string(body)); err != nil {
		t.Fatal(err)
	}
	if err = conn.QueryRowContext(ctx, `SELECT 'hhc_web.member_recording_comment_direct_children_idx'::regclass::oid::text`).Scan(&oid); err != nil || oid != before {
		t.Fatalf("valid index rebuilt: %q %q %v", before, oid, err)
	}
}
