-- Owned by the existing account/document row, including its DSR lifecycle.
ALTER TABLE hhc_web.bulletin_reader_migration_state
    ADD COLUMN highlight_history jsonb NOT NULL DEFAULT '[]'
    CHECK (jsonb_typeof(highlight_history) = 'array');
