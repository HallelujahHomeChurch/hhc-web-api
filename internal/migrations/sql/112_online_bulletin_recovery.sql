ALTER TABLE hhc_web.online_bulletin_extraction_job ADD COLUMN recognition_reused_from uuid REFERENCES hhc_web.online_bulletin_extraction_job(id) ON DELETE SET NULL;
ALTER TABLE hhc_web.online_bulletin_extraction_job ADD COLUMN requested_metadata jsonb
 CHECK (requested_metadata IS NULL OR jsonb_typeof(requested_metadata)='object' AND octet_length(requested_metadata::text)<=4096);
ALTER TABLE hhc_web.online_bulletin_update_job ADD COLUMN attempt_history_json jsonb NOT NULL DEFAULT '[]'
 CHECK (jsonb_typeof(attempt_history_json)='array' AND jsonb_array_length(attempt_history_json)<=6);
UPDATE hhc_web.online_bulletin_update_job SET attempt_history_json=checkpoint_json;
ALTER TABLE hhc_web.online_bulletin_layout_job ADD COLUMN repair_json jsonb;
ALTER TABLE hhc_web.online_bulletin_conversion_job ADD COLUMN assessment_json jsonb NOT NULL DEFAULT '[]'
 CHECK (jsonb_typeof(assessment_json)='array' AND jsonb_array_length(assessment_json)<=6);
