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

func TestImportedHistoryDateBackfillRunsAfterLegacyDataImport(t *testing.T) {
	contents, err := files.ReadFile("sql/015_imported_history_event_date_backfill.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"history.event_date IS NULL", "translation.locale = 'zh-Hant'", "SET event_date = canonical.event_date"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestYouTubeThumbnailMigrationRewritesUnavailableMaxResolutionURLs(t *testing.T) {
	contents, err := files.ReadFile("sql/016_youtube_thumbnail_fallback.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"resource_type = 'videos'", "maxresdefault.jpg", "hqdefault.jpg"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestHistoryProjectionMigrationPublishesBackfilledEventDates(t *testing.T) {
	contents, err := files.ReadFile("sql/017_history_projection_event_dates.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"resource_type = 'history'", "'{eventDate}'", "history.event_date", "etag = md5"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestNewsHomeImageMigrationAddsIndependentPublicationState(t *testing.T) {
	contents, err := files.ReadFile("sql/019_news_home_image.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"home_cover_asset_id", "home_public_grant_id", "published_home_cover_asset_id", "DEFAULT ''"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestBulletinNotificationMigrationAddsIndependentState(t *testing.T) {
	contents, err := files.ReadFile("sql/021_bulletin_notification_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"notification_status", "not_requested", "notification_queued_at", "notification_error_code"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestFiveContentLocalesMigrationReplacesOnlyLocaleChecks(t *testing.T) {
	contents, err := files.ReadFile("sql/022_five_content_locales.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"bulletin_version_locale_check",
		"content_translation_locale_check",
		"public_projection_locale_check",
		"publication_workflow_locale_check",
		"'zh-Hant','zh-Hans','en','ja','ko'",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	if strings.Contains(sql, "UPDATE ") || strings.Contains(sql, "DELETE ") || strings.Contains(sql, "INSERT ") {
		t.Fatal("locale migration must not rewrite existing rows")
	}
}

func TestTranslationRateLimitMigrationAddsAtomicCounter(t *testing.T) {
	contents, err := files.ReadFile("sql/023_translation_rate_limits.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"CREATE TABLE hhc_web.translation_rate_limit",
		"scope text NOT NULL",
		"window_start timestamptz NOT NULL",
		"count integer NOT NULL CHECK (count > 0)",
		"PRIMARY KEY (scope, window_start)",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestTranslationCostLimitMigrationAddsResourceCooldown(t *testing.T) {
	contents, err := files.ReadFile("sql/024_translation_cost_limits.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"CREATE TABLE hhc_web.translation_cooldown",
		"actor text NOT NULL",
		"resource_type text NOT NULL",
		"resource_id text NOT NULL",
		"source_version bigint NOT NULL",
		"target_locale text NOT NULL",
		"next_allowed_at timestamptz NOT NULL",
		"PRIMARY KEY (actor, resource_type, resource_id, source_version, target_locale)",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	if strings.Contains(sql, "DROP ") || strings.Contains(sql, "UPDATE ") || strings.Contains(sql, "DELETE ") {
		t.Fatal("cost-limit migration must be additive")
	}
}

func TestNewsSEOMetadataMigrationBackfillsSourceAndPublicProjections(t *testing.T) {
	contents, err := files.ReadFile("sql/025_news_seo_metadata.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"ADD COLUMN first_published_at timestamptz",
		"ADD COLUMN author_name text NOT NULL DEFAULT ''",
		"SET first_published_at=published_at",
		"'firstPublishedAt'",
		"'lastPublishedAt'",
		"'authorName'",
		"projection.resource_type='news'",
		"etag=md5",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestContentSeedProvenanceMigrationAddsRunAndSourceTables(t *testing.T) {
	contents, err := files.ReadFile("sql/026_content_seed_runs.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"CREATE TABLE hhc_web.content_seed_run",
		"CREATE TABLE hhc_web.content_seed_source",
		"CREATE UNIQUE INDEX uq_content_seed_run_succeeded_version",
		"ON hhc_web.content_seed_run(seed_version)",
		"WHERE status = 'succeeded'",
		"UNIQUE(seed_run_id, source_path, source_key)",
		"REFERENCES hhc_web.content_seed_run(id)",
		"status text NOT NULL CHECK (status IN ('started','succeeded','failed'))",
		"status text NOT NULL CHECK (status IN ('inserted','skipped'))",
		"mode text NOT NULL CHECK (mode IN ('apply'))",
		"target_kind text NOT NULL CHECK (target_kind IN ('location','site_layout','page'))",
		"source_commit text NOT NULL CHECK (source_commit ~ '^[0-9a-f]{40}$')",
		"manifest_sha256 text NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$')",
		"source_sha256 text NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$')",
		"record_sha256 text NOT NULL CHECK (record_sha256 ~ '^[0-9a-f]{64}$')",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}
