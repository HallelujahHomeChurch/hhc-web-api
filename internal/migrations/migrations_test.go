package migrations

import (
	"io/fs"
	"path"
	"strings"
	"testing"
)

func TestProtectAllBulletinsMigrationRemovesPublicStateAndSeedsExactPolicies(t *testing.T) {
	contents, err := files.ReadFile("sql/049_protect_all_bulletins.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"ADD COLUMN series", "bulletin_entitlement_policy",
		"bulletin.general.zh-Hant.access", "bulletin.general.zh-Hans.access", "bulletin.general.en.access",
		"DELETE FROM hhc_web.public_projection", "DELETE FROM hhc_web.outbox_event",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	if strings.Contains(sql, "DROP TABLE") {
		t.Fatal("expand migration must retain legacy tables for a later contract release")
	}
}

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

func TestBulletinInvestigationRefinementDeletesReasonAndPage(t *testing.T) {
	contents, err := files.ReadFile("sql/043_bulletin_investigation_refinement.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"payload_json - 'reason'", "DROP COLUMN reason", "DROP COLUMN page"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestBulletinDownloadJobsMigrationKeepsWorkDurableAndPrivate(t *testing.T) {
	contents, err := files.ReadFile("sql/044_bulletin_download_jobs.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"bulletin_download_job", "queued','running','ready','failed','expired", "UNIQUE (account_id,idempotency_key)", "UNIQUE (account_id,source_asset_id,renderer_version)", "cache_expires_at", "bulletin_download_job_lease"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestBulletinWatermarkOutboxVersionMigrationRepairsQueuedWork(t *testing.T) {
	contents, err := files.ReadFile("sql/045_bulletin_watermark_outbox_version.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"bulletin.watermark.enqueue", "aggregate_version", "status IN ('pending','processing')"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestBulletinDownloadRecoveryMigrationRequeuesStuckJobs(t *testing.T) {
	contents, err := files.ReadFile("sql/046_bulletin_download_recovery.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"status IN ('queued','running')", "lease_token=NULL", "j.generation", "status='pending'", "ON CONFLICT(destination,idempotency_key) DO UPDATE"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestAuditFilterMigrationRepairsOnlyKnownDeadLetters(t *testing.T) {
	contents, err := files.ReadFile("sql/051_requeue_audit_filter_events.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"(payload_json->'metadata') - 'filterNames'::text", "cms.bulletin.list", "cms.content.list", "status = 'failed'", "last_error = 'dead_letter'", "status = 'pending'"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestBulletinInvestigationBatchesAndDownloadProgressMigration(t *testing.T) {
	contents, err := files.ReadFile("sql/047_bulletin_investigation_batches_and_download_progress.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"scopes_json", "jsonb_array_length(scopes_json) BETWEEN 1 AND 1000", "'matched','inconclusive','conflict'", "10502144", "ADD COLUMN progress", "progress BETWEEN 0 AND 100"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestBulletinInvestigationHybridIdentificationMigrationIsAdditive(t *testing.T) {
	contents, err := files.ReadFile("sql/053_bulletin_investigation_hybrid_identification.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"ADD COLUMN match_method text",
		"'output_sha256','payload','candidate'",
		"SET match_method='candidate'",
		"result='matched'",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	for _, forbidden := range []string{"DROP COLUMN", "DROP TABLE", "DROP CONSTRAINT"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expand migration contains %q", forbidden)
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

func TestFiveBulletinEditionsExpandMigrationIsAdditive(t *testing.T) {
	contents, err := files.ReadFile("sql/056_five_bulletin_editions.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"ADD COLUMN series text NOT NULL DEFAULT 'general'",
		"bulletin_version_issue_series_locale_uq",
		"publication_workflow_edition_version_uq",
		"bulletin_published_version_issue_series_locale_uq",
		"series='children' AND locale IN ('zh-Hant','en')",
		"bulletin.children.zh-Hant.access",
		"bulletin.children.en.access",
		"ALTER TABLE hhc_web.bulletin_download_job",
		"ALTER TABLE hhc_web.bulletin_watermark_receipt",
		"ALTER TABLE hhc_web.bulletin_watermark_investigation",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	for _, forbidden := range []string{"DROP CONSTRAINT", "DROP INDEX", "DROP COLUMN", "DROP TABLE"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("expand migration contains %q", forbidden)
		}
	}
}

func TestBulletinEditionCompositeKeysMigrationDropsOnlyLegacyConstraints(t *testing.T) {
	contents, err := files.ReadFile("sql/057_bulletin_edition_composite_keys.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"DROP CONSTRAINT bulletin_version_issue_id_locale_key",
		"UNIQUE USING INDEX bulletin_version_issue_series_locale_uq",
		"DROP CONSTRAINT publication_workflow_workflow_type_resource_type_resource_i_key",
		"UNIQUE USING INDEX publication_workflow_edition_version_uq",
		"DROP CONSTRAINT bulletin_published_version_pkey",
		"PRIMARY KEY USING INDEX bulletin_published_version_issue_series_locale_uq",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	for _, forbidden := range []string{"DROP TABLE", "DROP COLUMN", "DELETE FROM", "UPDATE ", "TRUNCATE"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("contract migration contains %q", forbidden)
		}
	}
}

func TestBulletinUploadIdentityMigrationOnlyExpandsUniqueKey(t *testing.T) {
	contents, err := files.ReadFile("sql/086_online_bulletin_upload_source_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{"DROP CONSTRAINT online_bulletin_extraction_jo_issue_id_series_content_local_key", "CREATE UNIQUE INDEX online_bulletin_extraction_upload_key", "(issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version)", "WHERE NOT superseded"} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	for _, forbidden := range []string{"DROP TABLE", "DROP COLUMN", "DELETE FROM", "UPDATE ", "TRUNCATE"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("upload expansion contains %q", forbidden)
		}
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

func TestLocationsMigrationAddsFinalizedModulesAndDetailTable(t *testing.T) {
	contents, err := files.ReadFile("sql/027_locations_and_content_modules.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"DROP CONSTRAINT content_entry_module_check",
		"'news','history','videos','locations','pages'",
		"CHECK (module IN",
		"NOT VALID",
		"VALIDATE CONSTRAINT content_entry_module_check",
		"CREATE TABLE hhc_web.location_item",
		"stable_key text NOT NULL",
		"map_href text NOT NULL",
		"sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order >= 0)",
		"location_item_stable_key_uq",
		"location_item_sort_idx",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestSiteSettingsMigrationAddsSingletonLocalesAndRevisions(t *testing.T) {
	contents, err := files.ReadFile("sql/028_site_settings.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"CREATE TABLE hhc_web.site_setting_set",
		"id text PRIMARY KEY CHECK (id = 'default')",
		"CREATE TABLE hhc_web.site_setting_locale",
		"'zh-Hant','zh-Hans','en','ja','ko'",
		"header_items_json jsonb NOT NULL",
		"legal_items_json jsonb NOT NULL",
		"CREATE TABLE hhc_web.site_setting_revision",
		"'draft_saved','published','unpublished','seeded','restored_to_draft'",
		"UNIQUE(setting_set_id, revision)",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestFixedEditorialPagesMigrationAddsJSONAndFixedPageMetadata(t *testing.T) {
	contents, err := files.ReadFile("sql/029_fixed_editorial_pages.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"ADD COLUMN body_json jsonb",
		"content_translation_body_json_object_check",
		"body_json @> '{}'::jsonb",
		"CREATE TABLE hhc_web.page_item",
		"'home','about','privacy-policy','terms-of-use'",
		"'home.v1','about.v1','legal.v1'",
		"page_key = 'home' AND page_template = 'home.v1' AND route_path = '/'",
		"UNIQUE(page_key)",
		"UNIQUE(route_path)",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestStatementRichContentMigrationAddsReferencesAndTranslationFingerprint(t *testing.T) {
	contents, err := files.ReadFile("sql/052_statement_rich_content.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"ADD COLUMN translated_from_source_hash text",
		"CREATE TABLE hhc_web.content_asset_ref",
		"purpose = 'statement_inline'",
		"PRIMARY KEY (entry_id, locale, block_id)",
		"CREATE TABLE hhc_web.content_published_asset",
		"PRIMARY KEY (entry_id, asset_id)",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestHomePageV2MigrationReplacesBothPageConstraintsAndAddsState(t *testing.T) {
	contents, err := files.ReadFile("sql/030_home_page_v2.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"DROP CONSTRAINT page_item_page_template_check",
		"DROP CONSTRAINT page_item_check",
		"page_item_template_check",
		"'home.v1','home.v2','about.v1','legal.v1'",
		"page_item_definition_check",
		"page_key = 'home' AND page_template IN ('home.v1','home.v2') AND route_path = '/'",
		"ADD COLUMN banner_asset_id text",
		"ADD COLUMN published_banner_asset_id text",
		"ADD COLUMN banner_public_grant_id text",
		"ADD COLUMN published_banner_version bigint",
		"ADD COLUMN home_settings jsonb",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	if strings.Contains(sql, "DROP TABLE") || strings.Contains(sql, "DELETE ") {
		t.Fatal("home v2 migration must preserve legacy data")
	}
}

func TestPageGroupPublicationMigrationAddsPendingRemovalAndManifest(t *testing.T) {
	contents, err := files.ReadFile("sql/031_page_group_publication.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"'pending_removal'",
		"CREATE TABLE hhc_web.page_publication_manifest",
		"page_id uuid NOT NULL",
		"page_version bigint NOT NULL",
		"child_module text NOT NULL",
		"manifest_sha256 text NOT NULL",
		"manifest_json jsonb NOT NULL",
		"publication_status text NOT NULL",
		"PRIMARY KEY(page_id,page_version)",
		"CHECK (child_module IN ('history','videos'))",
		"CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$')",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
}

func TestOperationsMeetingMigrationAddsKernelTablesAndIndexes(t *testing.T) {
	contents, err := files.ReadFile("sql/032_operations_meetings.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, expected := range []string{
		"CREATE TABLE hhc_web.church_unit",
		"CREATE TABLE hhc_web.resource",
		"CREATE TABLE hhc_web.meeting",
		"CREATE TABLE hhc_web.meeting_occurrence_override",
		"CREATE TABLE hhc_web.meeting_collection_binding",
		"CREATE TABLE hhc_web.operations_audit",
		"church_unit_stable_key_uq",
		"church_unit_idempotency_key_uq",
		"resource_stable_key_uq",
		"resource_idempotency_key_uq",
		"meeting_stable_key_uq",
		"meeting_idempotency_key_uq",
		"church_unit_status_parent_idx",
		"resource_status_unit_idx",
		"meeting_status_venue_idx",
		"meeting_occurrence_override_date_idx",
		"meeting_collection_binding_collection_idx",
	} {
		if !strings.Contains(sql, expected) {
			t.Fatalf("migration missing %q", expected)
		}
	}
	if strings.Contains(sql, "CREATE TABLE hhc_web.resource_reservation") || strings.Contains(sql, "CREATE TABLE hhc_web.resource_maintenance") {
		t.Fatal("Phase 1 migration must not add reservation tables")
	}
}

func TestRetireOperationsKernelMigrationDropsOnlyMigratedTables(t *testing.T) {
	contents, err := files.ReadFile("sql/050_retire_operations_kernel.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(contents)
	for _, table := range []string{
		"meeting_collection_binding",
		"meeting_occurrence_override",
		"meeting",
		"resource",
		"church_unit",
		"operations_audit",
	} {
		if !strings.Contains(sql, "DROP TABLE hhc_web."+table+";") {
			t.Fatalf("migration does not drop %s", table)
		}
	}
	if strings.Contains(sql, "CASCADE") {
		t.Fatal("migration must fail when an unknown dependency still exists")
	}
}

func TestNewMigrationNumbersAreUnique(t *testing.T) {
	names, err := fs.Glob(files, "sql/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, name := range names {
		number := strings.SplitN(path.Base(name), "_", 2)[0]
		// Earlier published migrations include duplicate sequence numbers; preserve those immutable files.
		if number < "086" {
			continue
		}
		if prior, ok := seen[number]; ok {
			t.Fatalf("duplicate migration sequence %s: %s and %s", number, prior, name)
		}
		seen[number] = name
	}
}
