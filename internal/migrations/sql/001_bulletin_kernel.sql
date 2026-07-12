CREATE SCHEMA IF NOT EXISTS hhc_web;

CREATE TABLE hhc_web.bulletin_issue (
  id uuid PRIMARY KEY,
  issue_date date NOT NULL UNIQUE,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','publishing','published','unpublished','archived')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text NOT NULL UNIQUE,
  created_by text NOT NULL,
  updated_by text NOT NULL,
  published_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);
CREATE INDEX bulletin_issue_status_date_idx ON hhc_web.bulletin_issue(status, issue_date DESC);

CREATE TABLE hhc_web.bulletin_version (
  id uuid PRIMARY KEY,
  issue_id uuid NOT NULL REFERENCES hhc_web.bulletin_issue(id) ON DELETE CASCADE,
  locale text NOT NULL CHECK (locale IN ('zh-Hant','zh-Hans','en')),
  title text NOT NULL,
  pdf_asset_id text NOT NULL,
  pdf_file_name text NOT NULL,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','publishing','published','unpublished')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL,
  updated_by text NOT NULL,
  published_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE(issue_id, locale)
);
CREATE INDEX bulletin_version_asset_idx ON hhc_web.bulletin_version(pdf_asset_id);

CREATE TABLE hhc_web.public_projection (
  projection_key text PRIMARY KEY,
  resource_type text NOT NULL,
  resource_id uuid,
  locale text NOT NULL CHECK (locale IN ('zh-Hant','zh-Hans','en')),
  route_path text NOT NULL,
  version bigint NOT NULL CHECK (version > 0),
  etag text NOT NULL,
  payload_json jsonb NOT NULL CHECK (jsonb_typeof(payload_json) = 'object'),
  updated_at timestamptz NOT NULL
);
CREATE INDEX public_projection_resource_idx ON hhc_web.public_projection(resource_type, resource_id, locale);
CREATE INDEX public_projection_route_idx ON hhc_web.public_projection(locale, route_path);

CREATE TABLE hhc_web.publication_workflow (
  id uuid PRIMARY KEY,
  workflow_type text NOT NULL CHECK (workflow_type IN ('bulletin_publish','bulletin_unpublish')),
  resource_type text NOT NULL,
  resource_id uuid NOT NULL,
  locale text NOT NULL CHECK (locale IN ('zh-Hant','zh-Hans','en')),
  aggregate_version bigint NOT NULL,
  asset_id text,
  status text NOT NULL CHECK (status IN ('waiting_asset_scan','waiting_asset_grant','projection_pending','public_visible','completed','failed','cancelled')),
  error_code text NOT NULL DEFAULT '',
  error_detail text NOT NULL DEFAULT '',
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE(workflow_type, resource_type, resource_id, locale, aggregate_version)
);
CREATE INDEX publication_workflow_status_idx ON hhc_web.publication_workflow(status, updated_at);

CREATE TABLE hhc_web.outbox_event (
  id uuid PRIMARY KEY,
  destination text NOT NULL,
  event_type text NOT NULL,
  aggregate_type text NOT NULL,
  aggregate_id uuid NOT NULL,
  aggregate_version bigint,
  payload_json jsonb NOT NULL CHECK (jsonb_typeof(payload_json) = 'object'),
  idempotency_key text NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','processing','delivered','failed')),
  attempts integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL,
  claimed_until timestamptz,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  UNIQUE(destination, idempotency_key)
);
CREATE INDEX outbox_event_claim_idx ON hhc_web.outbox_event(status, next_attempt_at, created_at) WHERE status IN ('pending','processing');
