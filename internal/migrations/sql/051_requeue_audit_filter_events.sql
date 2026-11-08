UPDATE hhc_web.outbox_event
SET payload_json = jsonb_set(payload_json, '{metadata}', (payload_json->'metadata') - 'filterNames'::text),
    status = 'pending',
    attempts = 0,
    next_attempt_at = now(),
    claimed_until = NULL,
    last_error = '',
    updated_at = now()
WHERE destination = 'audit-log'
  AND event_type = 'audit.append'
  AND status = 'failed'
  AND last_error = 'dead_letter'
  AND payload_json->>'sourceService' = 'hhc-web-api'
  AND jsonb_typeof(payload_json->'metadata') = 'object'
  AND jsonb_typeof(payload_json->'metadata'->'filterNames') = 'array'
  AND (
    (payload_json->>'action' = 'cms.bulletin.list'
      AND (payload_json->'metadata') - 'filterNames'::text = '{}'::jsonb)
    OR
    (payload_json->>'action' = 'cms.content.list'
      AND payload_json->'metadata'->>'module' IN ('news', 'pages')
      AND (payload_json->'metadata') - 'filterNames'::text
        = jsonb_build_object('module', payload_json->'metadata'->>'module'))
  );
