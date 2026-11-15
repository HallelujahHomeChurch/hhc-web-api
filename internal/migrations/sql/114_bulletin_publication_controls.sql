-- Additive only. No historical authorization or cancellation is inferred.
CREATE TABLE hhc_web.online_bulletin_publication_intent (
 id uuid PRIMARY KEY,
 issue_id uuid NOT NULL,
 series text NOT NULL CHECK(series='general'),
 locale text NOT NULL CHECK(locale='zh-Hant'),
 epoch bigint NOT NULL CHECK(epoch>0),
 canonical_version bigint NOT NULL CHECK(canonical_version>0),
 source_asset_id text NOT NULL,
 source_checksum text NOT NULL CHECK(source_checksum='' OR source_checksum ~ '^[0-9a-f]{64}$'),
 actor_id text NOT NULL,
 request_id text NOT NULL,
 supersedes_id uuid REFERENCES hhc_web.online_bulletin_publication_intent(id),
 created_at timestamptz NOT NULL,
 UNIQUE(issue_id,series,locale,epoch)
);
CREATE TABLE hhc_web.online_bulletin_intent_transition (
 intent_id uuid NOT NULL REFERENCES hhc_web.online_bulletin_publication_intent(id),
 sequence bigint NOT NULL CHECK(sequence>0),
 status text NOT NULL CHECK(status IN('authorized','running','completed','cancelled','superseded','failed')),
 reason_code text NOT NULL CHECK(reason_code IN('automatic_authorized','automatic_running','automatic_published','pdf_only','source_replaced','canonical_edited','draft_edited','explicit_draft','unpublished','manual_published','recovery_authorized','recovery_started','recovery_published','processing_failed','manual_state_change')),
 actor_id text NOT NULL,
 request_id text NOT NULL,
 canonical_version bigint NOT NULL CHECK(canonical_version>0),
 document_id uuid,
 revision bigint CHECK(revision>0),
 receipt_id uuid,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(intent_id,sequence)
);
CREATE TABLE hhc_web.online_bulletin_recovery_receipt (
 id uuid PRIMARY KEY,
 issue_id uuid NOT NULL,
 actor_id text NOT NULL,
 operation_key text NOT NULL CHECK(octet_length(operation_key) BETWEEN 1 AND 512),
 input_json jsonb NOT NULL CHECK(jsonb_typeof(input_json)='object' AND octet_length(input_json::text)<=4096),
 result_json jsonb NOT NULL CHECK(jsonb_typeof(result_json)='object' AND octet_length(result_json::text)<=8192),
 request_id text NOT NULL,
 created_at timestamptz NOT NULL,
 UNIQUE(actor_id,operation_key)
);
CREATE TRIGGER bulletin_publication_intent_immutable BEFORE UPDATE OR DELETE ON hhc_web.online_bulletin_publication_intent FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();
CREATE TRIGGER bulletin_intent_transition_immutable BEFORE UPDATE OR DELETE ON hhc_web.online_bulletin_intent_transition FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();
CREATE TRIGGER bulletin_recovery_receipt_immutable BEFORE UPDATE OR DELETE ON hhc_web.online_bulletin_recovery_receipt FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();
ALTER TABLE hhc_web.bulletin_version ADD COLUMN online_intent_id uuid REFERENCES hhc_web.online_bulletin_publication_intent(id), ADD COLUMN online_intent_epoch bigint NOT NULL DEFAULT 0 CHECK(online_intent_epoch>=0);
ALTER TABLE hhc_web.online_bulletin_extraction_job ADD COLUMN publication_intent_id uuid REFERENCES hhc_web.online_bulletin_publication_intent(id), ADD COLUMN publication_intent_epoch bigint CHECK(publication_intent_epoch>0);
-- Receipts/transitions deliberately survive source deletion. Retention and any
-- future erasure are separate reviewed operations; no legacy history is backfilled.
