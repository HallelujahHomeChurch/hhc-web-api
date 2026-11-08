ALTER TABLE hhc_web.content_entry
  ADD COLUMN idempotency_fingerprint text NOT NULL DEFAULT '';
