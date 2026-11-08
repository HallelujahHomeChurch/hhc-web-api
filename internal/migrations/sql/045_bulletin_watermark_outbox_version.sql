UPDATE hhc_web.outbox_event e
SET aggregate_version=COALESCE(
      (SELECT generation FROM hhc_web.bulletin_download_job j
       WHERE e.payload_json->>'kind'='download' AND j.id=e.aggregate_id),
      1
    ),
    status='pending',
    attempts=0,
    claimed_until=NULL,
    last_error='',
    next_attempt_at=now(),
    updated_at=now()
WHERE event_type='bulletin.watermark.enqueue'
  AND aggregate_version IS NULL
  AND status IN ('pending','processing');
