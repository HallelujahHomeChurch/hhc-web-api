#!/usr/bin/env bash
# Verify the deployed watermark Event Job, prove it with a manual --preflight
# execution, and only then retire the persistent worker App (PLAN-v2 W5).
#   1. require this release's exact Job with admission paused (maxExecutions=0);
#   2. start one manual execution whose template is the Job's own container
#      with args overridden to --preflight, and wait (bounded) for Succeeded;
#   3. enable and verify admission, then log the App digest (rollback/ACR retention),
#      delete it and wait until it is gone.
# A Job that fails steps 1-2 is paused (maxExecutions=0) before exiting, so it
# cannot burn queue delivery attempts while the untouched App keeps serving.
# Failure after retirement begins requests worker recovery independently of API health.
set -euo pipefail
python3 scripts/deployment_config.py scope >/dev/null

[[ -n "${RESOURCE_GROUP:-}" && "${WATERMARK_WORKER_APP_NAME:-}" == hhc-web-bulletin-worker && "${WATERMARK_JOB_NAME:-}" == hhc-web-bulletin-watermark ]] || exit 1
python3 scripts/deployment_config.py image-digest api "${WATERMARK_IMAGE_REF:-}" >/dev/null
[[ "${GITHUB_SHA:-}" =~ ^[0-9a-f]{40}$ ]] || exit 1
max_executions="${WATERMARK_JOB_MAX_EXECUTIONS:-20}"
[[ "$max_executions" =~ ^([1-9]|1[0-9]|20)$ ]] || exit 1
poll_seconds="${WATERMARK_PREFLIGHT_POLL_SECONDS:-10}"
script_dir="$(cd "$(dirname "$0")" && pwd)"
work="$(mktemp -d)"

retiring=false

request_recovery() {
  [[ -z "${GITHUB_ENV:-}" ]] || echo 'WATERMARK_RECOVERY_REQUIRED=true' >> "$GITHUB_ENV"
}

pause_and_fail() {
  trap - EXIT
  request_recovery
  echo "$1" >&2
  az containerapp job update -g "$RESOURCE_GROUP" -n "$WATERMARK_JOB_NAME" --max-executions 0 --only-show-errors --output none || true
  paused="$(az containerapp job show -g "$RESOURCE_GROUP" -n "$WATERMARK_JOB_NAME" --query properties.configuration.eventTriggerConfig.scale.maxExecutions -o tsv --only-show-errors || true)"
  if [[ "$paused" == 0 ]]; then
    echo 'Watermark Job admission paused; the persistent App was not changed' >&2
  else
    echo '::error::Watermark Job admission could not be paused; run scripts/rollback-watermark-job.sh' >&2
  fi
  exit 1
}

on_exit() {
  local status=$?
  trap - EXIT
  rm -rf "$work"
  [[ "$status" == 0 ]] && return
  if [[ "$retiring" == true ]]; then
    request_recovery
    echo '::error::Watermark cutover failed during App retirement; worker recovery is required' >&2
    exit "$status"
  fi
  pause_and_fail 'Watermark cutover failed before App retirement'
}
trap on_exit EXIT

verify_job() {
  local expected_max="$1"
  az containerapp job show -g "$RESOURCE_GROUP" -n "$WATERMARK_JOB_NAME" -o json --only-show-errors > "$work/job.json"
  jq '{changes:[{resourceId:.id,changeType:"Modify",delta:[],after:.}]}' "$work/job.json" > "$work/job-verify.json"
  "$script_dir/check-what-if.sh" "$work/job-verify.json"
  jq -e --arg image "$WATERMARK_IMAGE_REF" --arg release "$GITHUB_SHA" --argjson max "$expected_max" '
    .properties.template.containers[0] as $container
    | $container.image == $image and $container.args == ["--once"]
    and ([$container.env[] | select(.name == "RELEASE") | .value] == [$release])
    and .properties.configuration.eventTriggerConfig.scale.maxExecutions == $max
    and ((.identity.userAssignedIdentities | keys | map(ascii_downcase | split("/") | last)) == ["hhc-web-bulletin-worker-identity"])
  ' "$work/job.json" >/dev/null
}
verify_job 0
echo 'Watermark Job matches this release; admission is paused'

# Same container (image, command, env incl. secret refs, resources); only args change.
jq '{containers:[.properties.template.containers[0] | {name,image,command,args:["--preflight"],env,resources:{cpu:.resources.cpu,memory:.resources.memory}}]}' \
  "$work/job.json" > "$work/preflight.yaml"
execution="$(az containerapp job start -g "$RESOURCE_GROUP" -n "$WATERMARK_JOB_NAME" --yaml "$work/preflight.yaml" --query name -o tsv --only-show-errors || true)"
[[ "$execution" =~ ^hhc-web-bulletin-watermark-[a-z0-9]+$ ]] || { echo 'Preflight execution did not start' >&2; exit 1; }
status=''
for attempt in $(seq 1 30); do
  status="$(az containerapp job execution show -g "$RESOURCE_GROUP" -n "$WATERMARK_JOB_NAME" --job-execution-name "$execution" --query properties.status -o tsv --only-show-errors)"
  case "$status" in
    Succeeded) break ;;
    Failed|Stopped|Degraded|Canceled|Cancelled) break ;;
  esac
  sleep "$poll_seconds"
done
if [[ "$status" != Succeeded ]]; then
  # The preflight logs only fixed per-check classes, never identifiers.
  az containerapp job logs show -g "$RESOURCE_GROUP" -n "$WATERMARK_JOB_NAME" --execution "$execution" --container bulletin-watermark --tail 20 --only-show-errors || true
  echo "Watermark Job preflight ${execution}: ${status:-timeout}" >&2
  exit 1
fi
echo "Watermark Job preflight ${execution} succeeded"
az containerapp job update -g "$RESOURCE_GROUP" -n "$WATERMARK_JOB_NAME" --max-executions "$max_executions" --only-show-errors --output none
verify_job "$max_executions"
echo 'Watermark Job admission enabled and verified'

apps="$(az containerapp list -g "$RESOURCE_GROUP" --query "[?name=='${WATERMARK_WORKER_APP_NAME}'] | length(@)" -o tsv --only-show-errors)"
case "$apps" in
  0) echo 'Persistent watermark worker App already retired'; exit 0 ;;
  1) ;;
  *) exit 1 ;;
esac
retired_image="$(az containerapp show -g "$RESOURCE_GROUP" -n "$WATERMARK_WORKER_APP_NAME" --query properties.template.containers[0].image -o tsv --only-show-errors)"
echo "Retiring persistent watermark worker App; last image (keep in ACR for rollback): ${retired_image##*@}"
retiring=true
az containerapp delete -g "$RESOURCE_GROUP" -n "$WATERMARK_WORKER_APP_NAME" --yes --only-show-errors --output none
for attempt in $(seq 1 30); do
  apps="$(az containerapp list -g "$RESOURCE_GROUP" --query "[?name=='${WATERMARK_WORKER_APP_NAME}'] | length(@)" -o tsv --only-show-errors)"
  [[ "$apps" == 0 ]] && { echo 'Persistent watermark worker App deleted'; exit 0; }
  sleep "$poll_seconds"
done
echo 'Persistent watermark worker App is still present' >&2
exit 1
