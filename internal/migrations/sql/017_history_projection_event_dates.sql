WITH rewritten AS (
  SELECT projection.projection_key,
         jsonb_set(projection.payload_json, '{eventDate}', to_jsonb(history.event_date)) AS payload_json
  FROM hhc_web.public_projection AS projection
  JOIN hhc_web.history_event AS history ON history.entry_id = projection.resource_id
  WHERE projection.resource_type = 'history'
    AND history.event_date IS NOT NULL
)
UPDATE hhc_web.public_projection AS projection
SET payload_json = rewritten.payload_json,
    etag = md5(rewritten.payload_json::text),
    updated_at = now()
FROM rewritten
WHERE projection.projection_key = rewritten.projection_key
  AND projection.payload_json IS DISTINCT FROM rewritten.payload_json;
