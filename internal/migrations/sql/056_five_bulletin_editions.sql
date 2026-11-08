ALTER TABLE hhc_web.bulletin_version
  ADD COLUMN series text NOT NULL DEFAULT 'general',
  ADD CONSTRAINT bulletin_version_edition_check
    CHECK ((series='general' AND locale IN ('zh-Hant','zh-Hans','en')) OR (series='children' AND locale IN ('zh-Hant','en')));
CREATE UNIQUE INDEX bulletin_version_issue_series_locale_uq
  ON hhc_web.bulletin_version(issue_id,series,locale);

ALTER TABLE hhc_web.publication_workflow
  ADD COLUMN series text NOT NULL DEFAULT 'general',
  ADD CONSTRAINT publication_workflow_edition_check
    CHECK ((series='general' AND locale IN ('zh-Hant','zh-Hans','en')) OR (series='children' AND locale IN ('zh-Hant','en')));
CREATE UNIQUE INDEX publication_workflow_edition_version_uq
  ON hhc_web.publication_workflow(workflow_type,resource_type,resource_id,series,locale,aggregate_version);

ALTER TABLE hhc_web.bulletin_published_version
  ADD CONSTRAINT bulletin_published_version_edition_check
    CHECK ((series='general' AND locale IN ('zh-Hant','zh-Hans','en')) OR (series='children' AND locale IN ('zh-Hant','en')));
CREATE UNIQUE INDEX bulletin_published_version_issue_series_locale_uq
  ON hhc_web.bulletin_published_version(issue_id,series,locale);

INSERT INTO hhc_web.bulletin_entitlement_policy(series,locale,entitlement_code) VALUES
  ('children','zh-Hant','bulletin.children.zh-Hant.access'),
  ('children','en','bulletin.children.en.access')
ON CONFLICT (series,locale) DO NOTHING;

ALTER TABLE hhc_web.bulletin_download_job
  ADD COLUMN series text NOT NULL DEFAULT 'general'
  CHECK (series IN ('general','children'));
ALTER TABLE hhc_web.bulletin_watermark_receipt
  ADD COLUMN series text NOT NULL DEFAULT 'general'
  CHECK (series IN ('general','children'));
ALTER TABLE hhc_web.bulletin_watermark_investigation
  ADD COLUMN series text NOT NULL DEFAULT 'general'
  CHECK (series IN ('general','children'));
