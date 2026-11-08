-- Unreleased reader migrations follow the main branch's recording migrations.
CREATE TABLE hhc_web.online_bulletin_reader_receipt (
  id uuid PRIMARY KEY,
  account_id uuid NOT NULL,
  document_id uuid NOT NULL,
  revision bigint NOT NULL,
  trace_code text NOT NULL UNIQUE CHECK (trace_code ~ '^[0-9A-HJKMNP-TV-Z]{18}$'),
  privacy_notice_version text NOT NULL CHECK (length(privacy_notice_version) BETWEEN 1 AND 200),
  issued_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL CHECK (expires_at=issued_at+interval '8760 hours'),
  UNIQUE(id,account_id),
  FOREIGN KEY(document_id,revision) REFERENCES hhc_web.online_bulletin_publication(document_id,revision) ON DELETE RESTRICT
);
CREATE INDEX online_bulletin_reader_receipt_account_idx ON hhc_web.online_bulletin_reader_receipt(account_id,id);
CREATE INDEX online_bulletin_reader_receipt_expiry_idx ON hhc_web.online_bulletin_reader_receipt(expires_at);

-- A retried request returns its original validation time, even if a different
-- request has subsequently revalidated the same browser-session receipt.
CREATE TABLE hhc_web.online_bulletin_reader_request (
  account_id uuid NOT NULL,
  client_request_id uuid NOT NULL,
  receipt_id uuid NOT NULL,
  request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
  validated_at timestamptz NOT NULL,
  offline_valid_until timestamptz NOT NULL CHECK (offline_valid_until=validated_at+interval '168 hours'),
  PRIMARY KEY(account_id,client_request_id),
  FOREIGN KEY(receipt_id,account_id) REFERENCES hhc_web.online_bulletin_reader_receipt(id,account_id) ON DELETE CASCADE
);

-- Exact trace lookup is unambiguous across the two isolated evidence channels.
CREATE FUNCTION hhc_web.check_bulletin_trace_code() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended(NEW.trace_code,20261002));
  IF (TG_TABLE_NAME='online_bulletin_reader_receipt' AND EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_receipt WHERE trace_code=NEW.trace_code))
    OR (TG_TABLE_NAME='bulletin_watermark_receipt' AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_reader_receipt WHERE trace_code=NEW.trace_code)) THEN
    RAISE EXCEPTION 'trace code collision' USING ERRCODE='23505',CONSTRAINT='bulletin_trace_code_unique';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER online_reader_trace_code_guard BEFORE INSERT OR UPDATE OF trace_code ON hhc_web.online_bulletin_reader_receipt FOR EACH ROW EXECUTE FUNCTION hhc_web.check_bulletin_trace_code();
CREATE TRIGGER pdf_trace_code_guard BEFORE INSERT OR UPDATE OF trace_code ON hhc_web.bulletin_watermark_receipt FOR EACH ROW EXECUTE FUNCTION hhc_web.check_bulletin_trace_code();
