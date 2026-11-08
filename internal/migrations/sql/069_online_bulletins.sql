-- Unreleased reader migrations follow the main branch's recording migrations.
ALTER TABLE hhc_web.bulletin_version
  ADD COLUMN pdf_sha256 text NOT NULL DEFAULT '' CHECK (pdf_sha256='' OR pdf_sha256 ~ '^[0-9a-f]{64}$');

CREATE TABLE hhc_web.online_bulletin_document (
  id uuid PRIMARY KEY,
  issue_id uuid NOT NULL,
  series text NOT NULL,
  content_locale text NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  draft_status text NOT NULL DEFAULT 'extracting'
    CHECK (draft_status IN ('extracting','reviewRequired','ready','published','extractionFailed')),
  base_revision bigint,
  local_revision bigint,
  incoming_revision bigint,
  published_revision bigint,
  metadata_sync_pending boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (issue_id,series,content_locale),
  UNIQUE (id,issue_id,series,content_locale),
  FOREIGN KEY (issue_id,series,content_locale)
    REFERENCES hhc_web.bulletin_version(issue_id,series,locale) ON DELETE RESTRICT
);

CREATE TABLE hhc_web.online_bulletin_revision (
  document_id uuid NOT NULL,
  issue_id uuid NOT NULL,
  series text NOT NULL,
  content_locale text NOT NULL,
  revision bigint NOT NULL CHECK (revision > 0),
  document_json jsonb NOT NULL CHECK (jsonb_typeof(document_json)='object' AND octet_length(document_json::text) <= 8388608),
  source_asset_checksum text NOT NULL CHECK (source_asset_checksum ~ '^[0-9a-f]{64}$'),
  schema_version text NOT NULL DEFAULT '1',
  template_version text NOT NULL,
  extractor_version text NOT NULL,
  content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
  renderer_version text NOT NULL,
  renderer_artifact_sha256 text NOT NULL CHECK (renderer_artifact_sha256 ~ '^[0-9a-f]{64}$'),
  layout_validation_hash text CHECK (layout_validation_hash ~ '^[0-9a-f]{64}$'),
  review_issues jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(review_issues)='array' AND jsonb_array_length(review_issues)<=4096),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (document_id,revision),
  UNIQUE (issue_id,series,content_locale,revision),
  FOREIGN KEY (document_id,issue_id,series,content_locale)
    REFERENCES hhc_web.online_bulletin_document(id,issue_id,series,content_locale) ON DELETE RESTRICT
);

ALTER TABLE hhc_web.online_bulletin_document
  ADD FOREIGN KEY (id,base_revision) REFERENCES hhc_web.online_bulletin_revision(document_id,revision),
  ADD FOREIGN KEY (id,local_revision) REFERENCES hhc_web.online_bulletin_revision(document_id,revision),
  ADD FOREIGN KEY (id,incoming_revision) REFERENCES hhc_web.online_bulletin_revision(document_id,revision);

CREATE TABLE hhc_web.online_bulletin_extraction_job (
  id uuid PRIMARY KEY,
  document_id uuid NOT NULL,
  issue_id uuid NOT NULL,
  series text NOT NULL,
  content_locale text NOT NULL,
  source_asset_id text NOT NULL,
  source_asset_checksum text NOT NULL CHECK (source_asset_checksum ~ '^[0-9a-f]{64}$'),
  template_version text NOT NULL,
  extractor_version text NOT NULL,
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','parsing','review_ready','failed')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  available_at timestamptz NOT NULL DEFAULT now(),
  lease_until timestamptz,
  lease_token uuid,
  last_error_code text NOT NULL DEFAULT '' CHECK (last_error_code ~ '^[a-z0-9_]{0,64}$'),
  superseded boolean NOT NULL DEFAULT false,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (issue_id,series,content_locale,source_asset_checksum,template_version,extractor_version),
  FOREIGN KEY (document_id,issue_id,series,content_locale)
    REFERENCES hhc_web.online_bulletin_document(id,issue_id,series,content_locale) ON DELETE RESTRICT
);
CREATE INDEX online_bulletin_extraction_job_claim_idx
  ON hhc_web.online_bulletin_extraction_job(available_at,created_at)
  WHERE status IN ('queued','parsing') AND NOT superseded;

-- Publication records contain frozen canonical metadata. Revisions themselves
-- never receive in-place edits when measured, confirmed, restored or published.
CREATE TABLE hhc_web.online_bulletin_publication (
  document_id uuid NOT NULL,
  revision bigint NOT NULL,
  canonical_metadata jsonb NOT NULL CHECK (jsonb_typeof(canonical_metadata)='object'),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (document_id,revision),
  FOREIGN KEY (document_id,revision)
    REFERENCES hhc_web.online_bulletin_revision(document_id,revision) ON DELETE RESTRICT
);
ALTER TABLE hhc_web.online_bulletin_document
  ADD FOREIGN KEY (id,published_revision) REFERENCES hhc_web.online_bulletin_publication(document_id,revision);

-- These are publication checkpoints, never draft-save counters.
CREATE TABLE hhc_web.online_bulletin_mapping (
  document_id uuid NOT NULL,
  from_revision bigint NOT NULL,
  to_revision bigint NOT NULL CHECK (to_revision > from_revision),
  from_sentence_id text NOT NULL CHECK (from_sentence_id ~ '^[A-Za-z0-9_-]{1,128}$'),
  status text NOT NULL CHECK (status IN ('unchanged','moved','split','merged','removed','requires_review')),
  to_sentence_ids jsonb NOT NULL CHECK (jsonb_typeof(to_sentence_ids)='array' AND jsonb_array_length(to_sentence_ids)<=1024),
  PRIMARY KEY (document_id,from_revision,to_revision,from_sentence_id),
  FOREIGN KEY (document_id,from_revision) REFERENCES hhc_web.online_bulletin_publication(document_id,revision) ON DELETE RESTRICT,
  FOREIGN KEY (document_id,to_revision) REFERENCES hhc_web.online_bulletin_publication(document_id,revision) ON DELETE RESTRICT,
  CHECK ((status='removed' AND jsonb_array_length(to_sentence_ids)=0)
    OR (status='split' AND jsonb_array_length(to_sentence_ids)>=2)
    OR (status IN ('unchanged','moved','merged') AND jsonb_array_length(to_sentence_ids)=1)
    OR status='requires_review')
);

CREATE FUNCTION hhc_web.reject_online_bulletin_history_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'online bulletin history is immutable' USING ERRCODE='23514';
END;
$$;
CREATE TRIGGER online_bulletin_revision_immutable BEFORE UPDATE ON hhc_web.online_bulletin_revision
  FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();
CREATE TRIGGER online_bulletin_publication_immutable BEFORE UPDATE ON hhc_web.online_bulletin_publication
  FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();
CREATE TRIGGER online_bulletin_mapping_immutable BEFORE UPDATE ON hhc_web.online_bulletin_mapping
  FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();

CREATE FUNCTION hhc_web.check_online_bulletin_published_mapping() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.published_revision IS DISTINCT FROM OLD.published_revision
    AND NEW.published_revision IS NOT NULL
    AND EXISTS (SELECT 1 FROM hhc_web.online_bulletin_mapping
      WHERE document_id=NEW.id AND to_revision=NEW.published_revision AND status='requires_review') THEN
    RAISE EXCEPTION 'online bulletin mapping requires review' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER online_bulletin_publication_mapping_guard BEFORE UPDATE ON hhc_web.online_bulletin_document
  FOR EACH ROW EXECUTE FUNCTION hhc_web.check_online_bulletin_published_mapping();
