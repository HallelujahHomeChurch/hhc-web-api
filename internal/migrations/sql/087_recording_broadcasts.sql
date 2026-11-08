-- Additive B1 state only: legacy recordings/captures are deliberately not backfilled.
ALTER TABLE hhc_web.member_recording ADD COLUMN is_broadcast boolean NOT NULL DEFAULT false;
ALTER TABLE hhc_web.member_recording_capture ADD COLUMN broadcast_epoch bigint CHECK(broadcast_epoch>0 AND broadcast_epoch<=9007199254740991);
CREATE UNIQUE INDEX member_recording_broadcast_active_capture ON hhc_web.member_recording_capture((true)) WHERE active AND broadcast_epoch IS NOT NULL;
CREATE TABLE hhc_web.member_recording_broadcast (
 recording_id uuid PRIMARY KEY REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 payload_json jsonb NOT NULL,
 created_at timestamptz NOT NULL,
 checked_at timestamptz
);
CREATE INDEX member_recording_broadcast_reconcile ON hhc_web.member_recording_broadcast(checked_at NULLS FIRST,recording_id);
CREATE TABLE hhc_web.member_recording_broadcast_create (
 actor_id uuid NOT NULL,
 operation_key text NOT NULL CHECK(length(operation_key) BETWEEN 1 AND 128),
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 digest text NOT NULL,
 PRIMARY KEY(actor_id,operation_key)
);
CREATE TABLE hhc_web.member_recording_broadcast_audit (
 id uuid PRIMARY KEY,
 recording_id uuid NOT NULL REFERENCES hhc_web.member_recording(id) ON DELETE CASCADE,
 actor_id uuid NOT NULL,
 request_id text NOT NULL,
 operation_key text NOT NULL,
 operation text NOT NULL,
 digest text NOT NULL,
 receipt_json jsonb NOT NULL,
 occurred_at timestamptz NOT NULL,
 UNIQUE(recording_id,operation_key)
);
