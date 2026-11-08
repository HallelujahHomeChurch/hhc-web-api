CREATE TABLE hhc_web.bulletin_revision (
  issue_id uuid NOT NULL REFERENCES hhc_web.bulletin_issue(id) ON DELETE CASCADE,
  version bigint NOT NULL CHECK (version > 0),
  snapshot_json jsonb NOT NULL CHECK (jsonb_typeof(snapshot_json) = 'object'),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY(issue_id,version)
);
