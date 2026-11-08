CREATE TABLE hhc_web.online_bulletin_recognition_checkpoint (
  id uuid PRIMARY KEY,
  job_id uuid NOT NULL REFERENCES hhc_web.online_bulletin_extraction_job(id) ON DELETE CASCADE,
  source_page integer NOT NULL CHECK (source_page BETWEEN 1 AND 40),
  phase text NOT NULL CHECK (phase IN ('recognition', 'verification', 'verified')),
  input_sha256 text NOT NULL CHECK (input_sha256 ~ '^[0-9a-f]{64}$'),
  protocol_sha256 text NOT NULL CHECK (protocol_sha256 ~ '^[0-9a-f]{64}$'),
  deployment text NOT NULL CHECK (length(deployment) BETWEEN 1 AND 128),
  model_version text NOT NULL CHECK (length(model_version) BETWEEN 1 AND 128),
  checkpoint_json jsonb NOT NULL CHECK (octet_length(checkpoint_json::text) <= 4194304),
  created_at timestamptz NOT NULL
);
CREATE UNIQUE INDEX online_bulletin_recognition_verified_page_idx
  ON hhc_web.online_bulletin_recognition_checkpoint(job_id, source_page) WHERE phase = 'verified';
CREATE INDEX online_bulletin_recognition_job_idx ON hhc_web.online_bulletin_recognition_checkpoint(job_id, created_at);
