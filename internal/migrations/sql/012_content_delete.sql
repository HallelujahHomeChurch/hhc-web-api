UPDATE hhc_web.content_entry SET status='draft' WHERE status='archived';
UPDATE hhc_web.bulletin_issue SET status='draft' WHERE status='archived';

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
