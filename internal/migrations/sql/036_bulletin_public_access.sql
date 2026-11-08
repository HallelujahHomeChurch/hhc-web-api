CREATE TABLE hhc_web.bulletin_access (
 id boolean PRIMARY KEY DEFAULT true CHECK(id),
 status text NOT NULL DEFAULT 'enabled' CHECK(status IN ('enabled','disabled','enabling','disabling','failed')),
 requested_enabled boolean NOT NULL DEFAULT true,
 version bigint NOT NULL DEFAULT 1,
 operation_id uuid,
 error_code text,
 updated_by text NOT NULL DEFAULT 'migration',
 updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO hhc_web.bulletin_access(id) VALUES(true);

-- Durable remote-call intent: a crash after CreateGrant must remain recoverable.
CREATE TABLE hhc_web.bulletin_grant_intent (
 key text PRIMARY KEY,
 asset_id text NOT NULL,
 issue_id uuid NOT NULL,
 locale text NOT NULL,
 grant_id text NOT NULL DEFAULT '',
 delete_asset boolean NOT NULL DEFAULT false,
 revoked boolean NOT NULL DEFAULT false
);
INSERT INTO hhc_web.bulletin_grant_intent(key,asset_id,issue_id,locale)
 SELECT 'bulletin:'||aggregate_id||':'||(payload_json->>'locale')||':v'||aggregate_version,
 payload_json->>'assetId',aggregate_id,payload_json->>'locale'
 FROM hhc_web.outbox_event WHERE event_type='bulletin.publish.ensure_asset'
 AND (attempts>0 OR status='delivered') AND payload_json->>'assetId' IS NOT NULL
 ON CONFLICT DO NOTHING;

CREATE TABLE hhc_web.bulletin_access_item (
 operation_id uuid NOT NULL,
 key text NOT NULL REFERENCES hhc_web.bulletin_grant_intent(key),
 completed boolean NOT NULL DEFAULT false,
 PRIMARY KEY(operation_id,key)
);
CREATE TABLE hhc_web.bulletin_access_request (
 actor text NOT NULL,
 idempotency_key text NOT NULL,
 request text NOT NULL,
 response jsonb NOT NULL,
 before_state jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(actor,idempotency_key)
);
-- This is the audit history as well as the idempotent request receipt.

-- Additive suppression marker leaves the existing notification constraint intact.
-- Older workers still see pending, so reopening cannot make these notices eligible again.
ALTER TABLE hhc_web.bulletin_issue ADD COLUMN notification_suppressed boolean NOT NULL DEFAULT false;
