ALTER TABLE hhc_web.content_translation
  ADD COLUMN translated_from_source_hash text,
  ADD CONSTRAINT content_translation_source_hash_check
    CHECK (translated_from_source_hash IS NULL OR translated_from_source_hash ~ '^[0-9a-f]{64}$');

CREATE TABLE hhc_web.content_asset_ref (
  entry_id uuid NOT NULL REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  locale text NOT NULL CHECK (locale IN ('zh-Hant','zh-Hans','en','ja','ko')),
  block_id text NOT NULL,
  asset_id text NOT NULL,
  purpose text NOT NULL CHECK (purpose = 'statement_inline'),
  PRIMARY KEY (entry_id, locale, block_id)
);

CREATE INDEX content_asset_ref_asset_idx ON hhc_web.content_asset_ref(asset_id);

CREATE TABLE hhc_web.content_published_asset (
  entry_id uuid NOT NULL REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  asset_id text NOT NULL,
  grant_id text NOT NULL,
  public_url text NOT NULL,
  published_version bigint NOT NULL CHECK (published_version > 0),
  PRIMARY KEY (entry_id, asset_id)
);
