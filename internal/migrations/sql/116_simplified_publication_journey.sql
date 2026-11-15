-- Additive source-bound bookkeeping. Historical operations are not backfilled.
ALTER TABLE hhc_web.outbox_event ADD COLUMN simplified_claim_token uuid;
CREATE TABLE hhc_web.online_bulletin_simplified_operation (
 operation_id uuid PRIMARY KEY,
 source_document_id uuid NOT NULL,
 source_revision bigint NOT NULL CHECK(source_revision>0),
 source_publication_version bigint CHECK(source_publication_version>0),
 source_content_hash text,
 source_metadata_hash text,
 coverage text NOT NULL CHECK(coverage IN('complete','partial')),
 failure_count bigint NOT NULL DEFAULT 0 CHECK(failure_count>=0),
 history_count bigint NOT NULL DEFAULT 0 CHECK(history_count>=0),
 last_failure_json jsonb,
 failures_json jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(failures_json)='array' AND jsonb_array_length(failures_json)<=20)
);
CREATE TABLE hhc_web.online_bulletin_simplified_authorization (
 id uuid PRIMARY KEY,
 operation_id uuid NOT NULL,
 receipt_id uuid NOT NULL,
 actor_id text NOT NULL,
 request_id text NOT NULL,
 created_at timestamptz NOT NULL
);
CREATE TRIGGER simplified_authorization_immutable BEFORE UPDATE OR DELETE ON hhc_web.online_bulletin_simplified_authorization FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();
CREATE FUNCTION hhc_web.track_simplified_publication() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE code text; kind text; phase text; item jsonb;
BEGIN
 IF NEW.event_type <> 'bulletin.online.publish_simplified' THEN RETURN NEW; END IF;
 IF TG_OP='INSERT' THEN
  INSERT INTO hhc_web.online_bulletin_simplified_operation(operation_id,source_document_id,source_revision,source_publication_version,source_content_hash,source_metadata_hash,coverage)
  SELECT NEW.id,NEW.aggregate_id,NEW.aggregate_version,
   CASE WHEN NEW.payload_json->>'sourcePublicationVersion' ~ '^[1-9][0-9]{0,17}$'
    AND NEW.idempotency_key='online-bulletin:'||NEW.aggregate_id::text||':simplified-release:'||(NEW.payload_json->>'sourcePublicationVersion')
    THEN (NEW.payload_json->>'sourcePublicationVersion')::bigint END,r.content_hash,
   CASE WHEN NEW.payload_json ? 'sourceMetadata' THEN encode(sha256(convert_to((NEW.payload_json->'sourceMetadata')::text,'UTF8')),'hex') END,'complete'
  FROM (SELECT 1) seed LEFT JOIN hhc_web.online_bulletin_revision r ON r.document_id=NEW.aggregate_id AND r.revision=NEW.aggregate_version;
  RETURN NEW;
 END IF;
 -- Normal healthy waits decrement the retry budget and do not add a failure.
 IF COALESCE(NEW.last_error,'')='' OR NOT (
  (NEW.status='failed' AND (OLD.status IS DISTINCT FROM NEW.status OR OLD.last_error IS DISTINCT FROM NEW.last_error)) OR
  (OLD.status='processing' AND NEW.status='pending' AND NEW.claimed_until IS NULL AND NEW.attempts>=OLD.attempts)
 ) THEN RETURN NEW; END IF;
 code:=CASE WHEN NEW.last_error IN('simplified_publication_failed','simplified_conversion_failed','simplified_layout_failed','simplified_target_changed','simplified_review_required','simplified_mapping_review_required','source_publication_changed','publication_withdrawn','stale_metadata','publication_authority_superseded') THEN NEW.last_error ELSE 'simplified_publication_failed' END;
 kind:=CASE code WHEN 'publication_withdrawn' THEN 'cancelled' WHEN 'source_publication_changed' THEN 'superseded' WHEN 'publication_authority_superseded' THEN 'superseded' ELSE 'failure' END;
 phase:=CASE code WHEN 'simplified_conversion_failed' THEN 'conversion' WHEN 'simplified_layout_failed' THEN 'layout' WHEN 'simplified_review_required' THEN 'assessment' WHEN 'source_publication_changed' THEN 'unknown' WHEN 'publication_withdrawn' THEN 'unknown' ELSE 'publication' END;
 item:=jsonb_build_object('code',code,'kind',kind,'phase',phase,'attempts',NEW.attempts,'at',NEW.updated_at);
 INSERT INTO hhc_web.online_bulletin_simplified_operation(operation_id,source_document_id,source_revision,coverage)
 VALUES(NEW.id,NEW.aggregate_id,NEW.aggregate_version,'partial') ON CONFLICT DO NOTHING;
 UPDATE hhc_web.online_bulletin_simplified_operation SET failure_count=failure_count+CASE WHEN kind='failure' THEN 1 ELSE 0 END,history_count=history_count+1,last_failure_json=CASE WHEN kind='failure' THEN item ELSE last_failure_json END,
 failures_json=(SELECT jsonb_agg(value ORDER BY ord) FROM jsonb_array_elements(jsonb_build_array(item)||failures_json) WITH ORDINALITY a(value,ord) WHERE ord<=20)
 WHERE operation_id=NEW.id;
 RETURN NEW;
END $$;
CREATE TRIGGER simplified_publication_tracking AFTER INSERT OR UPDATE ON hhc_web.outbox_event FOR EACH ROW EXECUTE FUNCTION hhc_web.track_simplified_publication();
