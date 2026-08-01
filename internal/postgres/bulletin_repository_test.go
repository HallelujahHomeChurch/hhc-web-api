package postgres

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestListIssuesLoadsPageVersionsWithOneBatchQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM hhc_web.bulletin_issue")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("SELECT id::text,issue_date::text,status,version,created_by,updated_by,published_at,created_at,updated_at FROM hhc_web.bulletin_issue").
		WithArgs(20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_date", "status", "version", "created_by", "updated_by", "published_at", "created_at", "updated_at"}).
			AddRow("00000000-0000-0000-0000-000000000001", "2026-08-02", "draft", 2, "user-1", "user-1", nil, now, now).
			AddRow("00000000-0000-0000-0000-000000000002", "2026-07-26", "draft", 2, "user-1", "user-1", nil, now, now))
	mock.ExpectQuery("FROM hhc_web.bulletin_version v").
		WithArgs("00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002").
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "locale", "title", "pdf_asset_id", "pdf_file_name", "public_grant_id", "status", "workflow_status", "workflow_error", "version", "published_at", "created_at", "updated_at"}).
			AddRow("10000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000001", "zh-Hant", "本週週報", "asset-1", "weekly.pdf", "", "draft", "", "", 1, nil, now, now).
			AddRow("10000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000002", "en", "Weekly", "asset-2", "weekly-en.pdf", "", "draft", "", "", 1, nil, now, now))

	page, err := New(db).ListIssues(context.Background(), 1, 20, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || len(page.Items[0].Versions) != 1 || page.Items[0].Versions[0].Title != "本週週報" || len(page.Items[1].Versions) != 1 || page.Items[1].Versions[0].Locale != "en" {
		t.Fatalf("page=%#v", page)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
