CREATE TABLE hhc_web.legal_draft (
 scope text PRIMARY KEY CHECK(scope IN ('common','verified-member')),
 version bigint NOT NULL CHECK(version>0), payload jsonb NOT NULL,
 updated_by uuid NOT NULL, updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE hhc_web.legal_document (
 scope text NOT NULL CHECK(scope IN ('common','verified-member')),
 document_key text NOT NULL CHECK(document_key IN ('terms-of-use','privacy-policy')),
 document_version text NOT NULL, locale text NOT NULL CHECK(locale IN ('zh-Hant','zh-Hans','en','ja','ko')),
 sha256 text NOT NULL CHECK(sha256 ~ '^[a-f0-9]{64}$'), payload jsonb NOT NULL,
 PRIMARY KEY(scope,document_key,document_version,locale)
);
CREATE TABLE hhc_web.legal_snapshot (
 id uuid PRIMARY KEY, scope text NOT NULL CHECK(scope IN ('common','verified-member')),
 terms_version text NOT NULL, privacy_version text NOT NULL,
 locale text NOT NULL CHECK(locale IN ('zh-Hant','zh-Hans','en','ja','ko')),
 payload jsonb NOT NULL, published_by uuid NOT NULL, published_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(scope,terms_version,privacy_version,locale)
);
CREATE TABLE hhc_web.legal_current (
 scope text PRIMARY KEY CHECK(scope IN ('common','verified-member')),
 terms_version text NOT NULL, privacy_version text NOT NULL
);
CREATE FUNCTION hhc_web.reject_legal_snapshot_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Published legal evidence is immutable'; END; $$;
CREATE TRIGGER immutable_legal_snapshot BEFORE UPDATE OR DELETE ON hhc_web.legal_snapshot FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_legal_snapshot_change();
CREATE TRIGGER immutable_legal_document BEFORE UPDATE OR DELETE ON hhc_web.legal_document FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_legal_snapshot_change();

-- Local transactional provenance; published evidence and mutation history are never public projections.
CREATE TABLE hhc_web.legal_change (
 id uuid PRIMARY KEY, scope text NOT NULL CHECK(scope IN ('common','verified-member')),
 action text NOT NULL CHECK(action IN ('draft_saved','published')),
 draft_version bigint NOT NULL CHECK(draft_version>0), actor_id uuid NOT NULL,
 request_id text NOT NULL CHECK(length(request_id) BETWEEN 1 AND 128),
 occurred_at timestamptz NOT NULL DEFAULT now()
);
CREATE TRIGGER immutable_legal_change BEFORE UPDATE OR DELETE ON hhc_web.legal_change FOR EACH ROW EXECUTE FUNCTION hhc_web.reject_legal_snapshot_change();
