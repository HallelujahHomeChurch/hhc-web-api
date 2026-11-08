ALTER TABLE hhc_web.news_item
    ADD COLUMN home_cover_asset_id text NOT NULL DEFAULT '',
    ADD COLUMN home_public_grant_id text NOT NULL DEFAULT '',
    ADD COLUMN published_home_cover_asset_id text NOT NULL DEFAULT '';
