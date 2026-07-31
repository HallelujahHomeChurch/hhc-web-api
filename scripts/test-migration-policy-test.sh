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
