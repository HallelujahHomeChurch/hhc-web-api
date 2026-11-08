ALTER TABLE hhc_web.member_recording ADD COLUMN selected_cover_id text,
 ADD COLUMN selected_cover_reference_id uuid;

CREATE TABLE hhc_web.member_recording_cover_operation (
 id uuid PRIMARY KEY,
 actor_type text NOT NULL CHECK(actor_type IN ('user','service')),
 actor_id text NOT NULL,
 credential_id text NOT NULL DEFAULT '',
 request_id text NOT NULL DEFAULT '',
 operation_key text NOT NULL CHECK(length(operation_key) BETWEEN 1 AND 128),
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 package_id text NOT NULL,
 cover_id text NOT NULL,
 expected_version bigint NOT NULL CHECK(expected_version>0),
 selection_json jsonb NOT NULL,
 selected_version bigint CHECK(selected_version>expected_version),
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','selected','releasing','released')),
 created_at timestamptz NOT NULL,
 checked_at timestamptz,
 UNIQUE(actor_type,actor_id,operation_key)
);
CREATE INDEX member_recording_cover_cleanup ON hhc_web.member_recording_cover_operation(state,checked_at,created_at);
