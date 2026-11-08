#!/usr/bin/env bash
set -euo pipefail
python3 scripts/deployment_config.py scope >/dev/null
[[ -n "${RESOURCE_GROUP:-}" && "$WATERMARK_WORKER_APP_NAME" == hhc-web-bulletin-worker ]] || exit 1
[[ "$PREVIOUS_WORKER_REVISION" == "$WATERMARK_WORKER_APP_NAME"--* ]] || exit 1
python3 scripts/deployment_config.py image-digest api "${PREVIOUS_WORKER_IMAGE_REF:-}" >/dev/null
[[ "$GITHUB_RUN_ID" =~ ^[0-9]+$ && "$GITHUB_RUN_ATTEMPT" =~ ^[0-9]+$ ]] || exit 1
suffix="rollback-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}"
revision="${WATERMARK_WORKER_APP_NAME}--${suffix}"
az containerapp revision copy -g "$RESOURCE_GROUP" -n "$WATERMARK_WORKER_APP_NAME" \
  --from-revision "$PREVIOUS_WORKER_REVISION" --image "$PREVIOUS_WORKER_IMAGE_REF" \
  --revision-suffix "$suffix" --only-show-errors --output none
for attempt in {1..60}; do
  ready="$(az containerapp show -g "$RESOURCE_GROUP" -n "$WATERMARK_WORKER_APP_NAME" --query properties.latestReadyRevisionName -o tsv --only-show-errors)"
  if [[ "$ready" == "$revision" ]]; then
    image="$(az containerapp revision show -g "$RESOURCE_GROUP" -n "$WATERMARK_WORKER_APP_NAME" --revision "$revision" --query properties.template.containers[0].image -o tsv --only-show-errors)"
    [[ "$image" == "$PREVIOUS_WORKER_IMAGE_REF" ]] || exit 1
    exit 0
  fi
  sleep 10
done
echo 'Watermark worker rollback did not become ready' >&2
exit 1
