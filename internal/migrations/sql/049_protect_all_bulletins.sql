ALTER TABLE hhc_web.bulletin_issue
  ADD COLUMN series text NOT NULL DEFAULT 'general'
  CHECK (series ~ '^[a-z][a-z0-9_-]{0,49}$');

CREATE TABLE hhc_web.bulletin_entitlement_policy (
  series text NOT NULL,
  locale text NOT NULL,
  entitlement_code text NOT NULL UNIQUE,
  PRIMARY KEY(series,locale)
);

INSERT INTO hhc_web.bulletin_entitlement_policy(series,locale,entitlement_code) VALUES
  ('general','zh-Hant','bulletin.general.zh-Hant.access'),
  ('general','zh-Hans','bulletin.general.zh-Hans.access'),
  ('general','en','bulletin.general.en.access');

CREATE TABLE hhc_web.bulletin_published_version (
  issue_id uuid NOT NULL REFERENCES hhc_web.bulletin_issue(id) ON DELETE CASCADE,
  series text NOT NULL,
  locale text NOT NULL,
  title text NOT NULL,
  subtitle text NOT NULL,
  asset_id text NOT NULL,
  published_at timestamptz NOT NULL,
  version bigint NOT NULL CHECK(version > 0),
  PRIMARY KEY(issue_id,locale)
);
CREATE INDEX bulletin_published_version_latest_idx
  ON hhc_web.bulletin_published_version(series,locale,published_at DESC,issue_id);

INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version)
SELECT i.id,i.series,v.locale,v.title,v.subtitle,v.pdf_asset_id,COALESCE(v.published_at,i.published_at,now()),i.version
FROM hhc_web.bulletin_issue i
JOIN hhc_web.bulletin_version v ON v.issue_id=i.id
WHERE v.status='published';

DELETE FROM hhc_web.public_projection
WHERE resource_type IN ('bulletin_issue','bulletin_latest');

DELETE FROM hhc_web.outbox_event
WHERE event_type='bulletin.access.reconcile';
