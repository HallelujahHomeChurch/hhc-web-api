#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/maintenance-contract.sh"
: "${DEPLOYED_COMMIT:?}" "${DEPLOYED_IMAGE:?}" "${DEPLOYED_REVISION:?}" "${DEPLOYED_ATTEMPT:?}" "${DEPLOYED_PUBLIC_STATE:?}"
: "${DEPLOYED_WORKER_IMAGE:?}" "${DEPLOYED_WORKER_JOB:?}" "${DEPLOYED_EXTRACTOR_IMAGE:?}" "${DEPLOYED_EXTRACTOR_ENABLED:?}"
: "${GITHUB_SHA:?}" "${GITHUB_RUN_ID:?}" "${GITHUB_RUN_ATTEMPT:?}" "${GITHUB_REPOSITORY:?}" "${GITHUB_OUTPUT:?}"
[[ "$GITHUB_REPOSITORY" == HallelujahHomeChurch/hhc-web-api && "${GITHUB_REF:-}" == refs/heads/main ]] || exit 1
DEPLOYED_RUN_ID="${DEPLOYED_RUN_ID:-$GITHUB_RUN_ID}"
[[ "$DEPLOYED_COMMIT" =~ ^[a-f0-9]{40}$ && "$GITHUB_SHA" =~ ^[a-f0-9]{40}$ && "$DEPLOYED_RUN_ID" =~ ^[1-9][0-9]*$ ]] || exit 1
if [[ "$DEPLOYED_COMMIT" != "$GITHUB_SHA" ]]; then
  [[ "$DEPLOYED_RUN_ID" != "$GITHUB_RUN_ID" ]] || exit 1
  git merge-base --is-ancestor "$DEPLOYED_COMMIT" HEAD
fi
api_digest="$(python3 scripts/deployment_config.py image-digest api "$DEPLOYED_IMAGE")"
[[ "$DEPLOYED_REVISION" =~ ^hhc-web-api--[a-z0-9-]+$ ]] || exit 1
[[ "$GITHUB_RUN_ID" =~ ^[1-9][0-9]*$ && "$DEPLOYED_ATTEMPT" =~ ^[1-9][0-9]*$ && "$GITHUB_RUN_ATTEMPT" =~ ^[1-9][0-9]*$ ]] || exit 1
(( DEPLOYED_ATTEMPT <= GITHUB_RUN_ATTEMPT )) || exit 1
worker_digest="$(python3 scripts/deployment_config.py image-digest api "$DEPLOYED_WORKER_IMAGE")"
[[ "$DEPLOYED_WORKER_IMAGE" != "$DEPLOYED_IMAGE" ]] || exit 1
# The watermark worker is the Event Job pinned to this release (PLAN-v2 W5).
[[ "$DEPLOYED_WORKER_JOB" == "hhc-web-bulletin-watermark@${DEPLOYED_COMMIT}" ]] || exit 1
extractor_digest="$(python3 scripts/deployment_config.py image-digest extractor "$DEPLOYED_EXTRACTOR_IMAGE")"
[[ "$DEPLOYED_EXTRACTOR_ENABLED" == true || "$DEPLOYED_EXTRACTOR_ENABLED" == false ]] || exit 1
case "$DEPLOYED_PUBLIC_STATE" in accepted|pending) ;; *) exit 1 ;; esac
# Successful upstream outputs are retained by failed-job retries; absence is fatal.
gh api "repos/$GITHUB_REPOSITORY/actions/runs/$DEPLOYED_RUN_ID/attempts/$DEPLOYED_ATTEMPT/jobs?per_page=100" \
  | jq -e --arg sha "$DEPLOYED_COMMIT" '[.jobs[] | select(.name == "deploy")] | length == 1 and all(.[]; .conclusion == "success" and .head_sha == $sha)' >/dev/null

