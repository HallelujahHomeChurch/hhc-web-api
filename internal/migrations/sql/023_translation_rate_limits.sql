CREATE TABLE hhc_web.translation_rate_limit (
  scope text NOT NULL,
  window_start timestamptz NOT NULL,
  count integer NOT NULL CHECK (count > 0),
  PRIMARY KEY (scope, window_start)
);
