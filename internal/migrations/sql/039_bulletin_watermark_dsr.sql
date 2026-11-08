CREATE TABLE hhc_web.bulletin_watermark_dsr_action (
 key_hash text PRIMARY KEY,
 subject_hash text NOT NULL,
 action text NOT NULL CHECK(action IN ('erase','restrict_processing')),
 record_count bigint NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX bulletin_watermark_dsr_action_expiry ON hhc_web.bulletin_watermark_dsr_action(created_at);
