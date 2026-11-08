-- Output identity must not follow a later binding on the same recording.
ALTER TABLE hhc_web.member_recording_broadcast_output
 ADD COLUMN capture_intent_id uuid,
 ADD COLUMN epoch bigint;
UPDATE hhc_web.member_recording_broadcast_output o
 SET capture_intent_id=(b.payload_json->>'CaptureIntentID')::uuid,
     epoch=(b.payload_json->>'Epoch')::bigint
 FROM hhc_web.member_recording_broadcast b WHERE b.recording_id=o.recording_id;
-- Missing/deleted bindings remain terminal tombstones, never new admissions.
