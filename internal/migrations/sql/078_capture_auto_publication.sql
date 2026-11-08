-- Retry/check timestamps and verified readiness live in the durable capture
-- aggregate. Index only pending work; preserve all immutable terminal receipts.
CREATE INDEX member_recording_capture_auto_publication ON hhc_web.member_recording_capture(id)
 WHERE payload_json->'Status'->>'autoPublish'='pending' AND payload_json->'Status'->>'state'='ready';
