CREATE TABLE hhc_web.translation_cooldown (
    actor text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    source_version bigint NOT NULL CHECK (source_version > 0),
    target_locale text NOT NULL,
    next_allowed_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (actor, resource_type, resource_id, source_version, target_locale)
);

CREATE INDEX translation_cooldown_expiry_idx
    ON hhc_web.translation_cooldown (next_allowed_at);
