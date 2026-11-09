ALTER TABLE hhc_web.online_bulletin_extraction_job
  ADD COLUMN auto_publish boolean NOT NULL DEFAULT false;

CREATE TABLE hhc_web.online_bulletin_update_job (
  id uuid PRIMARY KEY,
  document_id uuid NOT NULL REFERENCES hhc_web.online_bulletin_document(id) ON DELETE CASCADE,
  extraction_job_id uuid NOT NULL UNIQUE REFERENCES hhc_web.online_bulletin_extraction_job(id) ON DELETE CASCADE,
  source_revision bigint NOT NULL,
  canonical_version bigint NOT NULL CHECK (canonical_version>0),
  online_version bigint NOT NULL CHECK (online_version>0),
  protocol_sha256 text NOT NULL CHECK (protocol_sha256 ~ '^[0-9a-f]{64}$'),
  input_json jsonb NOT NULL CHECK (octet_length(input_json::text)<=33554432),
  checkpoint_json jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(checkpoint_json)='array' AND jsonb_array_length(checkpoint_json)<=2),
  evidence_json jsonb,
  mappings_json jsonb,
  result_revision bigint,
  status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','planning','ready','failed','superseded')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 10),
  last_error_code text NOT NULL DEFAULT '',
  lease_token uuid,
  lease_until timestamptz,
  available_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  FOREIGN KEY(document_id,source_revision) REFERENCES hhc_web.online_bulletin_revision(document_id,revision)
);
CREATE INDEX online_bulletin_update_due_idx ON hhc_web.online_bulletin_update_job(available_at,created_at,id) WHERE status IN ('queued','planning');
