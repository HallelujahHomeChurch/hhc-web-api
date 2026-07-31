DROP INDEX hhc_web.content_entry_module_status_idx;
CREATE INDEX content_entry_module_status_idx
  ON hhc_web.content_entry(module,status,updated_at DESC,id DESC);

CREATE INDEX content_entry_module_updated_idx
  ON hhc_web.content_entry(module,updated_at DESC,id DESC);

DROP INDEX hhc_web.news_item_display_date_idx;
CREATE INDEX news_item_display_date_idx
  ON hhc_web.news_item(display_date DESC,entry_id DESC);
