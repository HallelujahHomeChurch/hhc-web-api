package migrations

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"
)

//go:embed sql/*.sql
var files embed.FS

const (
	cleanupTimeout        = 10 * time.Second
	migrationLockID int64 = 721732019260730
)

func Run(ctx context.Context, db *sql.DB) (err error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	for {
		var acquired bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, migrationLockID).Scan(&acquired); err != nil {
			// Acquisition errors leave session lock ownership uncertain.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			return err
		}
		if acquired {
			break
		}
		// Wait outside PostgreSQL so a contender holds no snapshot needed by a concurrent index build.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if _, unlockErr := conn.ExecContext(cleanup, `SELECT pg_advisory_unlock($1)`, migrationLockID); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release migration lock: %w", unlockErr))
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	entries, err := fs.Glob(files, "sql/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)
	for _, name := range entries {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM public.schema_migrations WHERE version=$1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		contents, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		if name == commentThreadMigration {
			if err := runCommentThreadIndex(ctx, conn, string(contents)); err != nil {
				return fmt.Errorf("apply %s: %w", name, err)
			}
			continue
		}
		if name == bulletinExtractionIndexMigration {
			if err := runBulletinExtractionIndex(ctx, conn, string(contents)); err != nil {
				return fmt.Errorf("apply %s: %w", name, err)
			}
			continue
		}
		tx, err := beginMigrationTransaction(ctx, conn)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, string(contents)); err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO public.schema_migrations(version) VALUES($1)`, name)
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
