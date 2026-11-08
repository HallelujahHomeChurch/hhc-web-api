-- Two independent PostgreSQL CSPRNG UUIDs supply 244 random bits without pgcrypto.
-- This server-only seed is independent of the stream key, so senders cannot forge
-- fresh sessions after their Console authorizer loses permission.
ALTER TABLE hhc_web.member_recording_broadcast_source
 ADD COLUMN ingest_signing_secret text NOT NULL DEFAULT
 (replace(gen_random_uuid()::text,'-','') || replace(gen_random_uuid()::text,'-',''))
 CHECK(ingest_signing_secret ~ '^[a-f0-9]{64}$');
