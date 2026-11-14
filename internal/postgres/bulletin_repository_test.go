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
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func TestRepositoryRejectsUnsupportedEditionsBeforeDatabase(t *testing.T) {
	repository := New(nil)
	now := time.Now()
	for _, locale := range []string{"ja", "ko"} {
		if _, err := repository.PutVersion(context.Background(), "issue-1", 1, bulletins.PutVersionInput{Locale: locale}, "user-1", now); !errors.Is(err, bulletins.ErrInvalid) {
			t.Fatalf("put %s error=%v", locale, err)
		}
		if _, err := repository.UpdateVersion(context.Background(), "issue-1", "general", locale, 1, "title", "", "user-1", now); !errors.Is(err, bulletins.ErrInvalid) {
			t.Fatalf("update %s error=%v", locale, err)
		}
		if _, err := repository.DeleteVersion(context.Background(), "issue-1", "general", locale, 1, "user-1", now); !errors.Is(err, bulletins.ErrInvalid) {
			t.Fatalf("delete %s error=%v", locale, err)
		}
		if _, err := repository.StartPublish(context.Background(), "issue-1", "general", locale, 1, false, "user-1", now); !errors.Is(err, bulletins.ErrInvalid) {
			t.Fatalf("publish %s error=%v", locale, err)
		}
		if _, err := repository.Unpublish(context.Background(), "issue-1", "general", locale, 1, "user-1", now); !errors.Is(err, bulletins.ErrInvalid) {
			t.Fatalf("unpublish %s error=%v", locale, err)
		}
	}
}

func TestCompletePublishRejectsUnsupportedEditionBeforeDatabase(t *testing.T) {
	event := publication.Event{
		ID:      "event-1",
		Payload: []byte(`{"issueId":"issue-1","locale":"ja","assetId":"asset-ja","aggregateVersion":3}`),
	}
	err := New(nil).CompletePublish(context.Background(), event, "grant-ja", "/asset-ja", time.Now())
	if !errors.Is(err, bulletins.ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
}

func TestRestoreIssueRevisionRejectsUnsupportedEditionBeforeMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	issueID := "00000000-0000-0000-0000-000000000001"
	snapshot, _ := json.Marshal(bulletins.Issue{ID: issueID, IssueDate: "2026-08-02", Versions: []bulletins.Version{{Locale: "ko", PDFAssetID: "asset-ko"}}})
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT version,status,issue_number,issue_date::text FROM hhc_web.bulletin_issue").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"version", "status", "issue_number", "issue_date"}).AddRow(3, "draft", 1732, "2026-08-02"))
	mock.ExpectQuery("SELECT snapshot_json FROM hhc_web.bulletin_revision").WithArgs(issueID, int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"snapshot_json"}).AddRow(snapshot))
	mock.ExpectRollback()

	_, err = New(db).RestoreIssueRevision(context.Background(), issueID, 2, 3, "user-2", time.Now())
	if !errors.Is(err, bulletins.ErrInvalid) {
		t.Fatalf("error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_number", "issue_date", "status", "version", "created_by", "updated_by", "published_at", "created_at", "updated_at"}).
			AddRow("00000000-0000-0000-0000-000000000001", 1732, "2026-08-02", "draft", 2, "user-1", "user-1", nil, now, now).
			AddRow("00000000-0000-0000-0000-000000000002", 1731, "2026-07-26", "draft", 2, "user-1", "user-1", nil, now, now))
	mock.ExpectQuery("FROM hhc_web.bulletin_version v").
		WithArgs("00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002").
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "series", "locale", "title", "subtitle", "pdf_asset_id", "pdf_file_name", "public_grant_id", "status", "workflow_status", "workflow_error", "version", "published_at", "created_at", "updated_at"}).
			AddRow("10000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000001", "general", "zh-Hant", "本週週報", "副標", "asset-1", "weekly.pdf", "", "draft", "", "", 1, nil, now, now).
			AddRow("10000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000002", "general", "en", "Weekly", "", "asset-2", "weekly-en.pdf", "", "draft", "", "", 1, nil, now, now))

	page, err := New(db).ListIssues(context.Background(), 1, 20, "", "", "issueNumber", "desc")
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

