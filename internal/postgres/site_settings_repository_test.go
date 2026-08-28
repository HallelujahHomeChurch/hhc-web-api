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
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

func TestSiteSettingsSaveRejectsStaleVersionBeforeWrites(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT version FROM hhc_web.site_setting_set WHERE id=$1 FOR UPDATE")).
		WithArgs(sitesettings.SingletonID).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(int64(2)))
	mock.ExpectRollback()

	_, err = NewSiteSettingsRepository(db).Save(context.Background(), sitesettings.WriteInput{}, 1, "admin", time.Now())
	if !errors.Is(err, sitesettings.ErrPrecondition) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSiteSettingsPublishRejectsIncompleteLocalesBeforeProjectionMutation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	links, _ := json.Marshal(sitesettings.ExternalLinks{
		ChurchYouTube: "https://youtube.com/@hhc33", ChurchFacebook: "https://facebook.com/hhc", MusicYouTube: "https://youtube.com/@music",
	})
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT version FROM hhc_web.site_setting_set WHERE id=$1 FOR UPDATE")).
		WithArgs(sitesettings.SingletonID).
		WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(int64(1)))
	mock.ExpectQuery("SELECT id,status,version,external_links_json").
		WithArgs(sitesettings.SingletonID).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "version", "external_links_json", "created_by", "updated_by", "published_by", "published_at", "created_at", "updated_at"}).
			AddRow("default", "draft", int64(1), links, "admin", "admin", "", nil, now, now))
	mock.ExpectQuery("SELECT locale,site_name,english_name").
		WithArgs(sitesettings.SingletonID).
		WillReturnRows(sqlmock.NewRows([]string{"locale", "site_name", "english_name", "copyright_holder", "all_rights_reserved", "seo_title_suffix", "seo_description_fallback", "header_items_json", "legal_items_json"}))
	mock.ExpectRollback()

	_, err = NewSiteSettingsRepository(db).Publish(context.Background(), 1, "admin", now)
	if !errors.Is(err, sitesettings.ErrNotPublishable) {
		t.Fatalf("err=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSiteLayoutProjectionUsesExactLocaleRoutes(t *testing.T) {
	now := time.Date(2026, 8, 28, 1, 2, 3, 0, time.UTC)
	value := sitesettings.Settings{
		Version: 4, PublishedAt: &now,
		Links: sitesettings.ExternalLinks{ChurchYouTube: "https://youtube.com/@hhc33"},
	}
	locale := sitesettings.LocaleSettings{
		Locale: "ja", SiteName: "教会", EnglishName: "HHC", CopyrightHolder: "HHC", AllRightsReserved: "All rights reserved",
		SEOTitleSuffix: "HHC", SEODescriptionFallback: "description",
		Header: []sitesettings.NavItem{{Key: "about", Label: "概要", Href: "/{locale}/about", Visible: true}},
		Legal:  []sitesettings.NavItem{{Key: "privacy-policy", Label: "privacy", Href: "/{locale}/privacy-policy", Visible: true}},
	}
	payload, err := siteLayoutProjection(value, locale)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Locale string                 `json:"locale"`
		Header []sitesettings.NavItem `json:"header"`
		Legal  []sitesettings.NavItem `json:"legal"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.Locale != "ja" || got.Header[0].Href != "/ja/about" || got.Legal[0].Href != "/ja/privacy-policy" {
		t.Fatalf("projection=%s", payload)
	}
}

func TestSiteSettingsGetReturnsNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT id,status,version,external_links_json").
		WithArgs(sitesettings.SingletonID).
		WillReturnError(sql.ErrNoRows)
	_, err = NewSiteSettingsRepository(db).Get(context.Background())
	if !errors.Is(err, sitesettings.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestSiteSettingsPublicReadsExactLocaleProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT payload_json FROM hhc_web.public_projection WHERE projection_key=$1 AND resource_type='site_layout' AND locale=$2")).
		WithArgs("site_layout:ja", "ja").
		WillReturnRows(sqlmock.NewRows([]string{"payload_json"}).AddRow([]byte(`{"locale":"ja","siteName":"教会","version":4}`)))

	value, err := NewSiteSettingsRepository(db).Public(context.Background(), "ja")
	if err != nil || value.Locale != "ja" || value.SiteName != "教会" || value.Version != 4 {
		t.Fatalf("value=%#v err=%v", value, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSiteSettingsPublicMapsMissingProjection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT payload_json FROM hhc_web.public_projection").
		WithArgs("site_layout:ko", "ko").
		WillReturnError(sql.ErrNoRows)

	_, err = NewSiteSettingsRepository(db).Public(context.Background(), "ko")
	if !errors.Is(err, sitesettings.ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}
