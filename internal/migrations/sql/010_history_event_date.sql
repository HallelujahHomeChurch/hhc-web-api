ALTER TABLE hhc_web.history_event
  ADD COLUMN event_date text,
  ADD CONSTRAINT history_event_date_format_check CHECK (
    event_date IS NULL OR
    CASE
      WHEN event_date ~ '^[0-9]{4}$' THEN event_date <> '0000'
      WHEN event_date ~ '^[0-9]{4}-[0-9]{2}$' THEN to_char(to_date(event_date,'YYYY-MM'),'YYYY-MM') = event_date
      WHEN event_date ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' THEN to_char(to_date(event_date,'YYYY-MM-DD'),'YYYY-MM-DD') = event_date
      ELSE false
    END
  );

ALTER TABLE hhc_web.history_event DROP COLUMN sort_order;

CREATE INDEX history_event_event_date_idx
  ON hhc_web.history_event(event_date DESC NULLS LAST,entry_id DESC);
