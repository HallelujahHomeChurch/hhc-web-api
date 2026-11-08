-- Confirmed source-backed examples and append-only rule history stay private.
CREATE TABLE hhc_web.online_bulletin_learning_rule (
  signature text PRIMARY KEY CHECK (signature ~ '^[0-9a-f]{64}$'),
  version bigint NOT NULL CHECK (version > 0),
  status text NOT NULL CHECK (status IN ('pending','active','conflicted','disabled')),
  kind text NOT NULL CHECK (kind IN ('sermon','testimony','teaching','reflection')),
  title text NOT NULL CHECK (length(title) BETWEEN 1 AND 512),
  speaker text NOT NULL CHECK (length(speaker) BETWEEN 1 AND 256)
);
CREATE TABLE hhc_web.online_bulletin_learning_rule_revision (
  signature text NOT NULL REFERENCES hhc_web.online_bulletin_learning_rule(signature),
  version bigint NOT NULL CHECK (version > 0),
  status text NOT NULL CHECK (status IN ('pending','active','conflicted','disabled')),
  kind text NOT NULL CHECK (kind IN ('sermon','testimony','teaching','reflection')),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY(signature,version)
);
CREATE TRIGGER online_bulletin_learning_rule_revision_immutable BEFORE UPDATE ON hhc_web.online_bulletin_learning_rule_revision
  FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();
CREATE TABLE hhc_web.online_bulletin_learning_example (
  signature text NOT NULL REFERENCES hhc_web.online_bulletin_learning_rule(signature),
  issue_id uuid NOT NULL REFERENCES hhc_web.bulletin_issue(id),
  document_id uuid NOT NULL,
  revision bigint NOT NULL,
  extracted_revision bigint NOT NULL,
  extraction_job_id uuid NOT NULL REFERENCES hhc_web.online_bulletin_extraction_job(id),
  component_id text NOT NULL CHECK (length(component_id) BETWEEN 1 AND 128),
  source_page integer NOT NULL CHECK (source_page BETWEEN 2 AND 39),
  source_checksum text NOT NULL CHECK (source_checksum ~ '^[0-9a-f]{64}$'),
  layout_validation_hash text NOT NULL CHECK (layout_validation_hash ~ '^[0-9a-f]{64}$'),
  kind text NOT NULL CHECK (kind IN ('sermon','testimony','teaching','reflection')),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY(document_id,revision,component_id),
  FOREIGN KEY(document_id,revision) REFERENCES hhc_web.online_bulletin_confirmation(document_id,revision),
  FOREIGN KEY(document_id,extracted_revision) REFERENCES hhc_web.online_bulletin_revision(document_id,revision)
);
CREATE INDEX online_bulletin_learning_example_signature ON hhc_web.online_bulletin_learning_example(signature,issue_id);
CREATE TRIGGER online_bulletin_learning_example_immutable BEFORE UPDATE ON hhc_web.online_bulletin_learning_example
  FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();
ALTER TABLE hhc_web.online_bulletin_extraction_job ADD COLUMN learning_rules_json jsonb
  CHECK (learning_rules_json IS NULL OR (jsonb_typeof(learning_rules_json)='array' AND jsonb_array_length(learning_rules_json)<=4096 AND octet_length(learning_rules_json::text)<=1048576));
CREATE FUNCTION hhc_web.reject_online_bulletin_learning_input_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.learning_rules_json IS DISTINCT FROM OLD.learning_rules_json THEN
    RAISE EXCEPTION 'queued learning rules are immutable';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER online_bulletin_learning_input_immutable BEFORE UPDATE ON hhc_web.online_bulletin_extraction_job
  FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_learning_input_update();
