-- Browser source ownership is separate from its eventual immutable HLS package.
ALTER TABLE hhc_web.member_recording
 ADD COLUMN source_id text UNIQUE CHECK (source_id IS NULL OR source_id ~ '^[a-f0-9]{32}$'),
 ADD CONSTRAINT member_recording_source_excludes_legacy CHECK (source_id IS NULL OR primary_asset_version_id IS NULL);
