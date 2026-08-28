#!/bin/sh
set -eu

file="${1:?what-if JSON is required}"

jq -e '
  ([.changes[] | select(.changeType == "Delete" or .changeType == "Unsupported")] | length == 0)
  and
  ([.changes[] as $change
    | $change
    | .. | objects
    | select(.propertyChangeType? == "Delete")
    | select((
        ($change.resourceId | endswith("/Microsoft.App/containerApps/hhc-web-api"))
        and (.path? == "properties.template.revisionSuffix")
      ) | not)
  ] | length == 0)
  and
  ([.changes[]
    | select(.changeType != "Ignore" and .changeType != "NoChange")
    | select((
        (.changeType == "Modify" and (.resourceId | endswith("/Microsoft.App/containerApps/hhc-web-api") or endswith("/Microsoft.App/jobs/hhc-web-migrate")))
        or
        ((.changeType == "Create" or .changeType == "Modify") and (.resourceId | endswith("/Microsoft.App/jobs/hhc-web-content-import")))
        or
        ((.changeType == "Create" or .changeType == "Modify") and (.resourceId | endswith("/Microsoft.CognitiveServices/accounts/bible-text-embedding-resource/raiPolicies/hhc-cms-translation-v1")))
      ) | not)
  ] | length == 0)
' "$file" >/dev/null
