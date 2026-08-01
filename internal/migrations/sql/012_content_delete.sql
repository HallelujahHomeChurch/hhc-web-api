UPDATE hhc_web.content_entry SET status='draft' WHERE status='archived';
UPDATE hhc_web.bulletin_issue SET status='draft' WHERE status='archived';

ALTER TABLE hhc_web.content_entry DROP CONSTRAINT content_entry_status_check;
ALTER TABLE hhc_web.content_entry ADD CONSTRAINT content_entry_status_check
  CHECK (status IN ('draft','publishing','published','publish_failed','unpublishing','unpublish_failed','unpublished'));

ALTER TABLE hhc_web.bulletin_issue DROP CONSTRAINT bulletin_issue_status_check;
ALTER TABLE hhc_web.bulletin_issue ADD CONSTRAINT bulletin_issue_status_check
  CHECK (status IN ('draft','publishing','published','unpublishing','unpublish_failed','unpublished'));

CREATE TABLE hhc_web.cms_audit_event (
  id uuid PRIMARY KEY,
  action text NOT NULL,
  resource_type text NOT NULL,
  resource_id uuid NOT NULL,
  actor text NOT NULL,
  payload_json jsonb NOT NULL CHECK (jsonb_typeof(payload_json)='object'),
  created_at timestamptz NOT NULL
);
CREATE INDEX cms_audit_event_resource_idx
  ON hhc_web.cms_audit_event(resource_type,resource_id,created_at DESC);
