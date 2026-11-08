ALTER TABLE hhc_web.bulletin_version
  DROP CONSTRAINT bulletin_version_issue_id_locale_key,
  ADD CONSTRAINT bulletin_version_issue_series_locale_key
    UNIQUE USING INDEX bulletin_version_issue_series_locale_uq;

ALTER TABLE hhc_web.publication_workflow
  DROP CONSTRAINT publication_workflow_workflow_type_resource_type_resource_i_key,
  ADD CONSTRAINT publication_workflow_edition_version_key
    UNIQUE USING INDEX publication_workflow_edition_version_uq;

ALTER TABLE hhc_web.bulletin_published_version
  DROP CONSTRAINT bulletin_published_version_pkey,
  ADD CONSTRAINT bulletin_published_version_pkey
    PRIMARY KEY USING INDEX bulletin_published_version_issue_series_locale_uq;
