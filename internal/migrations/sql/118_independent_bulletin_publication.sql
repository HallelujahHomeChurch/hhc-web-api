-- Only newly authorized plans use independent publication. No historical flags
-- or pending legacy jobs are interpreted as new publisher consent.
CREATE TABLE hhc_web.bulletin_independent_release (
 intent_id uuid PRIMARY KEY REFERENCES hhc_web.online_bulletin_publication_intent(id),
 pdf_workflow_id uuid UNIQUE REFERENCES hhc_web.publication_workflow(id),
 online_completed boolean NOT NULL DEFAULT false
);
ALTER TABLE hhc_web.bulletin_version ADD COLUMN verified_pdf_asset_id text;
