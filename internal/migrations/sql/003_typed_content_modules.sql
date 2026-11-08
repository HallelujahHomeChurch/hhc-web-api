CREATE TABLE hhc_web.content_entry (
  id uuid PRIMARY KEY,
  module text NOT NULL CHECK (module IN ('news','history','videos')),
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published','unpublished','archived')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text NOT NULL UNIQUE,
  created_by text NOT NULL,
  updated_by text NOT NULL,
  published_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE INDEX content_entry_module_status_idx ON hhc_web.content_entry(module,status,updated_at DESC);

CREATE TABLE hhc_web.content_translation (
  entry_id uuid NOT NULL REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  locale text NOT NULL CHECK (locale IN ('zh-Hant','zh-Hans','en')),
  title text NOT NULL,
  summary text NOT NULL DEFAULT '',
  body text NOT NULL DEFAULT '',
  date_label text NOT NULL DEFAULT '',
  image_alt text NOT NULL DEFAULT '',
  PRIMARY KEY(entry_id,locale)
);

CREATE TABLE hhc_web.news_item (
  entry_id uuid PRIMARY KEY REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  slug text NOT NULL UNIQUE,
  display_date date NOT NULL,
  cover_asset_id text NOT NULL DEFAULT '',
  featured boolean NOT NULL DEFAULT false
);
CREATE INDEX news_item_display_date_idx ON hhc_web.news_item(display_date DESC);

CREATE TABLE hhc_web.history_event (
  entry_id uuid PRIMARY KEY REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  sort_order integer NOT NULL CHECK (sort_order > 0),
  UNIQUE(sort_order)
);

CREATE TABLE hhc_web.video_item (
  entry_id uuid PRIMARY KEY REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  youtube_video_id text NOT NULL UNIQUE,
  home_eligible boolean NOT NULL DEFAULT true
);

CREATE TABLE hhc_web.content_revision (
  entry_id uuid NOT NULL REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  version bigint NOT NULL,
  snapshot_json jsonb NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY(entry_id,version)
);
