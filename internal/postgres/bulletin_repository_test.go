package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

func TestGetPublicByNumberUsesExactProjectionJoin(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	payload := `{"issueNumber":1732,"issueDate":"2026-08-02","locale":"zh-Hant","title":"週報","subtitle":"本週消息","downloadUrl":"/api/assets/public/asset-1","downloadFileName":"1732-週報.pdf","publishedAt":"2026-08-01T12:00:00Z","version":3}`
	mock.ExpectQuery(`SELECT p\.payload_json\s+FROM hhc_web\.bulletin_issue i\s+JOIN hhc_web\.public_projection p\s+ON p\.resource_type='bulletin_issue'\s+AND p\.resource_id=i\.id\s+AND p\.locale=\$2\s+WHERE i\.issue_number=\$1`).
		WithArgs(1732, "zh-Hant").
		WillReturnRows(sqlmock.NewRows([]string{"payload_json"}).AddRow([]byte(payload)))

	value, err := New(db).GetPublicByNumber(context.Background(), 1732, "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if value.IssueNumber == nil || *value.IssueNumber != 1732 || value.Locale != "zh-Hant" || value.Subtitle != "本週消息" {
		t.Fatalf("value=%#v", value)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetPublicByNumberMapsMissingAndMalformedProjection(t *testing.T) {
	for _, test := range []struct {
		name string
		rows *sqlmock.Rows
	}{
		{name: "missing", rows: sqlmock.NewRows([]string{"payload_json"})},
		{name: "malformed", rows: sqlmock.NewRows([]string{"payload_json"}).AddRow([]byte(`{"issueNumber":`))},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery("FROM hhc_web.bulletin_issue i").WithArgs(1732, "en").WillReturnRows(test.rows)
			_, err = New(db).GetPublicByNumber(context.Background(), 1732, "en")
			if test.name == "missing" && !errors.Is(err, bulletins.ErrNotFound) {
				t.Fatalf("error=%v", err)
			}
			var syntaxError *json.SyntaxError
			if test.name == "malformed" && !errors.As(err, &syntaxError) {
				t.Fatalf("error=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestListIssuesLoadsPageVersionsWithOneBatchQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(*) FROM hhc_web.bulletin_issue i")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("SELECT i.id::text,i.issue_number,i.issue_date::text").
		WithArgs(20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_number", "issue_date", "status", "notification_status", "notification_queued_at", "notification_error_code", "version", "created_by", "updated_by", "published_at", "created_at", "updated_at"}).
			AddRow("00000000-0000-0000-0000-000000000001", 1732, "2026-08-02", "draft", "not_requested", nil, "", 2, "user-1", "user-1", nil, now, now).
			AddRow("00000000-0000-0000-0000-000000000002", 1731, "2026-07-26", "draft", "not_requested", nil, "", 2, "user-1", "user-1", nil, now, now))
	mock.ExpectQuery("FROM hhc_web.bulletin_version v").
		WithArgs("00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002").
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "locale", "title", "subtitle", "pdf_asset_id", "pdf_file_name", "public_grant_id", "status", "workflow_status", "workflow_error", "version", "published_at", "created_at", "updated_at"}).
			AddRow("10000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000001", "zh-Hant", "本週週報", "副標", "asset-1", "weekly.pdf", "", "draft", "", "", 1, nil, now, now).
			AddRow("10000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000002", "en", "Weekly", "", "asset-2", "weekly-en.pdf", "", "draft", "", "", 1, nil, now, now))

	page, err := New(db).ListIssues(context.Background(), 1, 20, "", "")
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

func TestBuildBulletinNotificationPreservesLegacyPayloadUntilFluentReviewIsEnabled(t *testing.T) {
	number := int64(1732)
	payload := buildBulletinNotification("issue-1", sql.NullInt64{Int64: number, Valid: true}, "user-1", false, []bulletins.Version{
		{Locale: "zh-Hant", Title: "繁體標題", Subtitle: "繁體副標", Status: "published"},
		{Locale: "en", Title: "English title", Status: "published"},
		{Locale: "ja", Title: "日本語タイトル", Subtitle: "日本語の副題", Status: "published"},
		{Locale: "ko", Title: "한국어 제목", Subtitle: "한국어 부제", Status: "published"},
	})

	if len(payload.Translations) != 3 || payload.Translations["zh-Hans"].Body != "English title" || payload.Translations["en"].Body != "English title" {
		t.Fatalf("translations=%#v", payload.Translations)
	}
	if _, ok := payload.Translations["ja"]; ok {
		t.Fatalf("Japanese translation leaked before fluent review: %#v", payload.Translations)
	}
	if _, ok := payload.Translations["ko"]; ok {
		t.Fatalf("Korean translation leaked before fluent review: %#v", payload.Translations)
	}
	if payload.Translations["en"].ActionURL != "/en/literature-ministry" || payload.IssueID != "issue-1" || payload.ActorID != "user-1" {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestBuildBulletinNotificationUsesEnglishGenericFallbackWhenEnglishIsUnpublished(t *testing.T) {
	payload := buildBulletinNotification("issue-1", sql.NullInt64{}, "user-1", false, []bulletins.Version{
		{Locale: "zh-Hant", Title: "繁體標題", Subtitle: "繁體副標", Status: "published"},
		{Locale: "en", Title: "Draft English", Status: "draft"},
	})

	if payload.Translations["en"].Body != "The latest weekly bulletin is now available." || payload.Translations["zh-Hans"].Body != "The latest weekly bulletin is now available." {
		t.Fatalf("translations=%#v", payload.Translations)
	}
}

func TestBuildBulletinNotificationUsesPublishedFiveLocaleCopyAfterFluentReview(t *testing.T) {
	number := int64(1732)
	payload := buildBulletinNotification("issue-1", sql.NullInt64{Int64: number, Valid: true}, "user-1", true, []bulletins.Version{
		{Locale: "zh-Hant", Title: "繁體標題", Subtitle: "繁體副標", Status: "published"},
		{Locale: "en", Title: "English title", Status: "published"},
		{Locale: "ja", Title: "日本語タイトル", Subtitle: "日本語の副題", Status: "published"},
		{Locale: "ko", Title: "한국어 제목", Subtitle: "한국어 부제", Status: "published"},
		{Locale: "zh-Hans", Title: "Draft simplified", Status: "draft"},
	})

	if len(payload.Translations) != 5 || payload.Translations["ja"].Subject != "第 1732 号の週報を公開しました" || payload.Translations["ja"].Body != "日本語の副題" || payload.Translations["ko"].Subject != "1732호 주보가 발행되었습니다" || payload.Translations["ko"].Body != "한국어 부제" {
		t.Fatalf("translations=%#v", payload.Translations)
	}
	if payload.Translations["zh-Hans"].Body != "English title" {
		t.Fatalf("draft translation leaked into notification: %#v", payload.Translations)
	}
}

func TestIssueRevisionsReturnsStoredSnapshots(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	snapshot, _ := json.Marshal(bulletins.Issue{ID: "00000000-0000-0000-0000-000000000001", IssueDate: "2026-08-02", Status: "draft", Version: 2})
	mock.ExpectQuery("SELECT EXISTS").WithArgs("00000000-0000-0000-0000-000000000001").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("FROM hhc_web.bulletin_revision").WithArgs("00000000-0000-0000-0000-000000000001").
		WillReturnRows(sqlmock.NewRows([]string{"version", "snapshot_json", "created_by", "created_at"}).AddRow(2, snapshot, "user-1", now))

	revisions, err := New(db).IssueRevisions(context.Background(), "00000000-0000-0000-0000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 1 || revisions[0].Version != 2 || revisions[0].Snapshot.IssueDate != "2026-08-02" {
		t.Fatalf("revisions=%#v", revisions)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreIssueRevisionAllowsMetadataChangeWithoutPublicProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	issueID := "00000000-0000-0000-0000-000000000001"
	versionID := "10000000-0000-0000-0000-000000000001"
	snapshot, _ := json.Marshal(bulletins.Issue{ID: issueID, IssueDate: "2026-08-02", Status: "published", Version: 2, Versions: []bulletins.Version{{
		ID: versionID, IssueID: issueID, Locale: "zh-Hant", Title: "舊週報", PDFAssetID: "asset-1", PDFFileName: "weekly.pdf", Status: "published", Version: 1,
	}}})

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT version,status,issue_number,issue_date::text FROM hhc_web.bulletin_issue").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"version", "status", "issue_number", "issue_date"}).AddRow(3, "published", 1733, "2026-08-09"))
	mock.ExpectQuery("SELECT snapshot_json FROM hhc_web.bulletin_revision").WithArgs(issueID, int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"snapshot_json"}).AddRow(snapshot))
	mock.ExpectQuery("SELECT EXISTS.*public_projection.*resource_type='bulletin_issue'.*resource_id=\\$1").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(issueID, "zh-Hant").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("DELETE FROM hhc_web.bulletin_version").WithArgs(issueID, "zh-Hant").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO hhc_web.bulletin_version").
		WithArgs(versionID, issueID, "zh-Hant", "舊週報", "", "asset-1", "weekly.pdf", "user-2", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE hhc_web.bulletin_issue").WithArgs(issueID, nil, "2026-08-02", int64(4), "user-2", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("FROM hhc_web.bulletin_issue WHERE id=\\$1").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_number", "issue_date", "status", "notification_status", "notification_queued_at", "notification_error_code", "version", "created_by", "updated_by", "published_at", "created_at", "updated_at"}).
			AddRow(issueID, nil, "2026-08-02", "draft", "not_requested", nil, "", 4, "user-1", "user-2", now, now, now))
	mock.ExpectQuery("FROM hhc_web.bulletin_version v").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "locale", "title", "subtitle", "pdf_asset_id", "pdf_file_name", "public_grant_id", "status", "workflow_status", "workflow_error", "version", "published_at", "created_at", "updated_at"}).
			AddRow(versionID, issueID, "zh-Hant", "舊週報", "", "asset-1", "weekly.pdf", "grant-1", "draft", "", "", 2, now, now, now))
	mock.ExpectExec("INSERT INTO hhc_web.bulletin_revision").WithArgs(issueID, int64(4), sqlmock.AnyArg(), "user-2", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	restored, err := New(db).RestoreIssueRevision(context.Background(), issueID, 2, 3, "user-2", now)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Status != "draft" || restored.Version != 4 || len(restored.Versions) != 1 || restored.Versions[0].PDFAssetID != "asset-1" {
		t.Fatalf("restored=%#v", restored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreIssueRevisionRejectsPublicMetadataChangeBeforeMutation(t *testing.T) {
	issueID := "00000000-0000-0000-0000-000000000001"
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		name   string
		number int
		date   string
	}{
		{name: "issue number", number: 1731, date: "2026-08-02"},
		{name: "issue date", number: 1732, date: "2026-07-26"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			snapshot := bulletins.Issue{ID: issueID, IssueNumber: &test.number, IssueDate: test.date, Status: "published", Version: 2, Versions: []bulletins.Version{}}
			payload, _ := json.Marshal(snapshot)

			mock.ExpectBegin()
			mock.ExpectQuery("SELECT version,status,issue_number,issue_date::text FROM hhc_web.bulletin_issue").WithArgs(issueID).
				WillReturnRows(sqlmock.NewRows([]string{"version", "status", "issue_number", "issue_date"}).AddRow(3, "published", 1732, "2026-08-02"))
			mock.ExpectQuery("SELECT snapshot_json FROM hhc_web.bulletin_revision").WithArgs(issueID, int64(2)).
				WillReturnRows(sqlmock.NewRows([]string{"snapshot_json"}).AddRow(payload))
			mock.ExpectQuery("SELECT EXISTS.*public_projection.*resource_type='bulletin_issue'.*resource_id=\\$1").WithArgs(issueID).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			mock.ExpectRollback()

			_, err = New(db).RestoreIssueRevision(context.Background(), issueID, 2, 3, "user-2", now)
			if !errors.Is(err, bulletins.ErrConflict) {
				t.Fatalf("error=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
