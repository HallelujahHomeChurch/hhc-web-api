ALTER TABLE hhc_web.online_bulletin_extraction_job
  ADD COLUMN authorizer_id text,
  ADD COLUMN authorizer_request_id text,
  ADD CONSTRAINT online_bulletin_extraction_authorizer_pair
    CHECK ((authorizer_id IS NULL) = (authorizer_request_id IS NULL));

-- Legacy jobs keep their original provenance; do not invent a human authorizer.
