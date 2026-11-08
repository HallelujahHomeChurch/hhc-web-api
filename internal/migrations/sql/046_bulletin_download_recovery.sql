UPDATE hhc_web.bulletin_download_job
SET status='queued',
    lease_token=NULL,
    lease_until=NULL,
    attempts=0,
    error_code=NULL,
    updated_at=now()
WHERE status IN ('queued','running')
  AND expires_at>now();

INSERT INTO hhc_web.outbox_event(
  id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,
  payload_json,idempotency_key,status,attempts,next_attempt_at,created_at,updated_at
)
SELECT gen_random_uuid(),'bulletin-watermark-queue','bulletin.watermark.enqueue',
       'bulletin_watermark',j.id,j.generation,
       jsonb_build_object('kind','download','id',j.id::text),
       'bulletin-watermark:download:'||j.id::text||':'||j.generation::text,
       'pending',0,now(),now(),now()
FROM hhc_web.bulletin_download_job j
WHERE j.status='queued'
  AND j.expires_at>now()
ON CONFLICT(destination,idempotency_key) DO UPDATE
SET aggregate_version=EXCLUDED.aggregate_version,
    payload_json=EXCLUDED.payload_json,
    status='pending',
    attempts=0,
    claimed_until=NULL,
    last_error='',
    next_attempt_at=now(),
    updated_at=now();
