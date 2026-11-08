ALTER TABLE hhc_web.bulletin_version DROP CONSTRAINT bulletin_version_locale_check;
ALTER TABLE hhc_web.bulletin_version ADD CONSTRAINT bulletin_version_locale_check CHECK (locale IN ('zh-Hant','zh-Hans','en','ja','ko'));

ALTER TABLE hhc_web.content_translation DROP CONSTRAINT content_translation_locale_check;
ALTER TABLE hhc_web.content_translation ADD CONSTRAINT content_translation_locale_check CHECK (locale IN ('zh-Hant','zh-Hans','en','ja','ko'));

ALTER TABLE hhc_web.public_projection DROP CONSTRAINT public_projection_locale_check;
ALTER TABLE hhc_web.public_projection ADD CONSTRAINT public_projection_locale_check CHECK (locale IN ('zh-Hant','zh-Hans','en','ja','ko'));

ALTER TABLE hhc_web.publication_workflow DROP CONSTRAINT publication_workflow_locale_check;
ALTER TABLE hhc_web.publication_workflow ADD CONSTRAINT publication_workflow_locale_check CHECK (locale IN ('zh-Hant','zh-Hans','en','ja','ko'));
