ALTER TABLE hhc_web.content_entry
  ADD COLUMN first_published_at timestamptz;

ALTER TABLE hhc_web.news_item
  ADD COLUMN author_name text NOT NULL DEFAULT '';

UPDATE hhc_web.content_entry
SET first_published_at=published_at
WHERE published_at IS NOT NULL AND first_published_at IS NULL;

WITH rewritten AS (
  SELECT projection.projection_key,
    projection.payload_json
      || jsonb_build_object(
        'firstPublishedAt', entry.first_published_at,
        'lastPublishedAt', entry.published_at
      )
      || CASE WHEN news.author_name<>''
        THEN jsonb_build_object('authorName', news.author_name)
        ELSE '{}'::jsonb
      END AS payload_json
  FROM hhc_web.public_projection projection
  JOIN hhc_web.content_entry entry ON entry.id=projection.resource_id
  JOIN hhc_web.news_item news ON news.entry_id=entry.id
  WHERE projection.resource_type='news' AND entry.published_at IS NOT NULL
)
UPDATE hhc_web.public_projection projection
SET payload_json=rewritten.payload_json,
    etag=md5(rewritten.payload_json::text)
FROM rewritten
WHERE projection.projection_key=rewritten.projection_key
  AND projection.payload_json IS DISTINCT FROM rewritten.payload_json;
