ALTER TABLE hhc_web.member_recording
  ADD COLUMN package_id text UNIQUE,
  ADD CONSTRAINT member_recording_package_id_check CHECK (package_id IS NULL OR package_id ~ '^[a-f0-9]{32}$'),
  ADD CONSTRAINT member_recording_media_exclusive_check CHECK (package_id IS NULL OR primary_asset_version_id IS NULL);
