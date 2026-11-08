#!/bin/sh
set -eu

pattern='DROP[[:space:]]+(SCHEMA|TABLE|COLUMN|VIEW|MATERIALIZED[[:space:]]+VIEW|TYPE|FUNCTION|CONSTRAINT)|TRUNCATE|RENAME[[:space:]]+(TABLE|COLUMN)|ALTER[^;]*([[:space:]]TYPE[[:space:]]|DROP[[:space:]]+CONSTRAINT)|SET[[:space:]]+NOT[[:space:]]+NULL'
legacy_pattern='DROP[[:space:]]+(SCHEMA|TABLE|COLUMN|VIEW|MATERIALIZED[[:space:]]+VIEW|TYPE|FUNCTION)|TRUNCATE|RENAME[[:space:]]+(TABLE|COLUMN)|ALTER[^;]*[[:space:]]TYPE[[:space:]]|SET[[:space:]]+NOT[[:space:]]+NULL'

if [ "$#" -eq 0 ]; then
  set -- internal/migrations/sql/*.sql
fi

for file in "$@"; do
  current_pattern="$pattern"
  legacy_hash=''
  approved_destructive_hash=''
  case "$file" in
    internal/migrations/sql/005_bulletin_asset_lifecycle.sql)
      legacy_hash='59734a4437169a147d569ab01402d5368f1a5b6fdc5f8c3e62789b2694073480'
      ;;
    internal/migrations/sql/006_news_publication_lifecycle.sql)
      legacy_hash='f299af0a5994995edb9e117340fa76c1b0425799967ef7699ecbc7989225e35e'
      ;;
    internal/migrations/sql/022_five_content_locales.sql)
      legacy_hash='7e81c0f7a3c499926206a65db07724e0aa15828d37ed1b4005b8318596d1b4d2'
      ;;
    internal/migrations/sql/027_locations_and_content_modules.sql)
      legacy_hash='52dd9dd0e8a83f08ca1a581b45fe5923fa261bd66499a76e0d185584e2c3cdea'
      ;;
    internal/migrations/sql/030_home_page_v2.sql)
      legacy_hash='33e51ddf7b022e2b2807840bf5e48b517f2b58840982af3e9aefccfd6dc66173'
      ;;
    internal/migrations/sql/031_page_group_publication.sql)
      legacy_hash='9f9134d27ccdf6fad7242980fdd0da9fdb6694907b919d60bb457fee180ffb8b'
      ;;
    internal/migrations/sql/068_recording_retention_projection.sql)
      # Expand-only CHECK replacement and additive projection fields. Old
      # ready+30 writes remain valid; no column or recording is removed.
      legacy_hash='12438c2caaade3841fbbe74e0f191a68ec818d30fc5a27958198ec62f78cf5cb'
      ;;
    internal/migrations/sql/093_broadcast_source_identity.sql)
      # Expand-only PK replacement: retain every row, key and signing seed.
      # WHERE singleton still identifies exactly the legacy row; a real PG
      # migration test proves old selectors and target references survive.
      legacy_hash='d0bdf8f93d64a414fa569cf17f1de5b808f96c9d5cbc2e3ace46594e106fb7c8'
      ;;
    internal/migrations/sql/086_online_bulletin_upload_source_identity.sql)
      # Expand-only UNIQUE replacement: every old key remains valid; upload
      # objects gain independent immutable evidence. No rows are removed.
      legacy_hash='1df7c4084c87ee66cccbc1c6e5311ed753725909e804a1e5c8e76aeae82f941e'
      ;;
    internal/migrations/sql/043_bulletin_investigation_refinement.sql)
      approved_destructive_hash='1f2963221acd3e3ca6b3bb7a9338f3942d6a51e26a229a178bd1478e268fc399'
      ;;
    internal/migrations/sql/047_bulletin_investigation_batches_and_download_progress.sql)
      approved_destructive_hash='4137277dd30ee556ad9f5da7d2b1f8f5079403bea0259acfc512ca62b6ad6e2f'
      ;;
    internal/migrations/sql/050_retire_operations_kernel.sql)
      approved_destructive_hash='38ce871d61d7b57d35f196e3c96ae9c9035c527aac1fdd6d9c0d69bcf6f2c15b'
      ;;
    internal/migrations/sql/057_bulletin_edition_composite_keys.sql)
      approved_destructive_hash='181218eeb504ec6a678ebb1083ff7ee347e7987e2a49b8d291cfe0e78c6494c5'
      ;;
    internal/migrations/sql/101_retire_legacy_dsr_schema.sql)
      # Guarded later contraction; exact rows and reader releases are separate gates.
      approved_destructive_hash='b05c113cb10d0ec4208fdf6baf3014f30ee76ea429c83adef5f66dd5bf06b668'
      ;;
  esac
  if [ -n "$approved_destructive_hash" ]; then
    if command -v sha256sum >/dev/null 2>&1; then
      current_hash="$(sha256sum "$file" | cut -d ' ' -f 1)"
    else
      current_hash="$(shasum -a 256 "$file" | cut -d ' ' -f 1)"
    fi
    [ "$current_hash" = "$approved_destructive_hash" ] || {
      echo "$file is immutable; add a new migration" >&2
      exit 1
    }
    continue
  fi
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
