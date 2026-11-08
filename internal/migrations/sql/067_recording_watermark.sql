CREATE TABLE hhc_web.recording_watermark_receipt (
 id uuid PRIMARY KEY,
 trace_code text NOT NULL UNIQUE CHECK(trace_code ~ '^[0-9A-HJKMNP-TV-Z]{10}$'),
 user_id uuid NOT NULL,
 recording_id uuid NOT NULL,
 package_id text NOT NULL CHECK(package_id ~ '^[a-f0-9]{32}$'),
 format_version integer NOT NULL DEFAULT 1 CHECK(format_version=1),
 issued_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK(expires_at=issued_at+interval '8760 hours'),
 UNIQUE(user_id,recording_id)
);
CREATE INDEX recording_watermark_expiry ON hhc_web.recording_watermark_receipt(expires_at,id);
CREATE INDEX recording_watermark_export ON hhc_web.recording_watermark_receipt(user_id,id);
CREATE TABLE hhc_web.recording_watermark_lookup_rate (
 subject_hash text NOT NULL,
 window_start timestamptz NOT NULL,
 count integer NOT NULL CHECK(count>0),
 PRIMARY KEY(subject_hash,window_start)
);
