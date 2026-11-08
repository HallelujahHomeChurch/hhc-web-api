ALTER TABLE hhc_web.news_item
 ADD COLUMN kind TEXT NOT NULL DEFAULT 'general' CHECK (kind IN ('general','statement')),
 ADD COLUMN popup_starts_at TIMESTAMPTZ,
 ADD COLUMN popup_ends_at TIMESTAMPTZ,
 ADD CONSTRAINT statement_window_valid CHECK (
  (popup_starts_at IS NULL AND popup_ends_at IS NULL) OR
  (kind='statement' AND popup_starts_at IS NOT NULL AND popup_ends_at IS NOT NULL AND popup_starts_at < popup_ends_at)
 );
