#!/bin/sh
set -eu

pattern='DROP[[:space:]]+(SCHEMA|TABLE|COLUMN|VIEW|MATERIALIZED[[:space:]]+VIEW|TYPE|FUNCTION|CONSTRAINT)|TRUNCATE|RENAME[[:space:]]+(TABLE|COLUMN)|ALTER[^;]*(TYPE|DROP[[:space:]]+CONSTRAINT)|SET[[:space:]]+NOT[[:space:]]+NULL'
legacy_pattern='DROP[[:space:]]+(SCHEMA|TABLE|COLUMN|VIEW|MATERIALIZED[[:space:]]+VIEW|TYPE|FUNCTION)|TRUNCATE|RENAME[[:space:]]+(TABLE|COLUMN)|ALTER[^;]*TYPE|SET[[:space:]]+NOT[[:space:]]+NULL'

if [ "$#" -eq 0 ]; then
  set -- internal/migrations/sql/*.sql
fi

for file in "$@"; do
  current_pattern="$pattern"
  legacy_hash=''
  case "$file" in
    internal/migrations/sql/005_bulletin_asset_lifecycle.sql)
      legacy_hash='59734a4437169a147d569ab01402d5368f1a5b6fdc5f8c3e62789b2694073480'
      ;;
    internal/migrations/sql/006_news_publication_lifecycle.sql)
      legacy_hash='f299af0a5994995edb9e117340fa76c1b0425799967ef7699ecbc7989225e35e'
      ;;
  esac
  if [ -n "$legacy_hash" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      current_hash="$(sha256sum "$file" | cut -d ' ' -f 1)"
    else
      current_hash="$(shasum -a 256 "$file" | cut -d ' ' -f 1)"
    fi
    [ "$current_hash" = "$legacy_hash" ] || {
      echo "$file is immutable; add a new migration" >&2
      exit 1
    }
    current_pattern="$legacy_pattern"
  fi
  if perl -0777 -pe 's{/\*.*?\*/}{}gs; s/--[^\n]*//g' "$file" | tr '\n' ' ' | grep -Eiq "$current_pattern"; then
    echo 'migrations must use expand/contract; destructive DDL requires a later release' >&2
    exit 1
  fi
done
