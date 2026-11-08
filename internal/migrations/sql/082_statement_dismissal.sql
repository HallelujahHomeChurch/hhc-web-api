CREATE TABLE hhc_web.statement_dismissal (
 account_id uuid NOT NULL,
 statement_id uuid NOT NULL REFERENCES hhc_web.content_entry(id) ON DELETE CASCADE,
 published_version bigint NOT NULL CHECK (published_version > 0 AND published_version <= 9007199254740991),
 PRIMARY KEY (account_id, statement_id, published_version)
);
CREATE INDEX statement_dismissal_statement_idx ON hhc_web.statement_dismissal(statement_id);
