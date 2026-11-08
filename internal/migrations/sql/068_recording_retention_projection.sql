-- Read model only. Asset remains the sole policy and media lifecycle authority.
ALTER TABLE hhc_web.member_recording
  DROP CONSTRAINT member_recording_completion_check,
  ADD CONSTRAINT member_recording_completion_check CHECK (
    (uploaded_at IS NULL AND expires_at IS NULL AND status='draft') OR
    (uploaded_at IS NOT NULL AND expires_at IS NOT NULL)
  ),
  ADD COLUMN ready_at timestamptz,
  ADD COLUMN retention_revision bigint NOT NULL DEFAULT 0 CHECK(retention_revision>=0),
  ADD COLUMN media_terminal boolean NOT NULL DEFAULT false;
UPDATE hhc_web.member_recording SET ready_at=uploaded_at WHERE package_id IS NOT NULL;
