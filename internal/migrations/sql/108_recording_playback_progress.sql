CREATE TABLE hhc_web.member_recording_progress (
  account_id uuid NOT NULL,
  recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
  media_version text NOT NULL,
  mode text NOT NULL CHECK (mode IN ('vod', 'live')),
  capture_id text,
  duration_seconds double precision,
  position_seconds double precision NOT NULL CHECK (position_seconds >= 0 AND position_seconds < 'Infinity'::double precision),
  completed boolean NOT NULL DEFAULT false,
  owner_session_id uuid NOT NULL,
  owner_generation uuid NOT NULL,
  claim_id uuid NOT NULL,
  last_sequence bigint NOT NULL CHECK (last_sequence BETWEEN 0 AND 9007199254740991),
  claimed_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (account_id, recording_id),
  CHECK (
    (mode = 'vod' AND capture_id IS NULL AND duration_seconds IS NOT NULL AND duration_seconds > 0 AND duration_seconds < 'Infinity'::double precision)
    OR (mode = 'live' AND capture_id IS NOT NULL AND duration_seconds IS NULL)
  )
);
CREATE INDEX member_recording_progress_recent_idx ON hhc_web.member_recording_progress (account_id, claimed_at DESC, recording_id) WHERE NOT completed;
CREATE INDEX member_recording_progress_expiry_idx ON hhc_web.member_recording_progress (expires_at);
CREATE INDEX member_recording_progress_recording_idx ON hhc_web.member_recording_progress (recording_id);
