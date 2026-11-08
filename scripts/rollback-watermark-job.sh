#!/usr/bin/env bash
# Roll the bulletin watermark worker back from the Event Job to the persistent
# Dapr App of the previous release (PLAN-v2 W5). Order favors availability:
#   1. validate every input before any write;
#   2. restore the App (unchanged App: verify only; changed App: copy the
#      previous revision; deleted App: recreate it from infra/main.bicep with
#      the previous worker image) and wait until it is ready with that digest;
#   3. pause Job admission (maxExecutions=0) if the Job exists, and read it back.
# The App and Job may overlap for a few seconds; database leases fence that.
set -euo pipefail
python3 scripts/deployment_config.py scope >/dev/null

[[ -n "${RESOURCE_GROUP:-}" && "${WATERMARK_WORKER_APP_NAME:-}" == hhc-web-bulletin-worker && "${WATERMARK_JOB_NAME:-}" == hhc-web-bulletin-watermark ]] || exit 1
[[ "${GITHUB_RUN_ID:-}" =~ ^[0-9]+$ && "${GITHUB_RUN_ATTEMPT:-}" =~ ^[0-9]+$ ]] || exit 1
valid_image() { python3 scripts/deployment_config.py image-digest api "$1" >/dev/null; }
previous_app_image="${PREVIOUS_WORKER_IMAGE_REF:-}"
previous_job_image="${PREVIOUS_WATERMARK_JOB_IMAGE_REF:-}"
for image in "$previous_app_image" "$previous_job_image"; do
  { [[ -z "$image" ]] || valid_image "$image"; } || { echo 'Invalid previous watermark worker image' >&2; exit 1; }
done
# The App and Job run the same watermark image; after a completed cutover only
# the Job's previous image exists, and it recreates the App unchanged.
target="${previous_app_image:-$previous_job_image}"
[[ -n "$target" ]] || { echo 'No previous watermark worker image was captured; leaving the Job running' >&2; exit 1; }

count() {
  local kind="$1" name="$2" value
  value="$(az containerapp $kind list -g "$RESOURCE_GROUP" --query "[?name=='${name}'] | length(@)" -o tsv --only-show-errors)"
  [[ "$value" == 0 || "$value" == 1 ]] || exit 1
  printf '%s\n' "$value"
}

ready_image() {
  local latest ready
  latest="$(az containerapp show -g "$RESOURCE_GROUP" -n "$WATERMARK_WORKER_APP_NAME" --query properties.latestRevisionName -o tsv --only-show-errors)"
  ready="$(az containerapp show -g "$RESOURCE_GROUP" -n "$WATERMARK_WORKER_APP_NAME" --query properties.latestReadyRevisionName -o tsv --only-show-errors)"
  [[ -n "$ready" && "$latest" == "$ready" ]] || return 0
  az containerapp revision show -g "$RESOURCE_GROUP" -n "$WATERMARK_WORKER_APP_NAME" --revision "$ready" \
    --query '{image:properties.template.containers[0].image,active:properties.active,health:properties.healthState,running:properties.runningState}' -o json --only-show-errors \
    | jq -r 'if .active == true and .health == "Healthy" and (.running == "Running" or .running == "RunningAtMaxScale" or .running == "ScaledToZero") then .image else empty end'
}

app_exists="$(count '' "$WATERMARK_WORKER_APP_NAME")"
job_exists="$(count job "$WATERMARK_JOB_NAME")"
if [[ "$app_exists" == 1 ]]; then
  # Releases no longer write the App, so a present App normally still runs the
  # captured revision. Anything else is restored by an exact revision copy.
  [[ -n "$previous_app_image" && -n "${PREVIOUS_WORKER_REVISION:-}" ]] || { echo 'Watermark worker App exists without a captured revision' >&2; exit 1; }
  if [[ "$(ready_image)" != "$previous_app_image" ]]; then
    bash "$(dirname "$0")/rollback-watermark-worker.sh"
    [[ "$(ready_image)" == "$target" ]] || { echo 'Restored watermark worker App is not healthy at the expected image' >&2; exit 1; }
  fi
  echo 'Watermark worker App retained at its previous revision'
else
  : "${IMAGE_REF:?}" "${RUNTIME_CPU:?}" "${RUNTIME_MEMORY:?}" "${BULLETIN_WATERMARK_PRIVACY_VERSION:?}"
  valid_image "$IMAGE_REF" || exit 1
  identity_enabled="${BULLETIN_WORKER_IDENTITY_ENABLED:-false}"
  asset_auth="${BULLETIN_WORKER_ASSET_AUTH:-dapr}"
  case "$identity_enabled:$asset_auth" in false:dapr|true:dapr|true:workload) ;; *) exit 1 ;; esac
  echo "Recreating watermark worker App with previous digest ${target##*@}"
  private_params="$(mktemp)"
  trap 'rm -f "$private_params"' EXIT
  python3 scripts/deployment_config.py parameters infra/main.bicep "$private_params"
  az deployment group create \
    --name "hhc-web-bulletin-worker-rollback-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}" \
    --resource-group "$RESOURCE_GROUP" \
    --template-file infra/main.bicep \
    --parameters "@$private_params" migrationImage="$IMAGE_REF" runtimeImage="$IMAGE_REF" watermarkWorkerImage="$target" runtimeCpu="$RUNTIME_CPU" runtimeMemory="$RUNTIME_MEMORY" bulletinWatermarkPrivacyVersion="$BULLETIN_WATERMARK_PRIVACY_VERSION" release="rollback-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}" deployRuntime=false deployMigrationJob=false deployWatermarkWorker=true deployWatermarkJob=false bulletinWorkerIdentityEnabled="$identity_enabled" bulletinWorkerAssetAuth="$asset_auth" bulletinExtractorAssetAudience="${BULLETIN_EXTRACTOR_ASSET_AUDIENCE:-}" provisionPermissions=false retainLegacyRuntimeSecret=false cmsTranslationEnabled=false \
    --only-show-errors --output none
  restored=false
  for attempt in {1..60}; do
    image="$(ready_image)"
    if [[ -n "$image" ]]; then
      [[ "$image" == "$target" ]] || { echo 'Recreated watermark worker App runs an unexpected image' >&2; exit 1; }
      restored=true
      break
    fi
    sleep 10
  done
  [[ "$restored" == true ]] || { echo 'Recreated watermark worker App did not become ready' >&2; exit 1; }
  min="$(az containerapp show -g "$RESOURCE_GROUP" -n "$WATERMARK_WORKER_APP_NAME" --query properties.template.scale.minReplicas -o tsv --only-show-errors)"
  [[ "$min" == 0 ]] || exit 1
fi

if [[ "$job_exists" == 1 ]]; then
  az containerapp job update -g "$RESOURCE_GROUP" -n "$WATERMARK_JOB_NAME" --max-executions 0 --only-show-errors --output none
  paused="$(az containerapp job show -g "$RESOURCE_GROUP" -n "$WATERMARK_JOB_NAME" --query properties.configuration.eventTriggerConfig.scale.maxExecutions -o tsv --only-show-errors)"
  [[ "$paused" == 0 ]] || { echo 'Watermark Job admission was not paused' >&2; exit 1; }
  echo 'Watermark Job admission paused (maxExecutions=0)'
fi
