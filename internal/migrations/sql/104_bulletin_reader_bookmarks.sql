-- Account/document parent serialization also bounds active bookmarks per document.
CREATE TABLE hhc_web.bulletin_reader_bookmark (
 account_id uuid NOT NULL,
 document_id uuid NOT NULL,
 id uuid NOT NULL,
 name text NOT NULL CHECK (length(name) <= 80),
 sentence_id text NOT NULL CHECK (length(sentence_id) <= 128),
 scalar_offset integer NOT NULL CHECK (scalar_offset >= 0),
 page_id text NOT NULL CHECK (length(page_id) <= 128),
 document_revision bigint NOT NULL CHECK (document_revision > 0),
 version bigint NOT NULL CHECK (version > 0),
 deleted boolean NOT NULL DEFAULT false,
 reanchor_required boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 CHECK ((deleted AND name='' AND sentence_id='' AND page_id='' AND scalar_offset=0 AND NOT reanchor_required)
     OR (NOT deleted AND length(name)>0 AND length(sentence_id)>0 AND length(page_id)>0)),
 PRIMARY KEY (account_id,document_id,id),
 FOREIGN KEY (account_id,document_id)
  REFERENCES hhc_web.bulletin_reader_migration_state ON DELETE CASCADE
);
