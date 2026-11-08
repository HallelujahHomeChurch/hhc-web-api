ALTER TABLE hhc_web.bulletin_access ADD COLUMN membership_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE hhc_web.bulletin_grant_intent ADD COLUMN service_only boolean NOT NULL DEFAULT false;
