CREATE TABLE hhc_web.bulletin_watermark_receipt (
 id uuid PRIMARY KEY,
 trace_code text NOT NULL UNIQUE CHECK (length(trace_code) IN (18,28)),
 user_id uuid NOT NULL,
 issue_id uuid NOT NULL,
 locale text NOT NULL,
 revision bigint NOT NULL CHECK (revision>0),
 source_asset_id uuid NOT NULL,
 source_sha256 text NOT NULL CHECK (source_sha256 ~ '^[a-f0-9]{64}$'),
 output_sha256 text NOT NULL CHECK (output_sha256 ~ '^[a-f0-9]{64}$'),
 renderer_version text NOT NULL,
 privacy_notice_version text NOT NULL,
 issued_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 CHECK (expires_at - issued_at = interval '8760 hours')
);
CREATE INDEX bulletin_watermark_receipt_expiry ON hhc_web.bulletin_watermark_receipt(expires_at);
CREATE INDEX bulletin_watermark_receipt_subject ON hhc_web.bulletin_watermark_receipt(user_id);
-- Hashes fence in-flight issuance without retaining a plain Account ID tombstone.
CREATE TABLE hhc_web.bulletin_watermark_subject (
 subject_hash text PRIMARY KEY,
 erased boolean NOT NULL DEFAULT false,
 updated_at timestamptz NOT NULL DEFAULT now()
);
