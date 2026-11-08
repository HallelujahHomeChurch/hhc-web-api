-- Unreleased reader migrations follow the main branch's recording migrations.
ALTER TABLE hhc_web.online_bulletin_extraction_job
  ADD COLUMN snapshot_json jsonb CHECK (jsonb_typeof(snapshot_json)='object' AND octet_length(snapshot_json::text)<=16777216),
  ADD COLUMN snapshot_sha256 text CHECK (snapshot_sha256 ~ '^[0-9a-f]{64}$'),
  ADD COLUMN result_revision bigint,
  ADD FOREIGN KEY (document_id,result_revision) REFERENCES hhc_web.online_bulletin_revision(document_id,revision),
  ADD CHECK ((snapshot_json IS NULL)=(snapshot_sha256 IS NULL));

CREATE FUNCTION hhc_web.reject_online_bulletin_snapshot_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.snapshot_json IS NOT NULL AND
    (NEW.snapshot_json IS DISTINCT FROM OLD.snapshot_json OR
     NEW.snapshot_sha256 IS DISTINCT FROM OLD.snapshot_sha256 OR
     NEW.result_revision IS DISTINCT FROM OLD.result_revision) THEN
    RAISE EXCEPTION 'online bulletin extraction snapshot is immutable' USING ERRCODE='23514';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER online_bulletin_snapshot_immutable BEFORE UPDATE ON hhc_web.online_bulletin_extraction_job
  FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_snapshot_update();
