#!/usr/bin/env bash
set -euo pipefail

fixture="$(mktemp)"
output="$(mktemp)"
trap 'rm -f "$fixture" "$output"' EXIT
printf '{"PG_ADMIN_PASSWORD":"admin-secret","HHC_WEB_DB_PASSWORD":"database-secret"}' >"$fixture"
chmod 0600 "$fixture"

HHC_ENV_FILE="$fixture" HHC_WEB_BOOTSTRAP_DRY_RUN=1 ./scripts/bootstrap-database.sh >"$output"
grep -q '^database=hhc_web$' "$output"
grep -q '^role=hhc_web$' "$output"
if grep -q 'admin-secret\\|database-secret' "$output"; then
  echo "bootstrap output leaked a password" >&2
  exit 1
fi
