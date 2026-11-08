-- Account IDs are trusted identity references, never cross-database foreign keys.
CREATE TABLE hhc_web.bulletin_reader_migration_state (
    account_id uuid NOT NULL,
    document_id uuid NOT NULL,
    applied_revision bigint NOT NULL CHECK (applied_revision > 0),
    conflicts jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(conflicts) = 'array'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, document_id),
    FOREIGN KEY (document_id, applied_revision)
        REFERENCES hhc_web.online_bulletin_publication(document_id, revision)
);

CREATE TABLE hhc_web.bulletin_reader_highlight (
    account_id uuid NOT NULL,
    document_id uuid NOT NULL,
    sentence_id text NOT NULL CHECK (length(sentence_id) BETWEEN 1 AND 128),
    color text NOT NULL CHECK (color IN ('yellow', 'red', 'blue')),
    quote text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    version bigint NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (account_id, document_id, sentence_id),
    FOREIGN KEY (account_id, document_id)
        REFERENCES hhc_web.bulletin_reader_migration_state ON DELETE CASCADE
);

CREATE TABLE hhc_web.bulletin_reader_note (
    account_id uuid NOT NULL,
    document_id uuid NOT NULL,
    id uuid NOT NULL,
    body text NOT NULL CHECK (length(body) <= 10000),
    sentence_ids jsonb NOT NULL CHECK (jsonb_typeof(sentence_ids) = 'array' AND jsonb_array_length(sentence_ids) <= 500),
    inactive_anchors jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(inactive_anchors) = 'array'),
    quote text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    deleted boolean NOT NULL DEFAULT false,
    reanchor_required boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (account_id, document_id, id),
    FOREIGN KEY (account_id, document_id)
        REFERENCES hhc_web.bulletin_reader_migration_state ON DELETE CASCADE
);

CREATE TABLE hhc_web.bulletin_reader_progress (
    account_id uuid NOT NULL,
    document_id uuid NOT NULL,
    page_id text NOT NULL DEFAULT '' CHECK (length(page_id) <= 128),
    component_id text NOT NULL DEFAULT '' CHECK (length(component_id) <= 128),
    sentence_id text NOT NULL DEFAULT '' CHECK (length(sentence_id) <= 128),
    recorded_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (account_id, document_id),
    FOREIGN KEY (account_id, document_id)
        REFERENCES hhc_web.bulletin_reader_migration_state ON DELETE CASCADE
);

CREATE TABLE hhc_web.bulletin_reader_processed_mutation (
    account_id uuid NOT NULL,
    mutation_id uuid NOT NULL,
    document_id uuid NOT NULL,
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    result_json jsonb NOT NULL CHECK (jsonb_typeof(result_json) = 'object'),
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at = created_at + interval '2184 hours'),
    PRIMARY KEY (account_id, mutation_id),
    FOREIGN KEY (account_id, document_id)
        REFERENCES hhc_web.bulletin_reader_migration_state ON DELETE CASCADE
);
CREATE INDEX bulletin_reader_processed_mutation_expiry
    ON hhc_web.bulletin_reader_processed_mutation(expires_at);
