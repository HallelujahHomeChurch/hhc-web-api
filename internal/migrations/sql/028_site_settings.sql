CREATE TABLE hhc_web.site_setting_set (
  id text PRIMARY KEY CHECK (id = 'default'),
  status text NOT NULL CHECK (status IN ('draft','published','unpublished')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  external_links_json jsonb NOT NULL CHECK (jsonb_typeof(external_links_json) = 'object'),
  created_by text NOT NULL,
  updated_by text NOT NULL,
  published_by text,
  published_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE hhc_web.site_setting_locale (
  setting_set_id text NOT NULL REFERENCES hhc_web.site_setting_set(id) ON DELETE CASCADE,
  locale text NOT NULL CHECK (locale IN ('zh-Hant','zh-Hans','en','ja','ko')),
  site_name text NOT NULL,
  english_name text NOT NULL,
  copyright_holder text NOT NULL,
  all_rights_reserved text NOT NULL,
  seo_title_suffix text NOT NULL,
  seo_description_fallback text NOT NULL,
  header_items_json jsonb NOT NULL CHECK (jsonb_typeof(header_items_json) = 'array'),
  legal_items_json jsonb NOT NULL CHECK (jsonb_typeof(legal_items_json) = 'array'),
  PRIMARY KEY(setting_set_id, locale)
);

CREATE TABLE hhc_web.site_setting_revision (
  id text PRIMARY KEY,
  setting_set_id text NOT NULL REFERENCES hhc_web.site_setting_set(id),
  revision bigint NOT NULL,
  revision_type text NOT NULL CHECK (revision_type IN ('draft_saved','published','unpublished','seeded','restored_to_draft')),
  snapshot_json jsonb NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  UNIQUE(setting_set_id, revision)
);
