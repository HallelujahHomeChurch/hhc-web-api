#!/usr/bin/env bash
set -euo pipefail

env_file="${HHC_ENV_FILE:-/Users/rayselfs/Projects/hhc/.env.json}"
host="${HHC_WEB_DB_HOST:-172.16.68.4}"
port="${HHC_WEB_DB_PORT:-5432}"
database="${HHC_WEB_DB_NAME:-hhc_web}"
admin_user="${HHC_WEB_DB_ADMIN_USER:-HHCAdmin}"
sslmode="${HHC_WEB_DB_SSLMODE:-require}"
runtime_vault="${HHC_WEB_RUNTIME_KEY_VAULT:-alive-hhw-runtime-kv}"
migration_vault="${HHC_WEB_MIGRATION_KEY_VAULT:-alive-hhw-migrate-kv}"
legacy_vault="${HHC_WEB_LEGACY_KEY_VAULT:-alive-vault}"
secret_file=""
trap 'rm -f "${secret_file:-}"' EXIT

for command in jq openssl; do
  command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 1; }
done
[[ -f "$env_file" ]] || { echo "environment file not found: $env_file" >&2; exit 1; }
chmod 0600 "$env_file"

echo "host=$host"
echo "database=$database"
echo "migration-role=hhc_web_migrate"
echo "runtime-role=hhc_web"
echo "runtime-key-vault=$runtime_vault"
echo "migration-key-vault=$migration_vault"

if [[ "${HHC_WEB_BOOTSTRAP_DRY_RUN:-0}" == "1" ]]; then
  exit 0
fi

psql_bin="$(command -v psql || true)"
[[ -n "$psql_bin" ]] || psql_bin="/opt/homebrew/opt/libpq/bin/psql"
[[ -x "$psql_bin" ]] || { echo "psql is required" >&2; exit 1; }

admin_password="$(jq -er '.PG_ADMIN_PASSWORD' "$env_file")"
migration_password=""
if [[ "${HHC_WEB_SKIP_KEY_VAULT:-0}" == "1" ]]; then
  migration_password="$(jq -er '.HHC_WEB_MIGRATE_DB_PASSWORD' "$env_file")"
else
  command -v az >/dev/null || { echo "az is required" >&2; exit 1; }

  runtime_dsn="$(az keyvault secret show --vault-name "$runtime_vault" --name database-url --query value -o tsv --only-show-errors 2>/dev/null || true)"
  if [[ -z "$runtime_dsn" ]]; then
    runtime_dsn="$(az keyvault secret show --vault-name "$legacy_vault" --name hhc-web-database-url --query value -o tsv --only-show-errors)"
    secret_file="$(mktemp)"
    chmod 0600 "$secret_file"
    printf '%s' "$runtime_dsn" >"$secret_file"
    az keyvault secret set --vault-name "$runtime_vault" --name database-url --file "$secret_file" --content-type text/plain --only-show-errors --output none
    rm -f "$secret_file"
    secret_file=""
  fi

  migration_dsn="$(az keyvault secret show --vault-name "$migration_vault" --name database-url --query value -o tsv --only-show-errors 2>/dev/null || true)"
  if [[ -n "$migration_dsn" ]]; then
    prefix="postgres://hhc_web_migrate:"
    suffix="@${host}:${port}/${database}?sslmode=${sslmode}"
    migration_password="${migration_dsn#"$prefix"}"
    migration_password="${migration_password%"$suffix"}"
    [[ "$migration_dsn" == "$prefix"*"$suffix" && "$migration_password" =~ ^[0-9a-f]{48}$ ]] || {
      echo "migration database URL has an unexpected format" >&2
      exit 1
    }
  else
    migration_password="$(openssl rand -hex 24)"
    migration_dsn="postgres://hhc_web_migrate:${migration_password}@${host}:${port}/${database}?sslmode=${sslmode}"
    secret_file="$(mktemp)"
    chmod 0600 "$secret_file"
    printf '%s' "$migration_dsn" >"$secret_file"
    az keyvault secret set --vault-name "$migration_vault" --name database-url --file "$secret_file" --content-type text/plain --only-show-errors --output none
    rm -f "$secret_file"
    secret_file=""
  fi
fi

export PGPASSWORD="$admin_password"
export HHC_WEB_MIGRATE_DB_PASSWORD="$migration_password"
"$psql_bin" "host=$host port=$port dbname=$database user=$admin_user sslmode=$sslmode" \
  --set=ON_ERROR_STOP=1 \
  --set=admin_user="$admin_user" \
  --set=database="$database" <<'SQL'
\getenv migration_password HHC_WEB_MIGRATE_DB_PASSWORD
BEGIN;
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';
SELECT format('CREATE ROLE hhc_web_migrate LOGIN PASSWORD %L', :'migration_password')
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'hhc_web_migrate')
\gexec
ALTER ROLE hhc_web_migrate LOGIN PASSWORD :'migration_password';
GRANT hhc_web_migrate TO :"admin_user";
SELECT pg_has_role(current_user, 'hhc_web', 'SET') AS has_runtime_role,
       pg_has_role(current_user, 'hhc_web_migrate', 'SET') AS has_migration_role
\gset
\if :has_runtime_role
\else
  \echo 'database admin must be a member of hhc_web'
  \quit 3
\endif
\if :has_migration_role
\else
  \echo 'database admin must be a member of hhc_web_migrate'
  \quit 3
\endif
REASSIGN OWNED BY hhc_web TO hhc_web_migrate;
ALTER DATABASE :"database" OWNER TO hhc_web_migrate;
ALTER SCHEMA public OWNER TO hhc_web_migrate;
SET ROLE hhc_web_migrate;
CREATE SCHEMA IF NOT EXISTS hhc_web;
CREATE TABLE IF NOT EXISTS public.schema_migrations (
  version text PRIMARY KEY,
  applied_at timestamptz NOT NULL DEFAULT now()
);
RESET ROLE;
REVOKE CREATE ON SCHEMA public, hhc_web FROM PUBLIC, hhc_web;
GRANT CONNECT ON DATABASE :"database" TO hhc_web;
GRANT USAGE ON SCHEMA public, hhc_web TO hhc_web;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public, hhc_web TO hhc_web;
GRANT USAGE, SELECT, UPDATE ON ALL SEQUENCES IN SCHEMA public, hhc_web TO hhc_web;
ALTER DEFAULT PRIVILEGES FOR ROLE hhc_web_migrate IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO hhc_web;
ALTER DEFAULT PRIVILEGES FOR ROLE hhc_web_migrate IN SCHEMA public
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO hhc_web;
ALTER DEFAULT PRIVILEGES FOR ROLE hhc_web_migrate IN SCHEMA hhc_web
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO hhc_web;
ALTER DEFAULT PRIVILEGES FOR ROLE hhc_web_migrate IN SCHEMA hhc_web
  GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO hhc_web;
REVOKE ALL PRIVILEGES ON TABLE public.schema_migrations FROM hhc_web;
COMMIT;
SQL
unset PGPASSWORD HHC_WEB_MIGRATE_DB_PASSWORD admin_password migration_password

if [[ "${HHC_WEB_SKIP_KEY_VAULT:-0}" != "1" ]]; then
  "$psql_bin" "$runtime_dsn" --set=ON_ERROR_STOP=1 -Atc 'SELECT 1' >/dev/null
  "$psql_bin" "$migration_dsn" --set=ON_ERROR_STOP=1 -Atc 'SELECT 1' >/dev/null
  unset runtime_dsn migration_dsn
fi

echo "hhc-web migration role and isolated Key Vault secrets are ready"
