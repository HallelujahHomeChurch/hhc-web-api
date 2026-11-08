-- Unreleased reader migrations follow the main branch's recording migrations.
CREATE TABLE hhc_web.online_bulletin_confirmation (
  document_id uuid NOT NULL,
  revision bigint NOT NULL,
  canonical_metadata jsonb NOT NULL CHECK (jsonb_typeof(canonical_metadata)='object' AND octet_length(canonical_metadata::text)<=65536),
  layout_validation_hash text NOT NULL CHECK (layout_validation_hash ~ '^[0-9a-f]{64}$'),
  page_count integer NOT NULL CHECK (page_count BETWEEN 1 AND 80),
  accepted_issues jsonb NOT NULL CHECK (jsonb_typeof(accepted_issues)='array' AND jsonb_array_length(accepted_issues)<=4096),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (document_id,revision),
  FOREIGN KEY (document_id,revision) REFERENCES hhc_web.online_bulletin_revision(document_id,revision) ON DELETE RESTRICT
);
CREATE TRIGGER online_bulletin_confirmation_immutable BEFORE UPDATE ON hhc_web.online_bulletin_confirmation
  FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();
