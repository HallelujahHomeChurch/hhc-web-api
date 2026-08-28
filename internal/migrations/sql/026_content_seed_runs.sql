CREATE TABLE hhc_web.content_seed_run (
  id text PRIMARY KEY,
  seed_version text NOT NULL,
  source_repo text NOT NULL,
  source_commit text NOT NULL CHECK (source_commit ~ '^[0-9a-f]{40}$'),
  manifest_sha256 text NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
  mode text NOT NULL CHECK (mode IN ('apply')),
  status text NOT NULL CHECK (status IN ('started','succeeded','failed')),
  warning_count integer NOT NULL DEFAULT 0,
  inserted_count integer NOT NULL DEFAULT 0,
  skipped_count integer NOT NULL DEFAULT 0,
  conflict_count integer NOT NULL DEFAULT 0,
  created_by text NOT NULL,
  started_at timestamptz NOT NULL,
  finished_at timestamptz
);

CREATE UNIQUE INDEX uq_content_seed_run_succeeded_version
  ON hhc_web.content_seed_run(seed_version)
  WHERE status = 'succeeded';

CREATE TABLE hhc_web.content_seed_source (
  id text PRIMARY KEY,
  seed_run_id text NOT NULL REFERENCES hhc_web.content_seed_run(id),
  source_path text NOT NULL,
  source_key text NOT NULL,
  source_sha256 text NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
  record_sha256 text NOT NULL CHECK (record_sha256 ~ '^[0-9a-f]{64}$'),
  target_kind text NOT NULL CHECK (target_kind IN ('location','site_layout','page')),
  target_id text NOT NULL,
  status text NOT NULL CHECK (status IN ('inserted','skipped')),
  created_at timestamptz NOT NULL,
  UNIQUE(seed_run_id, source_path, source_key)
);
