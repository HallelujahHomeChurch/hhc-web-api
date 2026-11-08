WITH download_urls AS (
  SELECT projection_key,
         CASE
           WHEN payload_json ? 'downloadUrl' THEN jsonb_set(
             payload_json,
             '{downloadUrl}',
             to_jsonb(replace(payload_json->>'downloadUrl', '/api/assets/public/', '/assets/'))
           )
           ELSE payload_json
         END AS payload_json
  FROM hhc_web.public_projection
), canonical AS (
  SELECT projection_key,
         CASE
           WHEN payload_json ? 'imageUrl' THEN jsonb_set(
             payload_json,
             '{imageUrl}',
             to_jsonb(replace(payload_json->>'imageUrl', '/api/assets/public/', '/assets/'))
           )
           ELSE payload_json
         END AS payload_json
  FROM download_urls
)
UPDATE hhc_web.public_projection AS projection
SET payload_json = canonical.payload_json,
    etag = md5(canonical.payload_json::text),
    updated_at = now()
FROM canonical
WHERE projection.projection_key = canonical.projection_key
  AND projection.payload_json IS DISTINCT FROM canonical.payload_json;
