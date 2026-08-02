package postgres

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

func TestHistoryContentOrderingUsesEventDateNullLastAndStableID(t *testing.T) {
	order, join := contentOrdering(content.ModuleHistory, "eventDate", "desc")
	if order != "h.event_date DESC NULLS LAST,e.id DESC" {
		t.Fatalf("order=%q", order)
	}
	if join != "JOIN hhc_web.history_event h ON h.entry_id=e.id" {
		t.Fatalf("join=%q", join)
	}
}

func TestPublicHistoryOrderingIsOldestFirstNullLastAndStable(t *testing.T) {
	if order := publicContentOrdering(content.ModuleHistory); order != "payload_json->>'eventDate' ASC NULLS LAST, resource_id ASC" {
		t.Fatalf("order=%q", order)
	}
}

func TestPublicNewsFallbackUsesCanonicalAssetPath(t *testing.T) {
	item := content.Item{Module: content.ModuleNews, CoverAssetID: "asset-1", Slug: "news"}
	translation := content.Translation{Locale: "zh-Hant", Title: "消息"}

	got := publicContent(item, translation)

	if got.ImageURL != "/assets/asset-1/large" {
		t.Fatalf("image URL = %q", got.ImageURL)
	}
	if got.HomeImageURL != got.ImageURL {
		t.Fatalf("home image URL = %q", got.HomeImageURL)
	}
}

func TestPublicNewsPrefersDedicatedHomeImage(t *testing.T) {
	item := content.Item{Module: content.ModuleNews, CoverAssetID: "detail-asset", HomeCoverAssetID: "home-asset", Slug: "news"}
	got := publicContent(item, content.Translation{Locale: "zh-Hant", Title: "消息"})

	if got.ImageURL != "/assets/detail-asset/large" || got.HomeImageURL != "/assets/home-asset/large" {
		t.Fatalf("detail=%q home=%q", got.ImageURL, got.HomeImageURL)
	}
}

func TestPublicNewsAllowsNoImage(t *testing.T) {
	got := publicContent(content.Item{Module: content.ModuleNews, Slug: "news"}, content.Translation{Locale: "zh-Hant", Title: "消息"})
	if got.ImageURL != "" || got.HomeImageURL != "" {
		t.Fatalf("detail=%q home=%q", got.ImageURL, got.HomeImageURL)
	}
}

func TestContentAssetDiscoveryIncludesCurrentAndRevisionHomeImages(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT asset_id FROM (")).WithArgs("news-1").
		WillReturnRows(sqlmock.NewRows([]string{"asset_id"}).AddRow("detail-current").AddRow("home-current").AddRow("home-revision"))

	assets, err := contentAssetIDs(context.Background(), tx, content.ModuleNews, "news-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 3 || assets[2] != "home-revision" {
		t.Fatalf("assets=%#v", assets)
	}
	mock.ExpectRollback()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicVideoUsesWidelyAvailableYouTubeThumbnail(t *testing.T) {
	item := content.Item{Module: content.ModuleVideos, YouTubeVideoID: "BlBhGrxS9sI"}
	got := publicContent(item, content.Translation{Locale: "zh-Hant", Title: "約沙法大軍"})

	if got.ImageURL != "https://i.ytimg.com/vi/BlBhGrxS9sI/hqdefault.jpg" {
		t.Fatalf("image URL = %q", got.ImageURL)
	}
}
