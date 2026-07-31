#!/bin/sh
set -eu

file="${1:?what-if JSON is required}"

jq -e '
  ([.changes[] | select(.changeType == "Delete" or .changeType == "Unsupported")] | length == 0)
  and
  ([.changes[] | .. | objects | select(.propertyChangeType? == "Delete")] | length == 0)
  and
  ([.changes[]
    | select(.changeType != "Ignore" and .changeType != "NoChange")
    | select(
        .changeType != "Modify"
        or ((.resourceId | endswith("/Microsoft.App/containerApps/hhc-web-api") or endswith("/Microsoft.App/jobs/hhc-web-migrate")) | not)
      )
  ] | length == 0)
' "$file" >/dev/null
