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
		"history_event_event_date_idx",
		"event_date DESC NULLS LAST,entry_id DESC",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	if strings.Contains(sql, "DROP COLUMN sort_order") {
		t.Fatal("expand migration must retain sort_order for rollback compatibility")
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
	if strings.Contains(sql, "DROP CONSTRAINT") {
		t.Fatal("expand migration must retain status constraints for rollback compatibility")
	}
}

func TestCanonicalAssetURLMigrationRewritesExistingPublicProjections(t *testing.T) {
	contents, err := files.ReadFile("sql/014_canonical_asset_urls.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"/api/assets/public/", "/assets/", "downloadUrl", "imageUrl", "etag"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}
