ALTER TABLE hhc_web.news_item
    ADD COLUMN detail_layout text NOT NULL DEFAULT 'top'
    CHECK (detail_layout IN ('top', 'left', 'right'));
