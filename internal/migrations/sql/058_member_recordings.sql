CREATE TABLE hhc_web.member_recording (
  id uuid PRIMARY KEY,
  title_override text NOT NULL DEFAULT '',
  create_title text NOT NULL,
  uploaded_at timestamptz,
  expires_at timestamptz,
  draft_expires_at timestamptz NOT NULL,
  featured boolean NOT NULL DEFAULT false,
  hidden boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','published')),
  primary_asset_version_id text,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  idempotency_key text NOT NULL UNIQUE,
  created_by text NOT NULL,
  updated_by text NOT NULL,
  published_at timestamptz,
  completion_checked_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  CONSTRAINT member_recording_completion_check CHECK (
    (uploaded_at IS NULL AND expires_at IS NULL AND status='draft') OR
    (uploaded_at IS NOT NULL AND expires_at IS NOT NULL AND expires_at=uploaded_at+interval '720 hours')
  )
);

CREATE INDEX member_recording_visible_idx
  ON hhc_web.member_recording(status, featured DESC, uploaded_at DESC)
  WHERE status='published' AND hidden=false;
CREATE INDEX member_recording_completion_queue_idx
  ON hhc_web.member_recording(completion_checked_at NULLS FIRST,id)
  WHERE uploaded_at IS NULL;

CREATE TABLE hhc_web.member_recording_file (
  id uuid PRIMARY KEY,
  recording_id uuid NOT NULL UNIQUE REFERENCES hhc_web.member_recording(id),
  asset_version_id text NOT NULL UNIQUE,
  file_name text NOT NULL,
  purge_state text NOT NULL DEFAULT 'active'
    CHECK (purge_state IN ('active','deleting','deleted')),
  created_at timestamptz NOT NULL,
  UNIQUE(recording_id, asset_version_id)
);

CREATE INDEX member_recording_file_candidate_purge_idx
  ON hhc_web.member_recording_file(created_at)
  WHERE purge_state='active';

ALTER TABLE hhc_web.member_recording
  ADD CONSTRAINT member_recording_primary_file_fk
  FOREIGN KEY (id, primary_asset_version_id)
  REFERENCES hhc_web.member_recording_file(recording_id, asset_version_id);
