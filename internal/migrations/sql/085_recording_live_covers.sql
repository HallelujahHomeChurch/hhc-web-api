CREATE TABLE hhc_web.member_recording_live_cover_default (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 payload_json jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO hhc_web.member_recording_live_cover_default(singleton,payload_json) VALUES(true,'{"Settings":{"revision":1,"selection":{"mode":"auto"},"coverState":"waiting"}}');
CREATE TABLE hhc_web.member_recording_live_cover_operation (
 id uuid PRIMARY KEY,
 target text NOT NULL,
 actor_id uuid NOT NULL,
 operation_key text NOT NULL,
 expected_revision bigint NOT NULL CHECK(expected_revision>0),
 selection_json jsonb NOT NULL,
 receipt_json jsonb,
 created_at timestamptz NOT NULL,
 UNIQUE(actor_id,operation_key)
);
CREATE TABLE hhc_web.member_recording_live_cover_reference (
 id uuid PRIMARY KEY,
 target text NOT NULL,
 cover_id text NOT NULL,
 scope text NOT NULL DEFAULT '',
 recording_id text NOT NULL DEFAULT '',
 wanted boolean NOT NULL DEFAULT true,
 applied boolean NOT NULL DEFAULT false,
 released_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(),
 checked_at timestamptz
);
CREATE INDEX member_recording_live_cover_reference_pending_idx ON hhc_web.member_recording_live_cover_reference(checked_at) WHERE released_at IS NULL;
