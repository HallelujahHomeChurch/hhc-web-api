ALTER TABLE hhc_web.bulletin_watermark_receipt
 ADD COLUMN fingerprint_payload text,
 ADD COLUMN model_sha256 text,
 ADD COLUMN pages_json jsonb,
 ADD CONSTRAINT bulletin_watermark_fingerprint_complete CHECK (
  (fingerprint_payload IS NULL AND model_sha256 IS NULL AND pages_json IS NULL)
  OR (fingerprint_payload IS NOT NULL AND model_sha256 IS NOT NULL AND pages_json IS NOT NULL
      AND fingerprint_payload ~ '^[01]{61}$' AND model_sha256 ~ '^[a-f0-9]{64}$'
      AND jsonb_typeof(pages_json)='array' AND jsonb_array_length(pages_json) BETWEEN 1 AND 60)
 );
-- Unexpired and expired rows share a uniqueness domain until retention cleanup.
CREATE UNIQUE INDEX bulletin_watermark_receipt_fingerprint_key
 ON hhc_web.bulletin_watermark_receipt(source_asset_id,revision,renderer_version,fingerprint_payload)
 WHERE fingerprint_payload IS NOT NULL;
CREATE INDEX bulletin_watermark_receipt_candidates
 ON hhc_web.bulletin_watermark_receipt(issue_id,locale,revision,source_asset_id,renderer_version,model_sha256)
 WHERE fingerprint_payload IS NOT NULL;
