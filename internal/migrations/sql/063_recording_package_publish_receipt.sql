ALTER TABLE hhc_web.member_recording_publish_receipt
  ADD COLUMN package_id text,
  ADD CONSTRAINT recording_receipt_package_id_check CHECK (package_id IS NULL OR package_id ~ '^[a-f0-9]{32}$');
