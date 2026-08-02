package postgres

import (
	"testing"

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
}

func TestPublicVideoUsesWidelyAvailableYouTubeThumbnail(t *testing.T) {
	item := content.Item{Module: content.ModuleVideos, YouTubeVideoID: "BlBhGrxS9sI"}
	got := publicContent(item, content.Translation{Locale: "zh-Hant", Title: "約沙法大軍"})

	if got.ImageURL != "https://i.ytimg.com/vi/BlBhGrxS9sI/hqdefault.jpg" {
		t.Fatalf("image URL = %q", got.ImageURL)
	}
}
