ALTER TABLE hhc_web.member_recording_broadcast_source
 ADD COLUMN direct_actor_id uuid,
 ADD COLUMN direct_recording_id uuid,
 ADD COLUMN prepared boolean NOT NULL DEFAULT false,
 ADD COLUMN deleted_at timestamptz,
 ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
-- The input cap is an admission policy, separate from source identity.
CREATE UNIQUE INDEX member_recording_prepared_source ON hhc_web.member_recording_broadcast_source((true)) WHERE prepared;
ALTER TABLE hhc_web.member_recording_broadcast
 ADD COLUMN studio_mode text NOT NULL DEFAULT 'legacy' CHECK(studio_mode IN ('legacy','direct','scheduled'));
-- Tombstones intentionally survive recording cleanup and key rotation.
CREATE TABLE hhc_web.member_recording_broadcast_output (
 source_id uuid NOT NULL REFERENCES hhc_web.member_recording_broadcast_source(id),
 output_id uuid NOT NULL,
 key_generation bigint NOT NULL,
 recording_id uuid NOT NULL,
 actor_id uuid NOT NULL,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(source_id,output_id)
);
CREATE TABLE hhc_web.member_recording_studio_operation (
 operation_key text PRIMARY KEY CHECK(length(operation_key) BETWEEN 1 AND 128),
 source_id uuid NOT NULL,
 actor_id uuid NOT NULL,
 digest text NOT NULL,
 recording_id uuid,
 created_at timestamptz NOT NULL
);
