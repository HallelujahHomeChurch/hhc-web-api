ALTER TABLE hhc_web.bulletin_watermark_receipt
 ADD COLUMN source_asset_key text GENERATED ALWAYS AS (replace(source_asset_id::text, '-', '')) STORED
 CHECK (source_asset_key ~ '^[a-f0-9]{32}$');

ALTER TABLE hhc_web.bulletin_watermark_investigation
 ADD COLUMN source_asset_key text GENERATED ALWAYS AS (replace(source_asset_id::text, '-', '')) STORED
 CHECK (source_asset_key ~ '^[a-f0-9]{32}$');
