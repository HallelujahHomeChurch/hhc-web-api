CREATE TABLE hhc_web.member_recording_publish_receipt (
  actor_type text NOT NULL CHECK (actor_type IN ('user','service')),
  actor_id text NOT NULL CHECK (length(actor_id) BETWEEN 1 AND 128),
  operation_key text NOT NULL CHECK (operation_key ~ '^[a-zA-Z0-9._:-]{1,128}$'),
  recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
  asset_version_id text NOT NULL,
  expected_version bigint NOT NULL CHECK (expected_version > 0),
  published_version bigint NOT NULL CHECK (published_version = expected_version + 1),
  published_at timestamptz NOT NULL,
  request_fingerprint text NOT NULL CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
  PRIMARY KEY (actor_type, actor_id, operation_key)
);

CREATE INDEX member_recording_publish_receipt_recording_idx
  ON hhc_web.member_recording_publish_receipt(recording_id);
