package postgres

import (
	"context"
	"database/sql"
	"testing"
)

// Seed the persisted fence left by retired restriction actions; ordinary readers must still honor it.
func seedWatermarkRestriction(ctx context.Context, db *sql.DB, user string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockWatermarkSubject(ctx, tx, user); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES($1,false) ON CONFLICT(subject_hash) DO NOTHING`, watermarkSubjectHash(user)); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_watermark_investigation SET status='failed',error_code='processing_restricted',evidence=NULL,lease_token=NULL,lease_until=NULL WHERE actor_id=$1 AND status IN ('queued','running')`, user); err != nil {
		return err
	}
	return tx.Commit()
}
func assertPrivateReaderRows(t *testing.T, db *sql.DB, account string, want int64) {
	t.Helper()
	var count int64
	err := db.QueryRow(`SELECT `+readerPrivateCountSQL, account).Scan(&count)
	if err != nil || count != want {
		t.Fatalf("reader rows=%d want=%d err=%v", count, want, err)
	}
}
