ALTER TABLE hhc_web.bulletin_version
 ADD COLUMN auto_publish boolean NOT NULL DEFAULT false,
 ADD COLUMN auto_notify boolean NOT NULL DEFAULT false,
 ADD COLUMN auto_publish_request text;
-- NULL is a pre-intent upload; explicit cancellation stores an empty request.
ALTER TABLE hhc_web.online_bulletin_update_job ADD COLUMN pdf_workflow_id uuid REFERENCES hhc_web.publication_workflow(id) ON DELETE SET NULL;
