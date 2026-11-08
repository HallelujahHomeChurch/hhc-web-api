-- Unreleased reader migrations follow the main branch's recording migrations.
CREATE TABLE hhc_web.online_bulletin_layout_job (
  id uuid PRIMARY KEY,
  document_id uuid NOT NULL,
  source_revision bigint NOT NULL,
  canonical_metadata jsonb NOT NULL CHECK (jsonb_typeof(canonical_metadata)='object' AND octet_length(canonical_metadata::text)<=65536),
  content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
  renderer_artifact_sha256 text NOT NULL CHECK (renderer_artifact_sha256 ~ '^[0-9a-f]{64}$'),
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','validating','ready','failed','superseded')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
  available_at timestamptz NOT NULL DEFAULT now(),
  lease_until timestamptz,
  lease_token uuid,
  last_error_code text NOT NULL DEFAULT '' CHECK (last_error_code ~ '^[a-z0-9_]{0,64}$'),
  result_revision bigint,
  -- JSON (not JSONB) retains the exact worker bytes used by the proof hash.
  measurement_json json CHECK (json_typeof(measurement_json)='object' AND octet_length(measurement_json::text)<=16777216),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (document_id,source_revision) REFERENCES hhc_web.online_bulletin_revision(document_id,revision),
  FOREIGN KEY (document_id,result_revision) REFERENCES hhc_web.online_bulletin_revision(document_id,revision),
  CHECK ((status='ready')=(result_revision IS NOT NULL AND measurement_json IS NOT NULL))
);
CREATE INDEX online_bulletin_layout_job_claim_idx ON hhc_web.online_bulletin_layout_job(available_at,created_at)
  WHERE status IN ('queued','validating');
CREATE UNIQUE INDEX online_bulletin_layout_job_active_idx ON hhc_web.online_bulletin_layout_job(document_id)
  WHERE status IN ('queued','validating');

CREATE FUNCTION hhc_web.reject_online_bulletin_layout_job_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.document_id IS DISTINCT FROM OLD.document_id OR NEW.source_revision IS DISTINCT FROM OLD.source_revision OR
    NEW.canonical_metadata IS DISTINCT FROM OLD.canonical_metadata OR NEW.content_hash IS DISTINCT FROM OLD.content_hash OR
    NEW.renderer_artifact_sha256 IS DISTINCT FROM OLD.renderer_artifact_sha256 OR
    (OLD.status='ready' AND row_to_json(NEW)::text IS DISTINCT FROM row_to_json(OLD)::text) THEN
    RAISE EXCEPTION 'online bulletin layout evidence is immutable' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER online_bulletin_layout_job_immutable BEFORE UPDATE ON hhc_web.online_bulletin_layout_job
  FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_layout_job_update();
