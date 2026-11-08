CREATE TABLE hhc_web.news_summary_state (
  entry_id uuid NOT NULL REFERENCES hhc_web.news_item(entry_id) ON DELETE CASCADE,
  locale text NOT NULL CHECK (locale IN ('zh-Hant','zh-Hans','en','ja','ko')),
  source_hash text NOT NULL CHECK (source_hash ~ '^[0-9a-f]{64}$'),
  source_version bigint NOT NULL CHECK (source_version > 0),
  status text NOT NULL CHECK (status IN ('pending','ready','failed')),
  error_code text NOT NULL DEFAULT '' CHECK (error_code IN ('','AI_DISABLED','AI_TIMEOUT','AI_FILTERED','AI_FAILED','AI_INVALID')),
  PRIMARY KEY (entry_id,locale)
);
