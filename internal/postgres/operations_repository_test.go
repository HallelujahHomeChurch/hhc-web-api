package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operations"
)

func TestUnionWindowsMergesOverlapsAndAdjacentIntervals(t *testing.T) {
	base := time.Date(2026, 9, 1, 1, 0, 0, 0, time.UTC)
	got := unionWindows([]operations.MediaSyncWindow{
		{StartsAt: base.Add(4 * time.Hour), EndsAt: base.Add(5 * time.Hour)},
		{StartsAt: base, EndsAt: base.Add(2 * time.Hour)},
		{StartsAt: base.Add(time.Hour), EndsAt: base.Add(3 * time.Hour)},
		{StartsAt: base.Add(3 * time.Hour), EndsAt: base.Add(4 * time.Hour)},
	})
	if len(got) != 1 || !got[0].StartsAt.Equal(base) || !got[0].EndsAt.Equal(base.Add(5*time.Hour)) {
		t.Fatalf("windows=%+v", got)
	}
}

func TestCreateChurchUnitCommitsRecordAndAuditAtomically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO hhc_web.church_unit")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text,stable_key FROM hhc_web.church_unit WHERE idempotency_key=$1")).WithArgs("unit-taipei").WillReturnRows(sqlmock.NewRows([]string{"id", "stable_key"}).AddRow("11111111-1111-4111-8111-111111111111", "taipei"))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id::text,stable_key,name,description,parent_id::text,status,version FROM hhc_web.church_unit WHERE id=$1")).WillReturnRows(sqlmock.NewRows([]string{"id", "stable_key", "name", "description", "parent_id", "status", "version"}).AddRow("11111111-1111-4111-8111-111111111111", "taipei", "Taipei", "", nil, "active", 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO hhc_web.operations_audit")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	_, err = NewOperationsRepository(db).CreateChurchUnit(context.Background(), operations.ChurchUnitInput{Key: "taipei", Name: "Taipei"}, "admin", "request", "unit-taipei", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
