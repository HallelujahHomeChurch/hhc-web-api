ALTER TABLE hhc_web.content_entry DROP CONSTRAINT content_entry_status_check;
ALTER TABLE hhc_web.content_entry ADD CONSTRAINT content_entry_status_check
  CHECK (status IN ('draft','publishing','published','publish_failed','unpublishing','unpublish_failed','unpublished','pending_removal','archived'));

CREATE TABLE hhc_web.page_publication_manifest (
  page_id uuid NOT NULL REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  page_version bigint NOT NULL CHECK (page_version > 0),
  child_module text NOT NULL CHECK (child_module IN ('history','videos')),
  manifest_sha256 text NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
  manifest_json jsonb NOT NULL CHECK (jsonb_typeof(manifest_json) = 'object'),
  publication_status text NOT NULL CHECK (publication_status IN ('pending','published','failed','unpublished')),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY(page_id,page_version)
);
