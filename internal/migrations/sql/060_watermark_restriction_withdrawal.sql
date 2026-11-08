ALTER TABLE hhc_web.bulletin_watermark_subject ADD COLUMN origin_tracking_complete boolean NOT NULL DEFAULT false;
ALTER TABLE hhc_web.bulletin_watermark_dsr_action ADD COLUMN request_id uuid;
ALTER TABLE hhc_web.bulletin_watermark_dsr_action ADD COLUMN withdrawn_at timestamptz;
ALTER TABLE hhc_web.bulletin_watermark_dsr_action ADD CONSTRAINT watermark_withdrawn_origin CHECK (withdrawn_at IS NULL OR (request_id IS NOT NULL AND action='restrict_processing'));
CREATE INDEX watermark_dsr_subject_origin ON hhc_web.bulletin_watermark_dsr_action(subject_hash,request_id);

-- Preserve the legacy fence when an old writer or retention removes origin proof.
-- This also covers the previous deployed worker while revisions overlap.
CREATE FUNCTION hhc_web.invalidate_watermark_restriction_origin() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.action='restrict_processing' AND OLD.withdrawn_at IS NULL THEN
   UPDATE hhc_web.bulletin_watermark_subject SET origin_tracking_complete=false WHERE subject_hash=OLD.subject_hash;
  END IF;
  RETURN OLD;
 END IF;
 IF NEW.action='restrict_processing' AND NEW.request_id IS NULL THEN
  UPDATE hhc_web.bulletin_watermark_subject SET origin_tracking_complete=false WHERE subject_hash=NEW.subject_hash;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER watermark_restriction_origin_insert AFTER INSERT ON hhc_web.bulletin_watermark_dsr_action FOR EACH ROW EXECUTE FUNCTION hhc_web.invalidate_watermark_restriction_origin();
CREATE TRIGGER watermark_restriction_origin_delete BEFORE DELETE ON hhc_web.bulletin_watermark_dsr_action FOR EACH ROW EXECUTE FUNCTION hhc_web.invalidate_watermark_restriction_origin();

CREATE TABLE hhc_web.bulletin_watermark_restriction_withdrawal (
 key_hash text PRIMARY KEY,
 request_fingerprint text NOT NULL,
 request_id uuid NOT NULL,
 original_request_id uuid NOT NULL,
 subject_hash text NOT NULL,
 result jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX watermark_withdrawal_subject ON hhc_web.bulletin_watermark_restriction_withdrawal(subject_hash);
