CREATE TABLE hhc_web.bulletin_watermark_investigation (
 id uuid PRIMARY KEY,
 actor_id uuid NOT NULL,
 issue_id uuid NOT NULL,
 locale text NOT NULL,
 revision bigint NOT NULL CHECK (revision>0),
 source_asset_id uuid NOT NULL,
 source_sha256 text NOT NULL CHECK(source_sha256 ~ '^[a-f0-9]{64}$'),
 renderer_version text NOT NULL,
 model_sha256 text NOT NULL CHECK(model_sha256 ~ '^[a-f0-9]{64}$'),
 page integer NOT NULL CHECK(page BETWEEN 1 AND 60),
 reason text NOT NULL CHECK(char_length(reason) BETWEEN 10 AND 300),
 evidence_sha256 text NOT NULL CHECK(evidence_sha256 ~ '^[a-f0-9]{64}$'),
 evidence bytea CHECK(octet_length(evidence) BETWEEN 1 AND 10485760),
 status text NOT NULL CHECK(status IN ('queued','running','completed','failed','expired')),
 result text CHECK(result IN ('matched','inconclusive')),
 receipt_id uuid REFERENCES hhc_web.bulletin_watermark_receipt(id) ON DELETE SET NULL,
 error_code text CHECK(error_code ~ '^[a-z_]{1,80}$'),
 candidate_set_hash text CHECK(candidate_set_hash ~ '^[a-f0-9]{64}$'),
 lease_token uuid,
 lease_until timestamptz,
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 2),
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 evidence_expires_at timestamptz NOT NULL,
 CHECK(expires_at-created_at=interval '8760 hours'),
 CHECK(evidence_expires_at-created_at=interval '24 hours'),
 CHECK((status IN ('queued','running') AND evidence IS NOT NULL) OR (status IN ('completed','failed','expired') AND evidence IS NULL)),
 CHECK((status='running' AND lease_token IS NOT NULL AND lease_until IS NOT NULL) OR (status<>'running' AND lease_token IS NULL AND lease_until IS NULL)),
 CHECK((status='completed' AND result IS NOT NULL AND candidate_set_hash IS NOT NULL) OR (status<>'completed' AND result IS NULL AND receipt_id IS NULL))
);
CREATE INDEX bulletin_watermark_investigation_claim ON hhc_web.bulletin_watermark_investigation(status,lease_until,created_at) WHERE status IN ('queued','running');
CREATE INDEX bulletin_watermark_investigation_actor ON hhc_web.bulletin_watermark_investigation(actor_id,status);
CREATE INDEX bulletin_watermark_investigation_receipt ON hhc_web.bulletin_watermark_investigation(receipt_id) WHERE receipt_id IS NOT NULL;
CREATE INDEX bulletin_watermark_investigation_expiry ON hhc_web.bulletin_watermark_investigation(expires_at);
