ALTER TABLE hhc_web.bulletin_issue
  ADD COLUMN notification_status text NOT NULL DEFAULT 'not_requested'
    CHECK (notification_status IN ('not_requested','pending','queued','failed')),
  ADD COLUMN notification_queued_at timestamptz,
  ADD COLUMN notification_error_code text NOT NULL DEFAULT '';