func TestBuildBulletinNotificationCombinesTitleAndSubtitle(t *testing.T) {
	for _, test := range []struct{ name, title, subtitle, want string }{
		{"both", " 主標 ", " 副標 ", "主標\n副標"},
		{"title only", " 主標 ", " \n ", "主標"},
		{"subtitle only", " ", " 副標 ", "副標"},
		{"same", " 主標 ", "主標", "主標"},
		{"empty", " ", "\n", "The latest weekly bulletin is now available."},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := buildBulletinNotification("issue-1", "general", sql.NullInt64{Int64: 1737, Valid: true}, "user-1", []bulletins.Version{
				{Series: "general", Locale: "en", Title: test.title, Subtitle: test.subtitle, Status: "published"},
			})
			translations := payload.Notifications[0].Translations
			for _, locale := range []string{"zh-Hant", "zh-Hans", "en"} {
				if got := translations[locale].Body; got != test.want {
					t.Errorf("%s body=%q, want %q", locale, got, test.want)
				}
			}
		})
	}
}

func TestBuildBulletinNotificationUsesOnlyMatchingSeries(t *testing.T) {
	number := int64(1732)
	payload := buildBulletinNotification("issue-1", "children", sql.NullInt64{Int64: number, Valid: true}, "user-1", []bulletins.Version{
		{Series: "general", Locale: "zh-Hant", Title: "一般週報", Status: "published"},
		{Series: "children", Locale: "zh-Hant", Title: "兒童週報", Status: "published"},
		{Series: "children", Locale: "en", Title: "Children's bulletin", Status: "published"},
	})
	translations := payload.Notifications[0].Translations
	if len(translations) != 2 || translations["zh-Hant"].Body != "兒童週報" || translations["en"].Body != "Children's bulletin" {
		t.Fatalf("translations=%#v", translations)
	}
	if got := translations["zh-Hant"]; got.Subject != "第 1732 期兒童週報已發布" || got.ActionURL != "/zh-Hant/literature-ministry?series=children" {
		t.Fatalf("Traditional Chinese notification=%#v", got)
	}
	if payload.Source == nil || payload.Source.Series != "children" || payload.Notifications[0].RequiredEntitlements["en"] != "bulletin.children.en.access" || payload.ActorID != "user-1" || len(payload.Notifications) != 2 {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestBuildBulletinNotificationUsesEnglishGenericFallbackWhenEnglishIsUnpublished(t *testing.T) {
	payload := buildBulletinNotification("issue-1", "general", sql.NullInt64{}, "user-1", []bulletins.Version{
		{Series: "general", Locale: "zh-Hant", Title: "繁體標題", Subtitle: "繁體副標", Status: "published"},
		{Series: "general", Locale: "en", Title: "Draft English", Status: "draft"},
	})

	translations := payload.Notifications[0].Translations
	if translations["en"].Body != "The latest weekly bulletin is now available." || translations["zh-Hans"].Body != "The latest weekly bulletin is now available." {
		t.Fatalf("translations=%#v", translations)
	}
}

func TestSubmitBulletinNotificationAllocatesAndReplaysSeriesGeneration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 9, 25, 1, 0, 0, 0, time.UTC)
	issueID, clientKey := "00000000-0000-0000-0000-000000000001", "10000000-0000-4000-8000-000000000001"

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id::text,issue_number FROM hhc_web.bulletin_issue").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_number"}).AddRow(issueID, 1300))
	mock.ExpectQuery("SELECT .*notificationGeneration.* FROM hhc_web.outbox_event WHERE destination='engagement-api'").WithArgs("manual-bulletin:" + clientKey).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("FROM hhc_web.bulletin_version v").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "series", "locale", "title", "subtitle", "pdf_asset_id", "pdf_file_name", "public_grant_id", "status", "workflow_status", "workflow_error", "version", "published_at", "created_at", "updated_at"}).
			AddRow("20000000-0000-4000-8000-000000000001", issueID, "children", "zh-Hant", "兒童週報", "", "asset-1", "children.pdf", "", "published", "", "", 1, now, now, now))
	mock.ExpectQuery("SELECT max.*notificationGeneration").WithArgs(issueID, "children").
		WillReturnRows(sqlmock.NewRows([]string{"max"}).AddRow(nil))
	mock.ExpectExec("INSERT INTO hhc_web.outbox_event").WithArgs(sqlmock.AnyArg(), issueID, int64(0), sqlmock.AnyArg(), "manual-bulletin:"+clientKey, now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := New(db).SubmitBulletinNotification(context.Background(), issueID, "children", "user-1", clientKey, now)
	if err != nil || result.NotificationGeneration != 0 || result.Series != "children" {
		t.Fatalf("result=%#v err=%v", result, err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id::text,issue_number FROM hhc_web.bulletin_issue").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_number"}).AddRow(issueID, 1300))
	mock.ExpectQuery("SELECT .*notificationGeneration.* FROM hhc_web.outbox_event WHERE destination='engagement-api'").WithArgs("manual-bulletin:" + clientKey).
		WillReturnRows(sqlmock.NewRows([]string{"generation"}).AddRow(0))
	mock.ExpectCommit()

	replayed, err := New(db).SubmitBulletinNotification(context.Background(), issueID, "children", "user-1", clientKey, now)
	if err != nil || replayed.NotificationGeneration != 0 {
		t.Fatalf("replayed=%#v err=%v", replayed, err)
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
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

func TestRestoreIssueRevisionAllowsDraftMetadataChange(t *testing.T) {
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
		WillReturnRows(sqlmock.NewRows([]string{"version", "status", "issue_number", "issue_date"}).AddRow(3, "draft", 1733, "2026-08-09"))
	mock.ExpectQuery("SELECT snapshot_json FROM hhc_web.bulletin_revision").WithArgs(issueID, int64(2)).
		WillReturnRows(sqlmock.NewRows([]string{"snapshot_json"}).AddRow(snapshot))
	mock.ExpectQuery("SELECT EXISTS.*bulletin_published_version.*issue_id=\\$1").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(issueID, "general", "zh-Hant").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT EXISTS.*online_bulletin_document").WithArgs(issueID, "general", "zh-Hant").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT COALESCE\\(v.online_intent_id::text").WithArgs(issueID, "general", "zh-Hant").
		WillReturnRows(sqlmock.NewRows([]string{"intent_id", "epoch", "asset", "checksum", "canonical", "enabled", "history"}).AddRow("", 0, "asset-1", "", 3, false, false))
	mock.ExpectQuery("UPDATE hhc_web.publication_workflow w SET status='cancelled'").WithArgs(issueID, "general", "zh-Hant", now).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	mock.ExpectExec("UPDATE hhc_web.bulletin_version SET auto_publish").WithArgs(issueID, "general", "zh-Hant", false, false, "").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE hhc_web.online_bulletin_extraction_job SET auto_publish=false").WithArgs(issueID, "general", "zh-Hant").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("UPDATE hhc_web.online_bulletin_update_job SET status='superseded'").WithArgs(issueID, "general", "zh-Hant", now).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("DELETE FROM hhc_web.bulletin_version").WithArgs(issueID, "general", "zh-Hant").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO hhc_web.bulletin_version").
		WithArgs(versionID, issueID, "general", "zh-Hant", "舊週報", "", "asset-1", "weekly.pdf", "user-2", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE hhc_web.bulletin_issue").WithArgs(issueID, nil, "2026-08-02", int64(4), "user-2", now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("FROM hhc_web.bulletin_issue WHERE id=\\$1").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_number", "issue_date", "status", "version", "created_by", "updated_by", "published_at", "created_at", "updated_at"}).
			AddRow(issueID, nil, "2026-08-02", "draft", 4, "user-1", "user-2", now, now, now))
	mock.ExpectQuery("FROM hhc_web.bulletin_version v").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "series", "locale", "title", "subtitle", "pdf_asset_id", "pdf_file_name", "public_grant_id", "status", "workflow_status", "workflow_error", "version", "published_at", "created_at", "updated_at"}).
			AddRow(versionID, issueID, "general", "zh-Hant", "舊週報", "", "asset-1", "weekly.pdf", "grant-1", "draft", "", "", 2, now, now, now))
	mock.ExpectQuery("FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_revision").WithArgs(issueID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "version", "document_json", "review_issues", "extractor_version", "content_hash"}))
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

func TestRestoreIssueRevisionRejectsPublishedMetadataChangeBeforeMutation(t *testing.T) {
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
			mock.ExpectQuery("SELECT EXISTS.*bulletin_published_version.*issue_id=\\$1").WithArgs(issueID).
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
