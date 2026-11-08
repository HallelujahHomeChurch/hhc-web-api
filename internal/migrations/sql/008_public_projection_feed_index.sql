CREATE INDEX public_projection_feed_idx
  ON hhc_web.public_projection(resource_type, locale, updated_at DESC);
