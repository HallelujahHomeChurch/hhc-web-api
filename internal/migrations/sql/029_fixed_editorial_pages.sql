ALTER TABLE hhc_web.content_translation
  ADD COLUMN body_json jsonb;

ALTER TABLE hhc_web.content_translation
  ADD CONSTRAINT content_translation_body_json_object_check
  CHECK (body_json IS NULL OR body_json @> '{}'::jsonb);

CREATE TABLE hhc_web.page_item (
  content_id uuid PRIMARY KEY REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  page_key text NOT NULL CHECK (page_key IN ('home','about','privacy-policy','terms-of-use')),
  page_template text NOT NULL CHECK (page_template IN ('home.v1','about.v1','legal.v1')),
  route_path text NOT NULL,
  indexable boolean NOT NULL DEFAULT true,
  CHECK (
    (page_key = 'home' AND page_template = 'home.v1' AND route_path = '/') OR
    (page_key = 'about' AND page_template = 'about.v1' AND route_path = '/about') OR
    (page_key = 'privacy-policy' AND page_template = 'legal.v1' AND route_path = '/privacy-policy') OR
    (page_key = 'terms-of-use' AND page_template = 'legal.v1' AND route_path = '/terms-of-use')
  ),
  UNIQUE(page_key),
  UNIQUE(route_path)
);
