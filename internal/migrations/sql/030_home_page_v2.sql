ALTER TABLE hhc_web.page_item
  DROP CONSTRAINT page_item_page_template_check,
  DROP CONSTRAINT page_item_check;

ALTER TABLE hhc_web.page_item
  ADD CONSTRAINT page_item_template_check
    CHECK (page_template IN ('home.v1','home.v2','about.v1','legal.v1')) NOT VALID,
  ADD CONSTRAINT page_item_definition_check
    CHECK (
      (page_key = 'home' AND page_template IN ('home.v1','home.v2') AND route_path = '/') OR
      (page_key = 'about' AND page_template = 'about.v1' AND route_path = '/about') OR
      (page_key = 'privacy-policy' AND page_template = 'legal.v1' AND route_path = '/privacy-policy') OR
      (page_key = 'terms-of-use' AND page_template = 'legal.v1' AND route_path = '/terms-of-use')
    ) NOT VALID;

ALTER TABLE hhc_web.page_item
  VALIDATE CONSTRAINT page_item_template_check;
ALTER TABLE hhc_web.page_item
  VALIDATE CONSTRAINT page_item_definition_check;

ALTER TABLE hhc_web.page_item
  ADD COLUMN banner_asset_id text,
  ADD COLUMN published_banner_asset_id text,
  ADD COLUMN banner_public_grant_id text,
  ADD COLUMN published_banner_version bigint,
  ADD COLUMN home_settings jsonb,
  ADD CONSTRAINT page_item_home_settings_object_check
    CHECK (home_settings IS NULL OR jsonb_typeof(home_settings) = 'object');
