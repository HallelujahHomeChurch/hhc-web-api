#!/bin/sh
set -eu

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

printf '%s\n' 'DROP INDEX IF EXISTS old_index;' >"$tmp/safe.sql"
./scripts/test-migration-policy.sh "$tmp/safe.sql"

printf '%s\n' 'DROP' 'TABLE users;' >"$tmp/destructive.sql"
if ./scripts/test-migration-policy.sh "$tmp/destructive.sql" 2>/dev/null; then
  echo 'multiline destructive migration was not rejected' >&2
  exit 1
fi

printf '%s\n' 'ALTER TABLE users ALTER COLUMN name SET NOT NULL;' >"$tmp/not-null.sql"
if ./scripts/test-migration-policy.sh "$tmp/not-null.sql" 2>/dev/null; then
  echo 'blocking not-null migration was not rejected' >&2
  exit 1
fi

printf '%s\n' 'ALTER TABLE users ALTER COLUMN name TYPE text;' >"$tmp/alter-type.sql"
if ./scripts/test-migration-policy.sh "$tmp/alter-type.sql" 2>/dev/null; then
  echo 'blocking type migration was not rejected' >&2
  exit 1
fi

printf '%s\n' 'DROP /* bypass */ TABLE users;' >"$tmp/comment-bypass.sql"
if ./scripts/test-migration-policy.sh "$tmp/comment-bypass.sql" 2>/dev/null; then
  echo 'comment-obfuscated destructive migration was not rejected' >&2
  exit 1
fi

printf '%s\n' 'DROP VIEW current_news;' >"$tmp/drop-view.sql"
if ./scripts/test-migration-policy.sh "$tmp/drop-view.sql" 2>/dev/null; then
  echo 'view drop migration was not rejected' >&2
  exit 1
fi

printf '%s\n' 'ALTER TABLE users DROP CONSTRAINT users_name_key;' >"$tmp/drop-constraint.sql"
if ./scripts/test-migration-policy.sh "$tmp/drop-constraint.sql" 2>/dev/null; then
  echo 'constraint drop migration was not rejected' >&2
  exit 1
fi

locale_migration='internal/migrations/sql/022_five_content_locales.sql'
./scripts/test-migration-policy.sh "$locale_migration"

locale_backup="$tmp/022_five_content_locales.sql"
cp "$locale_migration" "$locale_backup"
restore_locale_migration() {
  cp "$locale_backup" "$locale_migration"
}
trap 'restore_locale_migration; rm -rf "$tmp"' EXIT
printf '%s\n' '-- test mutation' >>"$locale_migration"
if ./scripts/test-migration-policy.sh "$locale_migration" 2>/dev/null; then
  echo 'five-locale constraint replacement was not immutable' >&2
  exit 1
fi

location_migration='internal/migrations/sql/027_locations_and_content_modules.sql'
restore_locale_migration
./scripts/test-migration-policy.sh "$location_migration"

location_backup="$tmp/027_locations_and_content_modules.sql"
cp "$location_migration" "$location_backup"
restore_location_migration() {
  cp "$location_backup" "$location_migration"
}
trap 'restore_locale_migration; restore_location_migration; rm -rf "$tmp"' EXIT
printf '%s\n' '-- test mutation' >>"$location_migration"
if ./scripts/test-migration-policy.sh "$location_migration" 2>/dev/null; then
  echo 'locations constraint replacement was not immutable' >&2
  exit 1
fi

home_migration='internal/migrations/sql/030_home_page_v2.sql'
restore_location_migration
./scripts/test-migration-policy.sh "$home_migration"

home_backup="$tmp/030_home_page_v2.sql"
cp "$home_migration" "$home_backup"
restore_home_migration() {
  cp "$home_backup" "$home_migration"
}
trap 'restore_locale_migration; restore_location_migration; restore_home_migration; rm -rf "$tmp"' EXIT
printf '%s\n' '-- test mutation' >>"$home_migration"
if ./scripts/test-migration-policy.sh "$home_migration" 2>/dev/null; then
  echo 'Home v2 constraint replacement was not immutable' >&2
  exit 1
fi

page_group_migration='internal/migrations/sql/031_page_group_publication.sql'
restore_home_migration
./scripts/test-migration-policy.sh "$page_group_migration"

page_group_backup="$tmp/031_page_group_publication.sql"
cp "$page_group_migration" "$page_group_backup"
restore_page_group_migration() {
  cp "$page_group_backup" "$page_group_migration"
}
trap 'restore_locale_migration; restore_location_migration; restore_home_migration; restore_page_group_migration; rm -rf "$tmp"' EXIT
printf '%s\n' '-- test mutation' >>"$page_group_migration"
if ./scripts/test-migration-policy.sh "$page_group_migration" 2>/dev/null; then
  echo 'Page-group constraint replacement was not immutable' >&2
  exit 1
fi
