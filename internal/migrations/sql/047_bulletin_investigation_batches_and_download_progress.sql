ALTER TABLE hhc_web.bulletin_watermark_investigation
  ADD COLUMN scopes_json jsonb;

UPDATE hhc_web.bulletin_watermark_investigation
SET scopes_json=jsonb_build_array(jsonb_build_object(
  'issueId',issue_id::text,
  'locale',locale,
  'revision',revision,
  'sourceAssetId',source_asset_key,
  'sourceSha256',source_sha256,
  'rendererVersion',renderer_version,
  'modelSha256',model_sha256
));

ALTER TABLE hhc_web.bulletin_watermark_investigation
  ALTER COLUMN scopes_json SET NOT NULL,
  DROP CONSTRAINT bulletin_watermark_investigation_result_check,
  ADD CONSTRAINT bulletin_watermark_investigation_result_check CHECK(result IN ('matched','inconclusive','conflict')),
  DROP CONSTRAINT bulletin_watermark_investigation_evidence_check,
  ADD CONSTRAINT bulletin_watermark_investigation_evidence_check CHECK(octet_length(evidence) BETWEEN 1 AND 10502144),
  ADD CONSTRAINT bulletin_watermark_investigation_scopes
    CHECK (jsonb_typeof(scopes_json)='array' AND jsonb_array_length(scopes_json) BETWEEN 1 AND 1000);

ALTER TABLE hhc_web.bulletin_download_job
  ADD COLUMN progress smallint NOT NULL DEFAULT 5 CHECK (progress BETWEEN 0 AND 100);

UPDATE hhc_web.bulletin_download_job
SET progress=CASE status WHEN 'ready' THEN 100 WHEN 'running' THEN 10 ELSE 5 END;
