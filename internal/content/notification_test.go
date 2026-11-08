package content

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNotificationSubjectUsesCharacterLimit(t *testing.T) {
	r := StatementNotificationRequest{RequestID: "d3728ae9-6679-448e-9628-27fccab5f50c", Channels: []string{"email", "web_push"}, Subject: strings.Repeat("聲", 200), Body: "內容"}
	if !r.Valid() {
		t.Fatal("valid 200-character title rejected")
	}
	r.Subject += "明"
	if r.Valid() {
		t.Fatal("overlong title accepted")
	}
}

func TestNewsAndStatementCanPublishWithNotifications(t *testing.T) {
	for _, kind := range []string{KindGeneral, KindStatement} {
		repo := &serviceRepository{item: Item{Module: ModuleNews, Kind: kind, Slug: "news", DisplayDate: "2026-09-07", DetailLayout: "top", Translations: []Translation{{Locale: "zh-Hant", Title: "標題", Body: "內容"}}}}
		_, err := NewService(repo, time.Now).PublishStatement(context.Background(), "news", 1, "actor", StatementNotificationRequest{RequestID: "d3728ae9-6679-448e-9628-27fccab5f50c", Channels: []string{"email", "web_push"}, Subject: "標題", Body: "內容"})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
