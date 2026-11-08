-- Durable format-independent first-visibility evidence; no recipient or presentation data.
CREATE TABLE hhc_web.inbox_publication_source (
 kind text NOT NULL CHECK(kind IN ('recording.published','bulletin.published')),
 resource_id text NOT NULL,
 series text NOT NULL DEFAULT '',
 locale text NOT NULL DEFAULT '',
 first_visible_at timestamptz NOT NULL,
 PRIMARY KEY(kind,resource_id,series,locale),
 CHECK((kind='recording.published' AND series='' AND locale='') OR (kind='bulletin.published' AND ((series='general' AND locale IN ('zh-Hant','zh-Hans','en')) OR (series='children' AND locale IN ('zh-Hant','en')))))
);
-- Historical visibility is suppressed, never enumerated into the inbox outbox.
INSERT INTO hhc_web.inbox_publication_source(kind,resource_id,series,locale,first_visible_at)
 SELECT 'bulletin.published',issue_id::text,series,locale,min(visible_at) FROM (
 SELECT issue_id,series,locale,published_at AS visible_at FROM hhc_web.bulletin_version WHERE published_at IS NOT NULL
 UNION ALL SELECT d.issue_id,d.series,d.content_locale,min(p.created_at) FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_publication p ON p.document_id=d.id GROUP BY d.issue_id,d.series,d.content_locale
 ) published GROUP BY issue_id,series,locale;
INSERT INTO hhc_web.inbox_publication_source(kind,resource_id,first_visible_at)
 SELECT 'recording.published',r.id::text,COALESCE(r.published_at,(b.payload_json->>'StartedAt')::timestamptz)
 FROM hhc_web.member_recording r LEFT JOIN hhc_web.member_recording_broadcast b ON b.recording_id=r.id
 WHERE r.published_at IS NOT NULL OR (b.payload_json->>'StartedAt' IS NOT NULL AND NOT COALESCE((b.payload_json->>'LiveNotificationPending')::boolean,false));
-- Legacy C1 has no exact first-live timestamp. A retained live state is evidence
-- only if the published waterline was actually playable at last_advanced_at.
-- Other states cannot prove prior visibility; never turn ambiguous history into
-- a permanent suppression marker. The witness time is not reconstructed first-live.
WITH legacy AS (
 SELECT c.recording_id,c.capture_id,c.payload_json,
 (c.payload_json->'Status'->>'createdAt')::timestamptz AS created_at,
 (c.payload_json->'Status'->>'expiresAt')::timestamptz AS expires_at,
 (c.payload_json->'Status'->'progress'->>'lastAdvancedAt')::timestamptz AS observed_at,
 (c.payload_json->'Status'->'progress'->>'mediaEndSeconds')::double precision AS media_end_seconds
 FROM hhc_web.member_recording_capture c WHERE COALESCE(c.broadcast_epoch,0)=0
)
INSERT INTO hhc_web.inbox_publication_source(kind,resource_id,first_visible_at)
 SELECT 'recording.published',recording_id::text,min(observed_at) FROM legacy
 WHERE payload_json->'Create'->>'liveEnabled'='true'
 AND payload_json->'Status'->>'liveEnabled'='true'
 AND payload_json->'Status'->>'liveState'='live'
 AND payload_json->'Status'->>'state' IN ('uploading','freezing','validating','ready')
 AND capture_id<>'' AND payload_json->'Status'->>'captureId'=capture_id
 AND payload_json->'Status'->>'recordingId'=recording_id::text
 AND payload_json->'Status'->>'terminalAt' IS NULL
 AND payload_json->'Status'->>'stopAcceptedAt' IS NULL
 AND (payload_json->'Status'->'progress'->>'lastSequence')::int>=2
 AND created_at<>'0001-01-01'::timestamptz AND observed_at>=created_at
 AND observed_at<=clock_timestamp() AND observed_at<expires_at
 AND (payload_json->>'MediaExpiresAt' IS NULL OR observed_at<(payload_json->>'MediaExpiresAt')::timestamptz)
 AND (payload_json->'Status'->>'replayUntil' IS NULL OR observed_at<(payload_json->'Status'->>'replayUntil')::timestamptz)
 AND media_end_seconds>=0
 AND EXTRACT(EPOCH FROM observed_at-created_at)-media_end_seconds+90<=180
 GROUP BY recording_id ON CONFLICT DO NOTHING;
CREATE INDEX inbox_publication_pending ON hhc_web.outbox_event(next_attempt_at,created_at) WHERE destination='engagement-inbox' AND status IN ('pending','processing');
