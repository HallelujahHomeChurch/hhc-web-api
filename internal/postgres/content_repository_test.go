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
