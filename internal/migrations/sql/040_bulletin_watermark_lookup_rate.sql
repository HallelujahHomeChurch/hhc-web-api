CREATE TABLE hhc_web.bulletin_watermark_lookup_rate (
 subject_hash text NOT NULL,
 window_start timestamptz NOT NULL,
 count integer NOT NULL CHECK(count>0),
 PRIMARY KEY(subject_hash,window_start)
);
