-- Different upload objects keep independent immutable extraction evidence,
-- even when their verified bytes are identical.
ALTER TABLE hhc_web.online_bulletin_extraction_job
  DROP CONSTRAINT online_bulletin_extraction_jo_issue_id_series_content_local_key;
CREATE UNIQUE INDEX online_bulletin_extraction_upload_key
  ON hhc_web.online_bulletin_extraction_job
    (issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version)
  WHERE NOT superseded;
CREATE INDEX online_bulletin_upload_request_idx
  ON hhc_web.outbox_event (aggregate_id,aggregate_version DESC)
  WHERE event_type='bulletin.online.ensure_extraction';
