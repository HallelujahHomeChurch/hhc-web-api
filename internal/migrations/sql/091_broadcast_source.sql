-- The derived column also covers old workers that only update payload_json/active.
-- Incomplete historical seal evidence conservatively keeps the input occupied.
CREATE FUNCTION hhc_web.capture_input_active(is_active boolean, payload jsonb, epoch bigint)
RETURNS boolean LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT is_active AND NOT COALESCE(
  epoch>0 AND payload->>'NormalEnd'='true'
  AND payload->'Status'->>'stopAcceptedAt' IS NOT NULL
  AND jsonb_typeof(payload->'SealedEndSequence')='number'
  AND CASE WHEN jsonb_typeof(payload->'SealedEndSequence')='number' THEN (payload->>'SealedEndSequence')::numeric>0 ELSE false END
  AND payload->'Status'->>'state' IN ('freezing','validating')
  AND EXISTS (
   SELECT 1 FROM jsonb_each(CASE WHEN jsonb_typeof(payload->'Operations')='object' THEN payload->'Operations' ELSE '{}'::jsonb END) op
   WHERE op.value->>'Operation'='seal' AND op.value->>'Pending'='false'
    AND op.value->'Receipt'->>'operation'='seal'
    AND op.value->'Receipt'->>'operationKey'=op.key
    AND op.value->'Receipt'->>'acceptedAt' IS NOT NULL
    AND NULLIF(op.value->'Receipt'->>'captureId','')=NULLIF(payload->'Status'->>'captureId','')
  ),false)
$$;
ALTER TABLE hhc_web.member_recording_capture ADD COLUMN input_active boolean GENERATED ALWAYS AS (hhc_web.capture_input_active(active,payload_json,broadcast_epoch)) STORED;
CREATE UNIQUE INDEX member_recording_broadcast_input_capture ON hhc_web.member_recording_capture((true)) WHERE input_active AND broadcast_epoch IS NOT NULL;
DROP INDEX hhc_web.member_recording_broadcast_active_capture;
ALTER TABLE hhc_web.member_recording_broadcast ADD COLUMN source_managed boolean NOT NULL DEFAULT false;
CREATE TABLE hhc_web.member_recording_broadcast_source (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision BETWEEN 1 AND 9007199254740991),
 key_hash text NOT NULL DEFAULT '',
 suffix text,
 target_recording_id uuid REFERENCES hhc_web.member_recording(id) ON DELETE SET NULL
);
INSERT INTO hhc_web.member_recording_broadcast_source(singleton) VALUES(true);
-- No cascade: an old operation can never become a fresh bind after event cleanup.
CREATE TABLE hhc_web.member_recording_broadcast_source_operation (
 operation_key text PRIMARY KEY CHECK(length(operation_key) BETWEEN 1 AND 128),
 actor_id uuid NOT NULL,
 request_id text NOT NULL,
 operation text NOT NULL,
 digest text NOT NULL,
 source_revision bigint NOT NULL,
 recording_id uuid,
 capture_intent_id uuid,
 epoch bigint,
 occurred_at timestamptz NOT NULL
);
