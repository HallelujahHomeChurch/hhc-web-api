package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

func TestContentRevisionLoadsTargetedSnapshot(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT r.version,r.snapshot_json,r.created_by,r.created_at").
		WithArgs("video-1", content.ModuleVideos, int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"version", "snapshot_json", "created_by", "created_at"}).
			AddRow(int64(7), []byte(`{"id":"video-1","module":"videos","translations":[{"locale":"ja","title":"動画"}]}`), "user-1", now))

	revision, err := New(db).ContentRevision(context.Background(), content.ModuleVideos, "video-1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if revision.Version != 7 || revision.CreatedBy != "user-1" || len(revision.Snapshot.Translations) != 1 || revision.Snapshot.Translations[0].Title != "動画" {
		t.Fatalf("revision=%#v", revision)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestContentRevisionReturnsNotFound(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT r.version,r.snapshot_json,r.created_by,r.created_at").
		WithArgs("video-1", content.ModuleVideos, int64(99)).
		WillReturnError(sql.ErrNoRows)

	_, err = New(db).ContentRevision(context.Background(), content.ModuleVideos, "video-1", 99)
	if !errors.Is(err, content.ErrNotFound) {
		t.Fatalf("error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

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

func TestPublicContentFallbackExposesResolvedAndAvailableLocales(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(DISTINCT resource_id) FROM hhc_web.public_projection WHERE resource_type=$1 AND locale IN ($2,'zh-Hant')")).
		WithArgs(content.ModuleNews, "ja").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT paged.resource_id,paged.locale,paged.payload_json,availability.locales").
		WithArgs(content.ModuleNews, "ja", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"resource_id", "locale", "payload_json", "available_locales"}).
			AddRow("news-1", "zh-Hant", []byte(`{"id":"news-1","title":"消息","href":"/zh-Hant/news/news-1"}`), []byte(`["en","zh-Hant"]`)))

	page, err := New(db).PublicContent(context.Background(), content.ModuleNews, "ja", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items=%#v", page.Items)
	}
	item := page.Items[0]
	if item.ResolvedLocale != "zh-Hant" || !reflect.DeepEqual(item.AvailableLocales, []string{"zh-Hant", "en"}) || item.Href != "/ja/news/news-1" {
		t.Fatalf("item=%#v", item)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicNewsFallbackExposesResolvedAndAvailableLocales(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery("SELECT selected.resource_id,selected.locale,selected.payload_json,selected.etag,availability.locales").
		WithArgs("ja", "news-1").
		WillReturnRows(sqlmock.NewRows([]string{"resource_id", "locale", "payload_json", "etag", "available_locales"}).
			AddRow("news-1", "zh-Hant", []byte(`{"id":"news-1","title":"消息","href":"/zh-Hant/news/news-1"}`), "etag-1", []byte(`["en","zh-Hant"]`)))

	item, etag, err := New(db).PublicNews(context.Background(), "ja", "news-1")
	if err != nil {
		t.Fatal(err)
	}
	if etag == "etag-1" || item.ResolvedLocale != "zh-Hant" || !reflect.DeepEqual(item.AvailableLocales, []string{"zh-Hant", "en"}) || item.Href != "/ja/news/news-1" {
		t.Fatalf("item=%#v etag=%q", item, etag)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicContentPrefersExactRequestedLocale(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(DISTINCT resource_id) FROM hhc_web.public_projection WHERE resource_type=$1 AND locale IN ($2,'zh-Hant')")).
		WithArgs(content.ModuleNews, "ja").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT paged.resource_id,paged.locale,paged.payload_json,availability.locales").
		WithArgs(content.ModuleNews, "ja", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"resource_id", "locale", "payload_json", "available_locales"}).
			AddRow("news-1", "ja", []byte(`{"id":"news-1","title":"ニュース","href":"/ja/news/news-1"}`), []byte(`["en","ja","zh-Hant"]`)))

	page, err := New(db).PublicContent(context.Background(), content.ModuleNews, "ja", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	item := page.Items[0]
	if item.Title != "ニュース" || item.ResolvedLocale != "ja" || !reflect.DeepEqual(item.AvailableLocales, []string{"zh-Hant", "en", "ja"}) || item.Href != "/ja/news/news-1" {
		t.Fatalf("item=%#v", item)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicContentLoadsAvailableLocalesInListDataQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(DISTINCT resource_id) FROM hhc_web.public_projection WHERE resource_type=$1 AND locale IN ($2,'zh-Hant')")).
		WithArgs(content.ModuleNews, "ja").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectQuery("SELECT paged.resource_id,paged.locale,paged.payload_json,availability.locales").
		WithArgs(content.ModuleNews, "ja", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"resource_id", "locale", "payload_json", "available_locales"}).
			AddRow("news-1", "zh-Hant", []byte(`{"id":"news-1","title":"消息一"}`), []byte(`["en","zh-Hant"]`)).
			AddRow("news-2", "zh-Hant", []byte(`{"id":"news-2","title":"消息二"}`), []byte(`["zh-Hant"]`)))

	page, err := New(db).PublicContent(context.Background(), content.ModuleNews, "ja", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || !reflect.DeepEqual(page.Items[0].AvailableLocales, []string{"zh-Hant", "en"}) || !reflect.DeepEqual(page.Items[1].AvailableLocales, []string{"zh-Hant"}) {
		t.Fatalf("items=%#v", page.Items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicContentSortsAvailableLocalesCanonically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT count(DISTINCT resource_id) FROM hhc_web.public_projection WHERE resource_type=$1 AND locale IN ($2,'zh-Hant')")).
		WithArgs(content.ModuleNews, "ja").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT paged.resource_id,paged.locale,paged.payload_json,availability.locales").
		WithArgs(content.ModuleNews, "ja", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{"resource_id", "locale", "payload_json", "available_locales"}).
			AddRow("news-1", "ja", []byte(`{"id":"news-1","title":"ニュース"}`), []byte(`["en","ja","ko","zh-Hans","zh-Hant"]`)))

	page, err := New(db).PublicContent(context.Background(), content.ModuleNews, "ja", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(page.Items[0].AvailableLocales, []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}) {
		t.Fatalf("availableLocales=%#v", page.Items[0].AvailableLocales)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublicNewsETagTracksResponseLocaleVariant(t *testing.T) {
	baseline := publicNewsETag(t, "ja", []string{"zh-Hant"})
	if repeated := publicNewsETag(t, "ja", []string{"zh-Hant"}); repeated != baseline {
		t.Fatalf("same response etag=%q want=%q", repeated, baseline)
	}
	if withSibling := publicNewsETag(t, "ja", []string{"zh-Hant", "en"}); withSibling == baseline {
		t.Fatalf("sibling locale did not change etag=%q", withSibling)
	}
	if requestedLocale := publicNewsETag(t, "zh-Hans", []string{"zh-Hant"}); requestedLocale == baseline {
		t.Fatalf("requested locale did not change etag=%q", requestedLocale)
	}
}

func publicNewsETag(t *testing.T, requestedLocale string, availableLocales []string) string {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	encodedLocales, err := json.Marshal(availableLocales)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT selected.resource_id,selected.locale,selected.payload_json,selected.etag,availability.locales").
		WithArgs(requestedLocale, "news-1").
		WillReturnRows(sqlmock.NewRows([]string{"resource_id", "locale", "payload_json", "etag", "available_locales"}).
			AddRow("news-1", "zh-Hant", []byte(`{"id":"news-1","title":"消息","href":"/zh-Hant/news/news-1"}`), "projection-etag", encodedLocales))

	_, etag, err := New(db).PublicNews(context.Background(), requestedLocale, "news-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	return etag
}

func TestPublicNewsFallbackUsesCanonicalAssetPath(t *testing.T) {
	item := content.Item{Module: content.ModuleNews, CoverAssetID: "asset-1", Slug: "news", DetailLayout: "left"}
	translation := content.Translation{Locale: "zh-Hant", Title: "消息"}

	got := publicContent(item, translation)

	if got.ImageURL != "/assets/asset-1/large" {
		t.Fatalf("image URL = %q", got.ImageURL)
	}
	if got.HomeImageURL != got.ImageURL {
		t.Fatalf("home image URL = %q", got.HomeImageURL)
	}
	if got.DetailLayout != "left" {
		t.Fatalf("detail layout = %q", got.DetailLayout)
	}
}

func TestPublicNewsIncludesAttributionAndPublicationTimestamps(t *testing.T) {
	first := time.Date(2026, 8, 14, 1, 0, 0, 0, time.UTC)
	last := first.Add(time.Hour)
	got := publicContent(content.Item{
		Module:           content.ModuleNews,
		Slug:             "news",
		AuthorName:       "王牧師",
		FirstPublishedAt: &first,
		PublishedAt:      &last,
	}, content.Translation{Locale: "zh-Hant", Title: "消息"})

	if got.AuthorName != "王牧師" || got.FirstPublishedAt == nil || !got.FirstPublishedAt.Equal(first) || got.LastPublishedAt == nil || !got.LastPublishedAt.Equal(last) {
		t.Fatalf("public=%#v", got)
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

func TestPublicLocationUsesStableKeyInsteadOfContentID(t *testing.T) {
	got := publicLocation(content.Item{
		ID: "10000000-0000-4000-8000-000000000001", Module: content.ModuleLocations,
		LocationKey: "taipei", MapHref: "https://maps.app.goo.gl/fDus6nVswbuhSEAd8", SortOrder: 10,
	}, content.Translation{Locale: "zh-Hant", Title: "台北哈利路亞家教會", Body: "台北地址"})
	if got.ID != "taipei" || got.Name != "台北哈利路亞家教會" || got.Address != "台北地址" || got.SortOrder != 10 || got.MapHref == "" {
		t.Fatalf("location=%#v", got)
	}
}

func TestPublicLocationsReadsExactLocaleAndSortsNumerically(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT projection.payload_json,availability.locales").
		WithArgs("ja").
		WillReturnRows(sqlmock.NewRows([]string{"payload_json", "available_locales"}).
			AddRow([]byte(`{"id":"taipei","name":"台北","address":"Taipei","mapHref":"https://maps.example.com/taipei","sortOrder":2}`), []byte(`["en","ja","zh-Hant"]`)).
			AddRow([]byte(`{"id":"zhongli","name":"中壢","address":"Zhongli","mapHref":"https://maps.example.com/zhongli","sortOrder":10}`), []byte(`["ja","zh-Hant"]`)))

	items, err := New(db).PublicLocations(context.Background(), "ja")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "taipei" || items[1].ID != "zhongli" || items[0].ResolvedLocale != "ja" || !reflect.DeepEqual(items[0].AvailableLocales, []string{"zh-Hant", "en", "ja"}) {
		t.Fatalf("items=%#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
