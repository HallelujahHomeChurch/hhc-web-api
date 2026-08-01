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

func TestContentDeleteMigrationPreservesArchivedRowsAsDraft(t *testing.T) {
	contents, err := files.ReadFile("sql/012_content_delete.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"content_entry SET status='draft' WHERE status='archived'",
		"bulletin_issue SET status='draft' WHERE status='archived'",
		"CREATE TABLE hhc_web.cms_audit_event",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}
