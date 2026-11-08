-- Expand the legacy singleton into named identities without rotating credentials.
-- Old binaries select WHERE singleton; only the original row keeps true.
ALTER TABLE hhc_web.member_recording_broadcast_source
 ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid(),
 ADD COLUMN name text NOT NULL DEFAULT 'HHC' CHECK(length(btrim(name)) BETWEEN 1 AND 100),
 ADD COLUMN key_generation bigint NOT NULL DEFAULT 1 CHECK(key_generation BETWEEN 1 AND 9007199254740991);
ALTER TABLE hhc_web.member_recording_broadcast_source DROP CONSTRAINT member_recording_broadcast_source_pkey;
ALTER TABLE hhc_web.member_recording_broadcast_source ALTER COLUMN singleton DROP NOT NULL;
ALTER TABLE hhc_web.member_recording_broadcast_source ADD PRIMARY KEY(id);
-- CHECK(singleton) permits NULL for new sources, but rejects false.
CREATE UNIQUE INDEX member_recording_broadcast_legacy_source ON hhc_web.member_recording_broadcast_source(singleton) WHERE singleton;
CREATE UNIQUE INDEX member_recording_broadcast_source_key ON hhc_web.member_recording_broadcast_source(key_hash) WHERE key_hash<>'';

ALTER TABLE hhc_web.member_recording_broadcast
 ADD COLUMN source_id uuid REFERENCES hhc_web.member_recording_broadcast_source(id);
UPDATE hhc_web.member_recording_broadcast SET source_id=(SELECT id FROM hhc_web.member_recording_broadcast_source WHERE singleton) WHERE source_managed;
CREATE INDEX member_recording_broadcast_source_recording ON hhc_web.member_recording_broadcast(source_id);
-- Keep the existing global input uniqueness fence (capacity 1) throughout rollout.
-- Source identity is independent of the current target; old sessions remain scoped.
