ALTER TABLE hhc_web.content_entry DROP CONSTRAINT content_entry_status_check;
ALTER TABLE hhc_web.content_entry ADD CONSTRAINT content_entry_status_check
  CHECK (status IN ('draft','publishing','published','publish_failed','unpublishing','unpublish_failed','unpublished','archived'));

ALTER TABLE hhc_web.news_item
  ADD COLUMN published_cover_asset_id text NOT NULL DEFAULT '',
  ADD COLUMN published_version bigint,
  ADD CONSTRAINT news_item_published_version_check CHECK (published_version IS NULL OR published_version > 0);

UPDATE hhc_web.news_item n
SET published_cover_asset_id=n.cover_asset_id,
    published_version=e.version
FROM hhc_web.content_entry e
WHERE e.id=n.entry_id AND e.status='published' AND n.public_grant_id<>'';
