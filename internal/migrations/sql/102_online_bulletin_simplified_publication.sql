-- Expand only. Legacy/manual conversions retain their existing validation path.
ALTER TABLE hhc_web.online_bulletin_conversion_job ADD COLUMN publication_event_id uuid;
