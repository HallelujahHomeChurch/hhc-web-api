ALTER TABLE hhc_web.content_entry
  DROP CONSTRAINT content_entry_module_check;
ALTER TABLE hhc_web.content_entry
  ADD CONSTRAINT content_entry_module_check
  CHECK (module IN ('news','history','videos','locations','pages')) NOT VALID;
ALTER TABLE hhc_web.content_entry
  VALIDATE CONSTRAINT content_entry_module_check;

CREATE TABLE hhc_web.location_item (
  content_id uuid PRIMARY KEY REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
  stable_key text NOT NULL CHECK (stable_key ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'),
  map_href text NOT NULL,
  sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order >= 0)
);

CREATE UNIQUE INDEX location_item_stable_key_uq ON hhc_web.location_item(stable_key);
CREATE INDEX location_item_sort_idx ON hhc_web.location_item(sort_order,stable_key);
