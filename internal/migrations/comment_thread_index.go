package migrations

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

const commentThreadMigration = "sql/105_recording_comment_threads.sql"

// This unmerged additive index needs PostgreSQL's non-transactional build path.
func runCommentThreadIndex(ctx context.Context, conn *sql.Conn, contents string) (err error) {
	buildCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var previous string
	if err = conn.QueryRowContext(buildCtx, `SHOW lock_timeout`).Scan(&previous); err != nil {
		return err
	}
	if _, err = conn.ExecContext(buildCtx, `SELECT set_config('lock_timeout','1s',false)`); err != nil {
		return err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer stop()
		if _, resetErr := conn.ExecContext(cleanup, `SELECT set_config('lock_timeout',$1,false)`, previous); resetErr != nil {
			err = errors.Join(err, fmt.Errorf("restore index lock timeout: %w", resetErr))
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	var valid, expected bool
	inspect := func() error {
		return conn.QueryRowContext(buildCtx, commentThreadIndexStateSQL).Scan(&valid, &expected)
	}
	err = inspect()
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && !expected {
		return errors.New("comment thread index definition mismatch")
	}
	if err == nil && !valid {
		// A canceled concurrent build may leave our exact index invalid; retry safely.
		if _, err = conn.ExecContext(buildCtx, `DROP INDEX CONCURRENTLY hhc_web.member_recording_comment_direct_children_idx`); err != nil {
			return err
		}
	}
	if !valid {
		if _, err = conn.ExecContext(buildCtx, contents); err != nil {
			return fmt.Errorf("build comment thread index: %w", err)
		}
		if err = inspect(); err != nil {
			return err
		}
		if !valid || !expected {
			return errors.New("comment thread index validation failed")
		}
	}
	_, err = conn.ExecContext(buildCtx, `INSERT INTO public.schema_migrations(version) VALUES($1) ON CONFLICT DO NOTHING`, commentThreadMigration)
	return err
}

const commentThreadIndexStateSQL = `SELECT i.indisvalid,
 i.indrelid='hhc_web.member_recording_comment'::regclass AND NOT i.indisunique AND i.indnatts=4 AND i.indnkeyatts=4
 AND pg_get_indexdef(i.indexrelid,1,true)='recording_id'
 AND pg_get_indexdef(i.indexrelid,2,true)='COALESCE(reply_to_id, parent_id)'
 AND pg_get_indexdef(i.indexrelid,3,true)='created_at' AND pg_get_indexdef(i.indexrelid,4,true)='id'
 AND pg_get_expr(i.indpred,i.indrelid)='(parent_id IS NOT NULL)'
 AND c.relam=(SELECT oid FROM pg_am WHERE amname='btree')
 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE i.indexrelid=to_regclass('hhc_web.member_recording_comment_direct_children_idx')`
