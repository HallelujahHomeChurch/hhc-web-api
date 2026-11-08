CREATE TABLE hhc_web.church_unit (
  id uuid PRIMARY KEY,
  stable_key text NOT NULL CHECK (stable_key ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
  name text NOT NULL CHECK (btrim(name) <> ''),
  description text NOT NULL DEFAULT '',
  parent_id uuid REFERENCES hhc_web.church_unit(id),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','archived')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text NOT NULL,
  created_by text NOT NULL,
  updated_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX church_unit_stable_key_uq ON hhc_web.church_unit(stable_key);
CREATE UNIQUE INDEX church_unit_idempotency_key_uq ON hhc_web.church_unit(idempotency_key);
CREATE INDEX church_unit_status_parent_idx ON hhc_web.church_unit(status,parent_id);

CREATE TABLE hhc_web.resource (
  id uuid PRIMARY KEY,
  stable_key text NOT NULL CHECK (stable_key ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
  name text NOT NULL CHECK (btrim(name) <> ''),
  description text NOT NULL DEFAULT '',
  kind text NOT NULL CHECK (kind IN ('venue')),
  church_unit_id uuid NOT NULL REFERENCES hhc_web.church_unit(id),
  location_content_id uuid REFERENCES hhc_web.location_item(content_id),
  timezone text NOT NULL DEFAULT 'Asia/Taipei',
  visibility text NOT NULL CHECK (visibility IN ('public','internal')),
  reservation_enabled boolean NOT NULL DEFAULT false CHECK (reservation_enabled = false),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','archived')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text NOT NULL,
  created_by text NOT NULL,
  updated_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX resource_stable_key_uq ON hhc_web.resource(stable_key);
CREATE UNIQUE INDEX resource_idempotency_key_uq ON hhc_web.resource(idempotency_key);
CREATE INDEX resource_status_unit_idx ON hhc_web.resource(status,church_unit_id);

CREATE TABLE hhc_web.meeting (
  id uuid PRIMARY KEY,
  stable_key text NOT NULL CHECK (stable_key ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
  name text NOT NULL CHECK (btrim(name) <> ''),
  description text NOT NULL DEFAULT '',
  church_unit_id uuid NOT NULL REFERENCES hhc_web.church_unit(id),
  venue_resource_id uuid NOT NULL REFERENCES hhc_web.resource(id),
  timezone text NOT NULL DEFAULT 'Asia/Taipei',
  schedule_type text NOT NULL CHECK (schedule_type IN ('weekly','once')),
  weekly_days smallint[],
  weekly_start_time time,
  once_starts_at timestamptz,
  duration_minutes integer NOT NULL CHECK (duration_minutes BETWEEN 1 AND 1440),
  visibility text NOT NULL CHECK (visibility IN ('public','internal')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','paused','archived')),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text NOT NULL,
  created_by text NOT NULL,
  updated_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CHECK (
    (schedule_type = 'weekly' AND weekly_days IS NOT NULL AND cardinality(weekly_days) > 0 AND weekly_days <@ ARRAY[0,1,2,3,4,5,6]::smallint[] AND weekly_start_time IS NOT NULL AND once_starts_at IS NULL)
    OR
    (schedule_type = 'once' AND weekly_days IS NULL AND weekly_start_time IS NULL AND once_starts_at IS NOT NULL)
  )
);

CREATE UNIQUE INDEX meeting_stable_key_uq ON hhc_web.meeting(stable_key);
CREATE UNIQUE INDEX meeting_idempotency_key_uq ON hhc_web.meeting(idempotency_key);
CREATE INDEX meeting_status_unit_idx ON hhc_web.meeting(status,church_unit_id);
CREATE INDEX meeting_status_venue_idx ON hhc_web.meeting(status,venue_resource_id);

CREATE TABLE hhc_web.meeting_occurrence_override (
  meeting_id uuid NOT NULL REFERENCES hhc_web.meeting(id),
  occurrence_date date NOT NULL,
  cancelled boolean NOT NULL DEFAULT false,
  starts_at timestamptz,
  duration_minutes integer CHECK (duration_minutes BETWEEN 1 AND 1440),
  venue_resource_id uuid REFERENCES hhc_web.resource(id),
  reason text NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by text NOT NULL,
  updated_by text NOT NULL,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (meeting_id, occurrence_date)
);

CREATE INDEX meeting_occurrence_override_date_idx ON hhc_web.meeting_occurrence_override(occurrence_date,meeting_id);

CREATE TABLE hhc_web.meeting_collection_binding (
  meeting_id uuid NOT NULL REFERENCES hhc_web.meeting(id),
  collection_id text NOT NULL CHECK (btrim(collection_id) <> '' AND length(collection_id) <= 200),
  enabled boolean NOT NULL DEFAULT true,
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (meeting_id, collection_id)
);

CREATE INDEX meeting_collection_binding_collection_idx ON hhc_web.meeting_collection_binding(collection_id,meeting_id);

CREATE TABLE hhc_web.operations_audit (
  id uuid PRIMARY KEY,
  resource_type text NOT NULL,
  resource_id uuid NOT NULL,
  action text NOT NULL,
  actor text NOT NULL,
  request_id text NOT NULL,
  before_json jsonb,
  after_json jsonb,
  created_at timestamptz NOT NULL
);

CREATE INDEX operations_audit_resource_idx ON hhc_web.operations_audit(resource_type,resource_id,created_at DESC);
