ALTER TABLE hhc_web.bulletin_watermark_investigation
  ADD COLUMN match_method text CHECK(match_method IN ('output_sha256','payload','candidate'));

UPDATE hhc_web.bulletin_watermark_investigation
SET match_method='candidate'
WHERE status='completed' AND result='matched';
