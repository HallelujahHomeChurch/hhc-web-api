WITH candidates AS (
  SELECT history.entry_id,
    CASE
      WHEN trim(translation.date_label) ~ '^[0-9]{4}年[0-9]{1,2}月[0-9]{1,2}日$' THEN
        regexp_replace(trim(translation.date_label), '^([0-9]{4})年([0-9]{1,2})月([0-9]{1,2})日$', '\1-\2-\3')
      WHEN trim(translation.date_label) ~ '^[0-9]{4}年[0-9]{1,2}月$' THEN
        regexp_replace(trim(translation.date_label), '^([0-9]{4})年([0-9]{1,2})月$', '\1-\2')
      WHEN trim(translation.date_label) ~ '^[0-9]{4}年$' THEN
        regexp_replace(trim(translation.date_label), '^([0-9]{4})年$', '\1')
      WHEN trim(translation.date_label) ~ '^[0-9]{4}(-[0-9]{1,2}){0,2}$' THEN trim(translation.date_label)
    END AS raw_date
  FROM hhc_web.history_event AS history
  JOIN hhc_web.content_translation AS translation ON translation.entry_id = history.entry_id
  WHERE history.event_date IS NULL AND translation.locale = 'zh-Hant'
), canonical AS (
  SELECT entry_id,
    CASE
      WHEN raw_date ~ '^[0-9]{4}$' THEN raw_date
      WHEN raw_date ~ '^[0-9]{4}-[0-9]{1,2}$' THEN split_part(raw_date, '-', 1) || '-' || lpad(split_part(raw_date, '-', 2), 2, '0')
      WHEN raw_date ~ '^[0-9]{4}-[0-9]{1,2}-[0-9]{1,2}$' THEN split_part(raw_date, '-', 1) || '-' || lpad(split_part(raw_date, '-', 2), 2, '0') || '-' || lpad(split_part(raw_date, '-', 3), 2, '0')
    END AS event_date
  FROM candidates
)
UPDATE hhc_web.history_event AS history
SET event_date = canonical.event_date
FROM canonical
WHERE history.entry_id = canonical.entry_id
  AND (
    canonical.event_date ~ '^[0-9]{4}$' OR
    (canonical.event_date ~ '^[0-9]{4}-[0-9]{2}$' AND split_part(canonical.event_date, '-', 2)::int BETWEEN 1 AND 12 AND to_char(to_date(canonical.event_date, 'YYYY-MM'), 'YYYY-MM') = canonical.event_date) OR
    (canonical.event_date ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' AND split_part(canonical.event_date, '-', 2)::int BETWEEN 1 AND 12 AND split_part(canonical.event_date, '-', 3)::int BETWEEN 1 AND 31 AND to_char(to_date(canonical.event_date, 'YYYY-MM-DD'), 'YYYY-MM-DD') = canonical.event_date)
  );
