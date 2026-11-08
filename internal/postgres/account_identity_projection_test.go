package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
)

func TestUpsertWatermarkIdentitiesDoesNotRecreateRestrictedSubject(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	userID := "3cf8ea36-6aee-49a1-8a22-c9df3588b545"
	mock.ExpectBegin()
	mock.ExpectExec("SELECT pg_advisory_xact_lock").WithArgs(watermarkSubjectHash(userID)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(watermarkSubjectHash(userID)).WillReturnRows(sqlmock.NewRows([]string{"restricted"}).AddRow(true))
	mock.ExpectCommit()

	err = New(db).UpsertWatermarkIdentities(context.Background(), []string{userID}, []accountclient.Identity{{ID: userID, Email: "member@example.test"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertWatermarkIdentitiesSerializesWithErasure(t *testing.T) {
	db := accessDB(t)
	r := New(db)
	userID := "3cf8ea36-6aee-49a1-8a22-c9df3588b546"
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.account_identity_projection WHERE account_user_id=$1`, userID)
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1`, watermarkSubjectHash(userID))
	})
	if _, err := db.Exec(`INSERT INTO hhc_web.account_identity_projection(account_user_id,display_name,email,available,refreshed_at) VALUES($1,'Before cleanup','before@example.test',true,now())`, userID); err != nil {
		t.Fatal(err)
	}

	cleanup, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup.Rollback()
	if err = lockWatermarkSubject(context.Background(), cleanup, userID); err != nil {
		t.Fatal(err)
	}
	if _, err = cleanup.Exec(`INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES($1,true)`, watermarkSubjectHash(userID)); err != nil {
		t.Fatal(err)
	}
	if _, err = cleanup.Exec(`DELETE FROM hhc_web.account_identity_projection WHERE account_user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}

	blocked, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = r.UpsertWatermarkIdentities(blocked, []string{userID}, []accountclient.Identity{{ID: userID, DisplayName: "Recreated", Email: "recreated@example.test"}}, time.Now())
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("upsert did not wait for the erasure lock: %v", err)
	}
	if err = cleanup.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = r.UpsertWatermarkIdentities(context.Background(), []string{userID}, []accountclient.Identity{{ID: userID, DisplayName: "Recreated", Email: "recreated@example.test"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.account_identity_projection WHERE account_user_id=$1`, userID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("projection recreated after erasure: count=%d err=%v", count, err)
	}
}
