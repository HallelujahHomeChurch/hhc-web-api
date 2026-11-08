ALTER TABLE hhc_web.member_recording ADD COLUMN human_edit_generation bigint NOT NULL DEFAULT 0 CHECK(human_edit_generation>=0);

CREATE TABLE hhc_web.member_recording_capture (
 id uuid PRIMARY KEY,
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 actor_id uuid NOT NULL,
 operation_key text NOT NULL CHECK(length(operation_key) BETWEEN 1 AND 128),
 capture_id text UNIQUE CHECK(capture_id ~ '^[a-f0-9]{32}$'),
 active boolean NOT NULL,
 payload_json jsonb NOT NULL,
 checked_at timestamptz,
 UNIQUE(actor_id,operation_key)
);
CREATE UNIQUE INDEX member_recording_capture_active ON hhc_web.member_recording_capture(recording_id) WHERE active;
CREATE INDEX member_recording_capture_reconcile ON hhc_web.member_recording_capture(checked_at NULLS FIRST,id);
CREATE TABLE hhc_web.member_recording_capture_scope (
 capture_intent_id uuid NOT NULL REFERENCES hhc_web.member_recording_capture(id) ON DELETE CASCADE,
 scope_id uuid NOT NULL,
 user_id uuid NOT NULL,
 registered_at timestamptz NOT NULL,
 PRIMARY KEY(capture_intent_id,scope_id)
);
