#!/usr/bin/env bash
set -euo pipefail

env_file="${HHC_ENV_FILE:-/Users/rayselfs/Projects/hhc/.env.json}"
host="${HHC_WEB_DB_HOST:-172.16.68.4}"
port="${HHC_WEB_DB_PORT:-5432}"
database="hhc_web"
role="hhc_web"
vault="${HHC_WEB_KEY_VAULT:-alive-vault}"
secret_name="hhc-web-database-url"
tmp_env=""
secret_file=""
trap 'rm -f "${tmp_env:-}" "${secret_file:-}"' EXIT

for command in jq openssl; do
  command -v "$command" >/dev/null || { echo "$command is required" >&2; exit 1; }
done
[[ -f "$env_file" ]] || { echo "environment file not found: $env_file" >&2; exit 1; }
chmod 0600 "$env_file"

admin_password="$(jq -er '.PG_ADMIN_PASSWORD' "$env_file")"
database_password="$(jq -r '.HHC_WEB_DB_PASSWORD // empty' "$env_file")"
if [[ -z "$database_password" ]]; then
  database_password="$(openssl rand -hex 24)"
  tmp_env="$(mktemp "${env_file}.XXXXXX")"
  jq --arg password "$database_password" '. + {HHC_WEB_DB_PASSWORD: $password}' "$env_file" >"$tmp_env"
  chmod 0600 "$tmp_env"
  mv "$tmp_env" "$env_file"
  tmp_env=""
fi

echo "host=$host"
echo "database=$database"
echo "role=$role"
echo "sslmode=require"
echo "key-vault-secret=$secret_name"

if [[ "${HHC_WEB_BOOTSTRAP_DRY_RUN:-0}" == "1" ]]; then
  exit 0
fi

psql_bin="$(command -v psql || true)"
[[ -n "$psql_bin" ]] || psql_bin="/opt/homebrew/opt/libpq/bin/psql"
[[ -x "$psql_bin" ]] || { echo "psql is required" >&2; exit 1; }
command -v az >/dev/null || { echo "az is required" >&2; exit 1; }

export PGPASSWORD="$admin_password"
export HHC_WEB_DB_PASSWORD="$database_password"
"$psql_bin" "host=$host port=$port dbname=postgres user=HHCAdmin sslmode=require" \
  --set=ON_ERROR_STOP=1 <<'SQL'
\getenv database_password HHC_WEB_DB_PASSWORD
SELECT format('CREATE ROLE hhc_web LOGIN PASSWORD %L', :'database_password')
WHERE NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = 'hhc_web')
\gexec
ALTER ROLE hhc_web LOGIN PASSWORD :'database_password';
SELECT 'CREATE DATABASE hhc_web OWNER hhc_web'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'hhc_web')
\gexec
\connect hhc_web
ALTER SCHEMA public OWNER TO hhc_web;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE, CREATE ON SCHEMA public TO hhc_web;
SQL
unset PGPASSWORD HHC_WEB_DB_PASSWORD admin_password database_password

encoded_password="$(jq -j '.HHC_WEB_DB_PASSWORD' "$env_file" | jq -sRr @uri)"
secret_file="$(mktemp)"
chmod 0600 "$secret_file"
printf 'postgres://hhc_web:%s@%s:%s/hhc_web?sslmode=require' "$encoded_password" "$host" "$port" >"$secret_file"
unset encoded_password

az keyvault secret set \
  --vault-name "$vault" \
  --name "$secret_name" \
  --file "$secret_file" \
  --content-type text/plain \
  --only-show-errors \
  --output none

rm -f "$secret_file"
secret_file=""
echo "hhc-web database and Key Vault secret are ready"
