ALTER TABLE hhc_web.bulletin_version
  ADD COLUMN retiring_asset_id text,
  ADD COLUMN retiring_grant_id text;

ALTER TABLE hhc_web.bulletin_version DROP CONSTRAINT bulletin_version_status_check;
ALTER TABLE hhc_web.bulletin_version ADD CONSTRAINT bulletin_version_status_check
  CHECK (status IN ('draft','publishing','published','unpublishing','unpublish_failed','unpublished'));

ALTER TABLE hhc_web.bulletin_issue DROP CONSTRAINT bulletin_issue_status_check;
ALTER TABLE hhc_web.bulletin_issue ADD CONSTRAINT bulletin_issue_status_check
  CHECK (status IN ('draft','publishing','published','unpublishing','unpublish_failed','unpublished','archived'));

ALTER TABLE hhc_web.publication_workflow DROP CONSTRAINT publication_workflow_status_check;
ALTER TABLE hhc_web.publication_workflow ADD CONSTRAINT publication_workflow_status_check
  CHECK (status IN ('waiting_asset_scan','waiting_asset_grant','projection_pending','public_visible','revoke_pending','completed','failed','cancelled'));
