-- Additive: NULL preserves legacy full-sentence rows and old writers.
ALTER TABLE hhc_web.bulletin_reader_highlight
    ADD COLUMN segments jsonb CHECK (segments IS NULL OR
        (jsonb_typeof(segments) = 'array' AND jsonb_array_length(segments) BETWEEN 1 AND 256));

ALTER TABLE hhc_web.bulletin_reader_note
    ADD COLUMN ranges jsonb CHECK (ranges IS NULL OR
        (jsonb_typeof(ranges) = 'array' AND jsonb_array_length(ranges) BETWEEN 1 AND 500));
