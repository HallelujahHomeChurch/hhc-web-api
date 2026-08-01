package migrations

import (
	"strings"
	"testing"
)

func TestHistoryEventDateMigrationProvidesCanonicalFieldAndQueryIndex(t *testing.T) {
	contents, err := files.ReadFile("sql/010_history_event_date.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"ADD COLUMN event_date text",
		"DROP COLUMN sort_order",
		"history_event_event_date_idx",
		"event_date DESC NULLS LAST,entry_id DESC",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}
