ALTER TABLE hhc_web.online_bulletin_conversion_job
 ADD COLUMN review_issues jsonb NOT NULL DEFAULT '[]'::jsonb
 CHECK (jsonb_typeof(review_issues) = 'array' AND jsonb_array_length(review_issues) <= 64);
