CREATE INDEX CONCURRENTLY IF NOT EXISTS online_bulletin_extraction_document_created_idx
ON hhc_web.online_bulletin_extraction_job(document_id, created_at DESC, id DESC);
