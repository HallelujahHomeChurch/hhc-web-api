ALTER TABLE hhc_web.member_recording
 ADD COLUMN deleted_at timestamptz,
 ADD COLUMN deletion_delivered_at timestamptz,
 ADD COLUMN deletion_checked_at timestamptz;
CREATE INDEX member_recording_deletion_queue_idx ON hhc_web.member_recording(deletion_checked_at NULLS FIRST,id)
 WHERE deleted_at IS NOT NULL AND deletion_delivered_at IS NULL;
-- Hidden publications must not become visible when the old switch is retired.
UPDATE hhc_web.member_recording SET status=CASE WHEN hidden THEN 'draft' ELSE status END,
 featured=false,hidden=false,version=version+1,updated_at=clock_timestamp()
 WHERE hidden OR featured;