identity() {
  az containerapp show -g "$RESOURCE_GROUP" -n "$API_APP_NAME" --query '{latest:properties.latestRevisionName,ready:properties.latestReadyRevisionName}' -o json --only-show-errors \
    | jq -e --arg revision "$DEPLOYED_REVISION" '.latest == $revision and .ready == $revision' >/dev/null
  az containerapp revision show -g "$RESOURCE_GROUP" -n "$API_APP_NAME" --revision "$DEPLOYED_REVISION" \
    --query '{active:properties.active,health:properties.healthState,running:properties.runningState,traffic:properties.trafficWeight,image:properties.template.containers[0].image}' -o json --only-show-errors \
    | jq -e --arg image "$DEPLOYED_IMAGE" '.active == true and .health == "Healthy" and .running == "Running" and .traffic == 100 and .image == $image' >/dev/null
  az containerapp job show -g "$RESOURCE_GROUP" -n hhc-web-bulletin-watermark \
    --query "{image:properties.template.containers[0].image,args:properties.template.containers[0].args,release:properties.template.containers[0].env[?name=='RELEASE'].value,max:properties.configuration.eventTriggerConfig.scale.maxExecutions}" -o json --only-show-errors \
    | jq -e --arg image "$DEPLOYED_WORKER_IMAGE" --arg release "$DEPLOYED_COMMIT" '.image == $image and .args == ["--once"] and .release == [$release] and .max == 20' >/dev/null
  # The persistent worker App was retired by the cutover; its return means a rollback.
  az containerapp list -g "$RESOURCE_GROUP" --query "[?name=='hhc-web-bulletin-worker'] | length(@)" -o json --only-show-errors \
    | jq -e '. == 0' >/dev/null
  az containerapp job show -g "$RESOURCE_GROUP" -n hhc-web-bulletin-extractor \
    --query "{image:properties.template.containers[0].image,enabled:properties.template.containers[0].env[?name=='BULLETIN_EXTRACTION_ENABLED'].value,trigger:properties.configuration.triggerType,max:properties.configuration.eventTriggerConfig.scale.maxExecutions}" -o json --only-show-errors \
    | jq -e --arg image "$DEPLOYED_EXTRACTOR_IMAGE" --arg enabled "$DEPLOYED_EXTRACTOR_ENABLED" '.image == $image and .enabled == [$enabled] and .trigger == "Event" and .max == 1' >/dev/null
}
if [[ "$DEPLOYED_PUBLIC_STATE" == pending || "$DEPLOYED_ATTEMPT" != "$GITHUB_RUN_ATTEMPT" ]]; then
  identity
  mode="$(observed_gateway_mode)"
  if [[ "$mode" == on && "$DEPLOYED_PUBLIC_STATE" == pending ]]; then
    maintenance_request 'https://www.alive.org.tw/api/site-layout?locale=zh-Hant'
    identity
    echo '::error::PUBLIC_ACCEPTANCE_PENDING: maintenance remains on; reopen only when approved, then rerun failed jobs (not all jobs).'
    exit 42
  fi
  [[ "$mode" == off ]] || exit 1
  RELEASE_PUBLIC_EXPECTATION=normal SMOKE_MODE=forward ./scripts/smoke-release.sh
  APP_NAME="$API_APP_NAME" IMAGE_REF="$DEPLOYED_IMAGE" CANDIDATE_REVISION="$DEPLOYED_REVISION" \
    RELEASE_PUBLIC_EXPECTATION=normal SMOKE_MODE=forward ./scripts/verify-rollout.sh
  require_gateway_mode off
  identity
fi
# No accepted outputs or artifact exist until the complete public gate succeeds.
mkdir -p "$ROLLOUT_EVIDENCE_DIR"
jq -n --arg commit "$DEPLOYED_COMMIT" --arg image "$api_digest" --arg revision "$DEPLOYED_REVISION" \
  --arg workerImage "$worker_digest" --arg workerJob "$DEPLOYED_WORKER_JOB" \
  --arg extractorImage "$extractor_digest" --arg extractorEnabled "$DEPLOYED_EXTRACTOR_ENABLED" \
  --arg run "$GITHUB_RUN_ID" --arg deploymentRun "$DEPLOYED_RUN_ID" --arg verificationSource "$GITHUB_SHA" --arg deploymentAttempt "$DEPLOYED_ATTEMPT" --arg acceptanceAttempt "$GITHUB_RUN_ATTEMPT" \
  '{state:"accepted",commit:$commit,imageDigest:$image,revision:$revision,workerDigest:$workerImage,workerJob:$workerJob,extractorDigest:$extractorImage,extractorEnabled:$extractorEnabled,run:$run,deploymentRun:$deploymentRun,verificationSource:$verificationSource,deploymentAttempt:$deploymentAttempt,acceptanceAttempt:$acceptanceAttempt}' \
  > "$ROLLOUT_EVIDENCE_DIR/acceptance.json"
printf 'commit=%s\nimage_digest=%s\nrevision=%s\ndeployment-attempt=%s\n' \
  "$DEPLOYED_COMMIT" "$api_digest" "$DEPLOYED_REVISION" "$DEPLOYED_ATTEMPT" >> "$GITHUB_OUTPUT"
printf "extractor_digest=%s\n" "$extractor_digest" >> "$GITHUB_OUTPUT"
printf "acceptance-attempt=%s\n" "$GITHUB_RUN_ATTEMPT" >> "$GITHUB_OUTPUT"
printf "deployment-run=%s\n" "$DEPLOYED_RUN_ID" >> "$GITHUB_OUTPUT"
