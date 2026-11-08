CREATE TABLE hhc_web.bulletin_download_job (
  id uuid PRIMARY KEY,
  idempotency_key uuid NOT NULL,
  account_id uuid NOT NULL,
  issue_id uuid NOT NULL REFERENCES hhc_web.bulletin_issue(id) ON DELETE CASCADE,
  issue_date date NOT NULL,
  locale text NOT NULL CHECK (locale IN ('zh-Hant','zh-Hans','en')),
  revision bigint NOT NULL CHECK (revision > 0),
  source_asset_id text NOT NULL CHECK (source_asset_id ~ '^[0-9a-f]{32}$'),
  source_sha256 text NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
  filename text NOT NULL,
  renderer_version text NOT NULL,
  fingerprint_payload text NOT NULL CHECK (fingerprint_payload ~ '^[01]{61}$'),
  trace_code text NOT NULL,
  status text NOT NULL CHECK (status IN ('queued','running','ready','failed','expired')),
  generation integer NOT NULL DEFAULT 1 CHECK (generation > 0),
  derived_asset_id text CHECK (derived_asset_id IS NULL OR derived_asset_id ~ '^[0-9a-f]{32}$'),
  receipt_id uuid REFERENCES hhc_web.bulletin_watermark_receipt(id) ON DELETE SET NULL,
  error_code text,
  lease_token uuid,
  lease_until timestamptz,
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  cache_expires_at timestamptz,
  created_at timestamptz NOT NULL,
  updated_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  UNIQUE (account_id,idempotency_key),
  UNIQUE (account_id,source_asset_id,renderer_version),
  CHECK ((status='ready') = (derived_asset_id IS NOT NULL AND receipt_id IS NOT NULL AND cache_expires_at IS NOT NULL))
);

CREATE INDEX bulletin_download_job_owner ON hhc_web.bulletin_download_job(account_id,created_at DESC);
CREATE INDEX bulletin_download_job_lease ON hhc_web.bulletin_download_job(status,lease_until,created_at) WHERE status IN ('queued','running');

UPDATE hhc_web.bulletin_watermark_investigation
SET status='queued',lease_token=NULL,lease_until=NULL
WHERE status='running' AND evidence_expires_at>now();

INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
SELECT gen_random_uuid(),'bulletin-watermark-queue','bulletin.watermark.enqueue','bulletin_watermark',id,
       jsonb_build_object('kind','investigation','id',id::text),
       'bulletin-watermark:investigation:'||id::text||':1','pending',now(),now(),now()
FROM hhc_web.bulletin_watermark_investigation
WHERE status='queued' AND evidence_expires_at>now()
ON CONFLICT(destination,idempotency_key) DO NOTHING;
