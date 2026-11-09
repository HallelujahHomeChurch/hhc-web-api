package migrations

import (
	"context"
	"io/fs"
	"testing"
	"time"
)

func TestMigrationContendersLeaveNoSnapshotForConcurrentIndexes(t *testing.T) {
	db := dsrContractDB(t)
	db.SetMaxOpenConns(4)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	holder, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	var holderPID int
	if err := holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockID); err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
			defer finish()
			_, _ = holder.ExecContext(cleanup, `SELECT pg_advisory_unlock($1)`, migrationLockID)
		}
	}()

	results := make(chan error, 2)
	remaining := cap(results)
	for range remaining {
		go func() { results <- Run(ctx, db) }()
	}
	defer func() {
		cancel()
		for range remaining {
			<-results
		}
	}()

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var idle, blocking int
		err := db.QueryRowContext(ctx, `
			SELECT count(*) FILTER (WHERE state='idle' AND backend_xmin IS NULL),
			       count(*) FILTER (WHERE wait_event_type='Lock' AND backend_xmin IS NOT NULL)
			FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>$1 AND pid<>pg_backend_pid()
			  AND query LIKE 'SELECT pg%advisory_lock(%'`, holderPID).Scan(&idle, &blocking)
		if err != nil {
			t.Fatal(err)
		}
		if blocking != 0 {
			t.Fatalf("%d migration contenders hold a snapshot while blocked", blocking)
		}
		if idle == cap(results) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("did not observe both migration contenders")
		case <-ticker.C:
		}
	}

	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, migrationLockID); err != nil {
		t.Fatal(err)
	}
	locked = false
	for remaining > 0 {
		err := <-results
		remaining--
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := fs.Glob(files, "sql/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM public.schema_migrations`).Scan(&applied); err != nil || applied != len(entries) {
		t.Fatalf("applied migrations = %d, want %d: %v", applied, len(entries), err)
	}
	var valid bool
	if err := db.QueryRowContext(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid='hhc_web.member_recording_comment_direct_children_idx'::regclass`).Scan(&valid); err != nil || !valid {
		t.Fatalf("concurrent index valid = %v: %v", valid, err)
	}
}
