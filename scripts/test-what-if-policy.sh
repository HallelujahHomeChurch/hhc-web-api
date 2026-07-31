#!/bin/sh
set -eu

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/safe.json" <<'JSON'
{"changes":[
  {"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers","propertyChangeType":"Array","children":[{"path":"image","propertyChangeType":"Modify"}]}]},
  {"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.App/jobs/hhc-web-migrate","changeType":"Modify","delta":[]},
  {"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.KeyVault/vaults/other","changeType":"Ignore","delta":[]}
]}
JSON
./scripts/check-what-if.sh "$tmp/safe.json"

cat >"$tmp/nested-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers","propertyChangeType":"Array","children":[{"path":"env","propertyChangeType":"Delete"}]}]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/nested-delete.json" 2>/dev/null; then
  echo "nested delete was not rejected" >&2
  exit 1
fi

cat >"$tmp/unrelated-modify.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.KeyVault/vaults/alive-vault","changeType":"Modify","delta":[]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/unrelated-modify.json" 2>/dev/null; then
  echo "unrelated infrastructure modification was not rejected" >&2
  exit 1
fi
