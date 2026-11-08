#!/usr/bin/env bash
set -euo pipefail
[[ -n "${RESOURCE_GROUP:-}" ]] || exit 1
previous="${PREVIOUS_EXTRACTOR_IMAGE_REF:-}"
python3 scripts/deployment_config.py image-digest extractor "$previous" >/dev/null
: "${PREVIOUS_EXTRACTOR_SNAPSHOT:?}"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
# Restore the captured trigger and environment as well as its immutable image.
# Returning to Schedule with the old image must not leave the minute cron removed.
scope="$(python3 scripts/deployment_config.py scope)"
jq -e --arg image "$previous" --arg scope "${scope}/providers/Microsoft.App/jobs/hhc-web-bulletin-extractor" '
 select(((.id | ascii_downcase) == ($scope | ascii_downcase)))
 | select(.properties.configuration.triggerType == "Schedule" or .properties.configuration.triggerType == "Event")
 | select(all(.properties.configuration.secrets[]; (.value // "") == ""))
 | select(.properties.configuration.dapr == null and ((.properties.configuration.identitySettings // []) | length) == 0)
 | select(all(.properties.template.containers[]; .imageType == null or .imageType == "ContainerImage"))
 | {properties:{configuration:(.properties.configuration | del(.dapr,.identitySettings)),template:.properties.template}}
 | .properties.template.containers |= map(del(.imageType))
 | .properties.configuration.scheduleTriggerConfig = (.properties.configuration.scheduleTriggerConfig // null)
 | .properties.configuration.eventTriggerConfig = (.properties.configuration.eventTriggerConfig // null)
 | .properties.template.containers[0].image = $image
' "$PREVIOUS_EXTRACTOR_SNAPSHOT" > "$work/payload.json"
enabled="$(jq -r '.properties.template.containers[0].env[] | select(.name=="BULLETIN_EXTRACTION_ENABLED") | .value' "$work/payload.json")"
[[ "$enabled" == true || "$enabled" == false ]] || exit 1
id="$(jq -r .id "$PREVIOUS_EXTRACTOR_SNAPSHOT")"
az rest --method PATCH --url "https://management.azure.com${id}?api-version=2025-01-01" --body "@$work/payload.json" --only-show-errors --output none
for attempt in {1..60}; do
 az containerapp job show -g "$RESOURCE_GROUP" -n hhc-web-bulletin-extractor --only-show-errors -o json > "$work/restored.json"
 state="$(jq -r .properties.provisioningState "$work/restored.json")"
 case "$state" in Succeeded) break ;; Failed|Canceled) exit 1 ;; esac
 sleep 10
done
[[ "$state" == Succeeded ]]
jq -e --slurpfile target "$work/payload.json" '
 {configuration:(.properties.configuration | del(.dapr,.identitySettings)),
  template:(.properties.template | .containers |= map(del(.imageType)))}
 | .configuration.scheduleTriggerConfig = (.configuration.scheduleTriggerConfig // null)
 | .configuration.eventTriggerConfig = (.configuration.eventTriggerConfig // null)
 | . == $target[0].properties
' "$work/restored.json" >/dev/null
echo 'Extractor trigger, image and activation restored'
