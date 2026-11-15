-- Admin-only extraction progress. No provider payloads or source text are copied.
ALTER TABLE hhc_web.online_bulletin_extraction_job
 ADD COLUMN production_revision bigint NOT NULL DEFAULT 1 CHECK (production_revision>0),
 ADD COLUMN production_tracking_started boolean NOT NULL DEFAULT true,
 ADD COLUMN production_attempt_id uuid,
 ADD COLUMN production_started_at timestamptz,
 ADD COLUMN production_last_progress_at timestamptz,
 ADD COLUMN production_heartbeat_at timestamptz,
 ADD COLUMN production_total_pages integer CHECK (production_total_pages BETWEEN 1 AND 40),
 ADD COLUMN production_current_page integer CHECK (production_current_page BETWEEN 1 AND 40),
 ADD COLUMN production_phase text NOT NULL DEFAULT 'scan' CHECK (production_phase IN ('scan','recognition','reconciliation')),
 ADD COLUMN production_subphase text NOT NULL DEFAULT '' CHECK (production_subphase IN ('','source','recognition','verified','checkpoint_reuse','snapshot_reuse')),
 ADD COLUMN production_protocol_sha256 text CHECK (production_protocol_sha256 ~ '^[0-9a-f]{64}$'),
 ADD COLUMN production_canonical jsonb CHECK (jsonb_typeof(production_canonical)='object'),
 ADD COLUMN production_reused_snapshot_sha256 text CHECK (production_reused_snapshot_sha256 ~ '^[0-9a-f]{64}$');

-- Existing jobs have no trustworthy first-start timestamp. Future inserts use
-- the default true; retrying an old job must not manufacture a new first start.
UPDATE hhc_web.online_bulletin_extraction_job SET production_tracking_started=false;

-- Same-row only: staff retries, attempt limits and terminal transitions all
-- advance snapshot ordering without touching another job or acquiring its lock.
CREATE FUNCTION hhc_web.advance_online_bulletin_production_revision() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 NEW.production_revision := OLD.production_revision + 1;
 -- Compatible old retry writers reset attempts without knowing progress fields.
 -- If the first start was never recorded, keep that history unknown forever.
 IF OLD.production_started_at IS NULL AND NEW.attempts < OLD.attempts THEN
  NEW.production_tracking_started := false;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER online_bulletin_production_revision BEFORE UPDATE ON hhc_web.online_bulletin_extraction_job
 FOR EACH ROW EXECUTE FUNCTION hhc_web.advance_online_bulletin_production_revision();

CREATE TABLE hhc_web.online_bulletin_production_page (
 job_id uuid NOT NULL REFERENCES hhc_web.online_bulletin_extraction_job(id) ON DELETE CASCADE,
 source_page integer NOT NULL CHECK (source_page BETWEEN 1 AND 40),
 input_sha256 text NOT NULL CHECK (input_sha256 ~ '^[0-9a-f]{64}$'),
 semantic_sha256 text NOT NULL CHECK (semantic_sha256 ~ '^[0-9a-f]{64}$'),
 result_sha256 text NOT NULL CHECK (result_sha256 ~ '^[0-9a-f]{64}$'),
 previous_sha256 text NOT NULL CHECK (previous_sha256='' OR previous_sha256 ~ '^[0-9a-f]{64}$'),
 next_sha256 text NOT NULL CHECK (next_sha256 ~ '^[0-9a-f]{64}$'),
 reused boolean NOT NULL,
 verified_at timestamptz NOT NULL,
 PRIMARY KEY(job_id,source_page)
);

-- At most forty receipts. The existing verified-checkpoint partial unique index
-- supplies the other side of the join; no speculative checkpoint index is added.
CREATE FUNCTION hhc_web.online_bulletin_production_prefix(job uuid, protocol text, deployment_name text, model text, canonical jsonb)
 RETURNS TABLE(verified_pages integer,reused_pages integer) LANGUAGE sql STABLE AS $$
 WITH RECURSIVE valid AS (
  SELECT p.* FROM hhc_web.online_bulletin_production_page p
  JOIN hhc_web.online_bulletin_recognition_checkpoint c ON c.job_id=p.job_id AND c.source_page=p.source_page AND c.phase='verified'
  WHERE p.job_id=job AND c.input_sha256=p.input_sha256 AND c.protocol_sha256=protocol AND c.deployment=deployment_name AND c.model_version=model
   AND c.checkpoint_json->'canonical'=canonical AND COALESCE(c.checkpoint_json->>'errorCode','')=''
   AND (
    (c.checkpoint_json ? 'result'
     AND encode(sha256(convert_to((c.checkpoint_json->'result')::text,'UTF8')),'hex')=p.result_sha256
     AND c.checkpoint_json#>>'{result,semanticSha256}'=p.semantic_sha256
     AND c.checkpoint_json#>>'{result,page}'=p.source_page::text
     AND c.checkpoint_json#>'{result,unassignedIds}'='[]'::jsonb)
    OR (NOT c.checkpoint_json ? 'result' AND EXISTS(
     SELECT 1 FROM hhc_web.online_bulletin_extraction_job finished,
      LATERAL jsonb_array_elements(finished.snapshot_json#>'{candidate,recognition,pages}') evidence
     WHERE finished.id=job AND finished.status='review_ready' AND finished.snapshot_sha256 IS NOT NULL
      AND finished.snapshot_json->'canonicalMetadata'=canonical
      AND finished.snapshot_json#>>'{candidate,recognition,protocolSha256}'=protocol
      AND finished.snapshot_json#>>'{candidate,recognition,deployment}'=deployment_name
      AND finished.snapshot_json#>>'{candidate,recognition,modelVersion}'=model
      AND finished.snapshot_json#>>'{candidate,recognition,sourceSha256}'=finished.source_asset_checksum
      AND evidence->>'page'=p.source_page::text AND evidence->>'inputSha256'=p.input_sha256
      AND evidence->>'semanticSha256'=p.semantic_sha256
    ))
   )
   AND (c.checkpoint_json#>>'{totalUsage,Requests}')::integer>=2
 ), prefix AS (
  SELECT * FROM valid WHERE source_page=1 AND previous_sha256=''
  UNION ALL SELECT v.* FROM valid v JOIN prefix p ON v.source_page=p.source_page+1 AND v.previous_sha256=p.next_sha256 WHERE v.source_page<=40
 ) SELECT COALESCE(MAX(source_page),0),COUNT(*) FILTER(WHERE reused)::integer FROM prefix
$$;
