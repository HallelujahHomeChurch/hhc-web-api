WITH rewritten AS (
  SELECT projection_key,
         jsonb_set(
           payload_json,
           '{imageUrl}',
           to_jsonb(replace(replace(payload_json->>'imageUrl', 'https://img.youtube.com/', 'https://i.ytimg.com/'), 'maxresdefault.jpg', 'hqdefault.jpg'))
         ) AS payload_json
  FROM hhc_web.public_projection
  WHERE resource_type = 'videos'
    AND payload_json->>'imageUrl' LIKE '%/maxresdefault.jpg'
)
UPDATE hhc_web.public_projection AS projection
SET payload_json = rewritten.payload_json,
    etag = md5(rewritten.payload_json::text),
    updated_at = now()
FROM rewritten
WHERE projection.projection_key = rewritten.projection_key;
