-- Disabled until the bounded worker is explicitly enabled; the old read path remains.
CREATE TABLE hhc_web.member_recording_lifecycle_checkpoint (
 id boolean PRIMARY KEY DEFAULT true CHECK(id),
 checkpoint jsonb NOT NULL DEFAULT '{"repairs":[]}'::jsonb,
 lease_token uuid,
 lease_until timestamptz,
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK(jsonb_typeof(checkpoint)='object'),
 CHECK(jsonb_typeof(checkpoint->'repairs')='array'),
 CHECK(jsonb_array_length(checkpoint->'repairs')<=1000),
 CHECK(octet_length(checkpoint::text)<=524288)
);
