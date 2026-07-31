#!/usr/bin/env bash
set -euo pipefail

fixture="$(mktemp)"
psql_bin="$(command -v psql || true)"
[[ -n "$psql_bin" ]] || psql_bin="/opt/homebrew/opt/libpq/bin/psql"
admin_dsn="${TEST_POSTGRES_DSN:-}"
cleanup() {
  rm -f "$fixture"
  if [[ -n "$admin_dsn" ]]; then
    "$psql_bin" "$admin_dsn" --set=ON_ERROR_STOP=1 <<'SQL' >/dev/null
DROP DATABASE IF EXISTS hhc_web_bootstrap_test WITH (FORCE);
DROP ROLE IF EXISTS hhc_web_migrate;
DROP ROLE IF EXISTS hhc_web;
DROP ROLE IF EXISTS bootstrap_admin;
SQL
  fi
}
trap cleanup EXIT
printf '{"PG_ADMIN_PASSWORD":"admin-secret"}\n' >"$fixture"

output="$(HHC_ENV_FILE="$fixture" HHC_WEB_BOOTSTRAP_DRY_RUN=1 ./scripts/bootstrap-migration-role.sh)"
grep -q '^database=hhc_web$' <<<"$output"
grep -q '^migration-role=hhc_web_migrate$' <<<"$output"
grep -q '^runtime-role=hhc_web$' <<<"$output"
grep -q '^runtime-key-vault=alive-hhw-runtime-kv$' <<<"$output"
grep -q '^migration-key-vault=alive-hhw-migrate-kv$' <<<"$output"
[[ "$(jq -r 'has("HHC_WEB_MIGRATE_DB_PASSWORD")' "$fixture")" == "false" ]]

if [[ -z "$admin_dsn" ]]; then
  exit 0
fi
[[ "${HHC_WEB_ALLOW_DESTRUCTIVE_TEST:-0}" == "1" ]] || {
  echo "set HHC_WEB_ALLOW_DESTRUCTIVE_TEST=1 for the disposable Postgres integration test" >&2
  exit 1
}
case "$admin_dsn" in
  postgres://*@localhost:*/*|postgres://*@127.0.0.1:*/*) ;;
  *) echo "TEST_POSTGRES_DSN must target disposable localhost Postgres" >&2; exit 1 ;;
esac

"$psql_bin" "$admin_dsn" --set=ON_ERROR_STOP=1 <<'SQL' >/dev/null
DROP DATABASE IF EXISTS hhc_web_bootstrap_test WITH (FORCE);
DROP ROLE IF EXISTS hhc_web_migrate;
DROP ROLE IF EXISTS hhc_web;
DROP ROLE IF EXISTS bootstrap_admin;
CREATE ROLE bootstrap_admin LOGIN CREATEROLE CREATEDB PASSWORD 'bootstrap-password';
CREATE ROLE hhc_web LOGIN PASSWORD 'runtime-password';
GRANT hhc_web TO bootstrap_admin;
CREATE DATABASE hhc_web_bootstrap_test OWNER hhc_web;
SQL

"$psql_bin" "$admin_dsn" --set=ON_ERROR_STOP=1 <<'SQL' >/dev/null
\connect hhc_web_bootstrap_test
SET ROLE hhc_web;
CREATE SCHEMA hhc_web;
CREATE TABLE hhc_web.owned_by_runtime(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY);
RESET ROLE;
SQL

printf '{"PG_ADMIN_PASSWORD":"bootstrap-password","HHC_WEB_MIGRATE_DB_PASSWORD":"migration-password"}\n' >"$fixture"
for _ in 1 2; do
  HHC_ENV_FILE="$fixture" \
  HHC_WEB_DB_HOST=127.0.0.1 \
  HHC_WEB_DB_PORT=5432 \
  HHC_WEB_DB_NAME=hhc_web_bootstrap_test \
  HHC_WEB_DB_ADMIN_USER=bootstrap_admin \
  HHC_WEB_DB_SSLMODE=disable \
  HHC_WEB_SKIP_KEY_VAULT=1 \
  ./scripts/bootstrap-migration-role.sh >/dev/null
done

verification="$("$psql_bin" "$admin_dsn" -At <<'SQL'
\connect hhc_web_bootstrap_test
SELECT schema_owner FROM information_schema.schemata WHERE schema_name='hhc_web';
SELECT tableowner FROM pg_tables WHERE schemaname='hhc_web' AND tablename='owned_by_runtime';
SELECT has_table_privilege('hhc_web','hhc_web.owned_by_runtime','INSERT');
SELECT has_schema_privilege('hhc_web','hhc_web','CREATE');
SELECT has_table_privilege('hhc_web','public.schema_migrations','UPDATE');
SQL
)"
verification="$(tail -n 5 <<<"$verification")"
[[ "$verification" == $'hhc_web_migrate\nhhc_web_migrate\nt\nf\nf' ]]

PGPASSWORD=runtime-password "$psql_bin" \
  'postgres://hhc_web@127.0.0.1:5432/hhc_web_bootstrap_test?sslmode=disable' \
  --set=ON_ERROR_STOP=1 -c 'INSERT INTO hhc_web.owned_by_runtime DEFAULT VALUES' >/dev/null
if PGPASSWORD=runtime-password "$psql_bin" \
  'postgres://hhc_web@127.0.0.1:5432/hhc_web_bootstrap_test?sslmode=disable' \
  --set=ON_ERROR_STOP=1 -c 'CREATE TABLE hhc_web.forbidden(id bigint)' >/dev/null 2>&1; then
  echo "runtime role unexpectedly created a table" >&2
  exit 1
fi
if PGPASSWORD=runtime-password "$psql_bin" \
  'postgres://hhc_web@127.0.0.1:5432/hhc_web_bootstrap_test?sslmode=disable' \
  --set=ON_ERROR_STOP=1 -c "INSERT INTO public.schema_migrations(version) VALUES('forbidden')" >/dev/null 2>&1; then
  echo "runtime role unexpectedly modified migration history" >&2
  exit 1
fi
