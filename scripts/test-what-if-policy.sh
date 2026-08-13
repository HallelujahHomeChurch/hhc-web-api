#!/bin/sh
set -eu

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/safe.json" <<'JSON'
{"changes":[
  {"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers","propertyChangeType":"Array","children":[{"path":"image","propertyChangeType":"Modify"}]}]},
  {"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.App/jobs/hhc-web-migrate","changeType":"Modify","delta":[]},
  {"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.CognitiveServices/accounts/bible-text-embedding-resource/raiPolicies/hhc-cms-translation-v1","changeType":"Create","delta":[]},
  {"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.KeyVault/vaults/other","changeType":"Ignore","delta":[]}
]}
JSON
./scripts/check-what-if.sh "$tmp/safe.json"

cat >"$tmp/translation-disabled-retained.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers[0].env[?name=='CMS_TRANSLATION_ENABLED'].value","propertyChangeType":"Modify"},{"path":"properties.template.containers[0].env[?name=='AZURE_OPENAI_ENDPOINT']","propertyChangeType":"NoEffect"},{"path":"properties.configuration.secrets[?name=='azure-openai-api-key']","propertyChangeType":"NoEffect"}]}]}
JSON
./scripts/check-what-if.sh "$tmp/translation-disabled-retained.json"

cat >"$tmp/translation-first-disabled-unbound.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers[0].env[?name=='CMS_TRANSLATION_ENABLED']","propertyChangeType":"Add"}]}]}
JSON
./scripts/check-what-if.sh "$tmp/translation-first-disabled-unbound.json"

cat >"$tmp/nested-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers","propertyChangeType":"Array","children":[{"path":"env","propertyChangeType":"Delete"}]}]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/nested-delete.json" 2>/dev/null; then
  echo "nested delete was not rejected" >&2
  exit 1
fi

cat >"$tmp/translation-binding-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.configuration.secrets[?name=='azure-openai-api-key']","propertyChangeType":"Delete"}]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/translation-binding-delete.json" 2>/dev/null; then
  echo "translation binding delete was not rejected" >&2
  exit 1
fi

cat >"$tmp/unrelated-modify.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.KeyVault/vaults/alive-vault","changeType":"Modify","delta":[]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/unrelated-modify.json" 2>/dev/null; then
  echo "unrelated infrastructure modification was not rejected" >&2
  exit 1
fi

cat >"$tmp/unrelated-ai-policy.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/alive/providers/Microsoft.CognitiveServices/accounts/bible-text-embedding-resource/raiPolicies/other-policy","changeType":"Create","delta":[]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/unrelated-ai-policy.json" 2>/dev/null; then
  echo "unrelated AI policy creation was not rejected" >&2
  exit 1
fi
