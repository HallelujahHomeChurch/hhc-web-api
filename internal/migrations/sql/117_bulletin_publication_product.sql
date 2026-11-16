CREATE TABLE hhc_web.bulletin_product_command (
 issue_id uuid NOT NULL,
 actor_id text NOT NULL,
 operation text NOT NULL CHECK (operation IN ('publication_update','complete_upload')),
 operation_key text NOT NULL CHECK (octet_length(operation_key) BETWEEN 1 AND 128),
 input_hash bytea NOT NULL CHECK (octet_length(input_hash)=32),
 result_json jsonb NOT NULL CHECK(jsonb_typeof(result_json)='object' AND octet_length(result_json::text)<=65536),
 created_at timestamptz NOT NULL,
 PRIMARY KEY (issue_id,actor_id,operation,operation_key)
);

CREATE TRIGGER bulletin_product_command_immutable BEFORE UPDATE OR DELETE ON hhc_web.bulletin_product_command
 FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_online_bulletin_history_update();

CREATE TABLE hhc_web.bulletin_issue_number_alias (
 issue_number integer PRIMARY KEY CHECK (issue_number>0),
 issue_id uuid NOT NULL REFERENCES hhc_web.bulletin_issue(id) ON DELETE CASCADE
);

CREATE FUNCTION hhc_web.guard_bulletin_issue_number_alias() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.issue_number IS NULL THEN RETURN NEW; END IF;
 PERFORM pg_advisory_xact_lock(1172101,NEW.issue_number);
 IF TG_TABLE_NAME='bulletin_issue' THEN
  IF EXISTS(SELECT 1 FROM hhc_web.bulletin_issue_number_alias a WHERE a.issue_number=NEW.issue_number AND a.issue_id<>NEW.id)
   OR EXISTS(SELECT 1 FROM hhc_web.bulletin_metadata_update c
     JOIN hhc_web.bulletin_issue i ON i.id=c.issue_id AND i.version=c.issue_version
     JOIN hhc_web.online_bulletin_layout_job l ON l.id=c.layout_job_id AND l.status IN('queued','validating','failed','ready')
     JOIN hhc_web.online_bulletin_document d ON d.id=l.document_id AND d.version=c.online_version
     WHERE c.issue_number=NEW.issue_number AND c.issue_id<>NEW.id) THEN
   RAISE EXCEPTION 'bulletin issue number is reserved' USING ERRCODE='23505';
  END IF;
 ELSE
  IF EXISTS(SELECT 1 FROM hhc_web.bulletin_issue i WHERE i.issue_number=NEW.issue_number AND i.id<>NEW.issue_id) THEN
   RAISE EXCEPTION 'bulletin issue number is reserved' USING ERRCODE='23505';
  END IF;
 END IF;
 RETURN NEW;
END $$;

CREATE TRIGGER bulletin_issue_alias_collision BEFORE INSERT OR UPDATE OF issue_number ON hhc_web.bulletin_issue
 FOR EACH ROW EXECUTE FUNCTION hhc_web.guard_bulletin_issue_number_alias();
CREATE TRIGGER bulletin_alias_issue_collision BEFORE INSERT OR UPDATE ON hhc_web.bulletin_issue_number_alias
 FOR EACH ROW EXECUTE FUNCTION hhc_web.guard_bulletin_issue_number_alias();

ALTER TABLE hhc_web.bulletin_published_version ADD COLUMN issue_number integer, ADD COLUMN issue_date date;
UPDATE hhc_web.bulletin_published_version p SET issue_number=i.issue_number,issue_date=i.issue_date
 FROM hhc_web.bulletin_issue i WHERE i.id=p.issue_id;

CREATE TABLE hhc_web.bulletin_metadata_update (
 issue_id uuid PRIMARY KEY REFERENCES hhc_web.bulletin_issue(id) ON DELETE CASCADE,
 issue_version bigint NOT NULL,
 online_version bigint NOT NULL,
 issue_number integer NOT NULL CHECK(issue_number>0),
 layout_job_id uuid NOT NULL REFERENCES hhc_web.online_bulletin_layout_job(id) ON DELETE CASCADE,
 metadata_json jsonb NOT NULL,
 actor_id text NOT NULL,
 request_id text NOT NULL
);
CREATE INDEX bulletin_metadata_update_issue_number_idx ON hhc_web.bulletin_metadata_update(issue_number);

CREATE TABLE hhc_web.bulletin_upload_asset (
 issue_id uuid NOT NULL REFERENCES hhc_web.bulletin_issue(id) ON DELETE CASCADE,
 series text NOT NULL,
 locale text NOT NULL,
 asset_id text NOT NULL CHECK(octet_length(asset_id) BETWEEN 1 AND 255),
 PRIMARY KEY(issue_id,series,locale,asset_id)
);
