#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "$0")/maintenance-contract.sh"
umask 077
: "${RESOURCE_GROUP:?}" "${APP_NAME:?}" "${IMAGE_REF:?}" "${CANDIDATE_REVISION:?}" "${ROLLOUT_EVIDENCE_DIR:?}"
case "$APP_NAME" in api-gateway|hhc-web-api) ;; *) exit 1 ;; esac
mode="${SMOKE_MODE:-forward}"
case "$mode" in forward|rollback) ;; *) exit 1 ;; esac
mkdir -p "$ROLLOUT_EVIDENCE_DIR"
scratch="$(mktemp -d)"
stage=initializing
finish() {
  code=$?
  jq -nc --arg time "$(date -u +%FT%TZ)" --arg stage "$stage" --argjson exit_code "$code" \
    '{time:$time,stage:$stage,exit_code:$exit_code}' >>"$ROLLOUT_EVIDENCE_DIR/result.jsonl"
  rm -rf "$scratch"
}
trap finish EXIT

snapshot() {
  local label="$1" expected_dapr
  stage="candidate-$label"
  az containerapp revision show -g "$RESOURCE_GROUP" -n "$APP_NAME" --revision "$CANDIDATE_REVISION" \
    --query "{active:properties.active,health:properties.healthState,running:properties.runningState,traffic:properties.trafficWeight,min:properties.template.scale.minReplicas,containers:properties.template.containers[].{name:name,image:image,port:env[?name=='PORT'].{value:value,secretRef:secretRef},daprMode:env[?name=='CMS_DAPR_ENABLED'].{value:value,secretRef:secretRef},daprPort:env[?name=='DAPR_HTTP_PORT'].{value:value,secretRef:secretRef},audit:env[?name=='AUDIT_DISPATCH_ENABLED'].{value:value,secretRef:secretRef}}}" \
    -o json --only-show-errors >"$ROLLOUT_EVIDENCE_DIR/revision-$label.json"
  jq -e --arg image "$IMAGE_REF" --arg app "$APP_NAME" --arg mode "$mode" '
    .active == true and .health == "Healthy" and .running == "Running"
    and (.min | type == "number" and . >= 1)
    and any(.containers[]; .name == $app and .image == $image)
    and ($mode != "rollback" or .traffic == 100)
  ' "$ROLLOUT_EVIDENCE_DIR/revision-$label.json" >/dev/null
  expected_dapr=true
  if [[ "$APP_NAME" == hhc-web-api ]]; then
    expected_dapr="$(jq -er '
      [.containers[] | select(.name == "hhc-web-api")] | select(length == 1) | .[0].daprMode // [] |
      if . == [] or . == [{"value":"true","secretRef":null}] then "true"
      elif . == [{"value":"false","secretRef":null}] then "false"
      else error("invalid CMS sidecar mode") end
    ' "$ROLLOUT_EVIDENCE_DIR/revision-$label.json")"
  fi
  if [[ "$mode" == forward ]]; then
    az containerapp show -g "$RESOURCE_GROUP" -n "$APP_NAME" \
      --query '{latest:properties.latestRevisionName,ready:properties.latestReadyRevisionName,mode:properties.configuration.activeRevisionsMode,dapr:properties.configuration.dapr}' \
      -o json --only-show-errors >"$ROLLOUT_EVIDENCE_DIR/app-$label.json"
    jq -e --arg revision "$CANDIDATE_REVISION" '.latest == $revision and .ready == $revision' "$ROLLOUT_EVIDENCE_DIR/app-$label.json" >/dev/null
    if [[ "$APP_NAME" == hhc-web-api ]]; then
      # ACA injects port 3500. Reject overrides; readiness only proves local initialization.
      jq -e --argjson expected "$expected_dapr" '.mode == "Single" and .dapr.enabled == $expected
        and ($expected == false or (.dapr.appId == "hhc-web-api"
        and .dapr.appPort == 8082 and .dapr.appProtocol == "http"))' "$ROLLOUT_EVIDENCE_DIR/app-$label.json" >/dev/null
      jq -e '[.containers[] | select(.name == "hhc-web-api")] | length == 1 and all(.[];
        .port == [{"value":"8082","secretRef":null}]
        and .audit == [{"value":"true","secretRef":null}]
        and (.daprPort == [] or (.daprPort | length == 1 and all(.[]; .value == "3500" and .secretRef == null))))'         "$ROLLOUT_EVIDENCE_DIR/revision-$label.json" >/dev/null
    fi
  fi
  az containerapp replica list -g "$RESOURCE_GROUP" -n "$APP_NAME" --revision "$CANDIDATE_REVISION" \
    --query '[].{name:name,containers:properties.containers[].{name:name,ready:ready,started:started,restarts:restartCount}}' \
    -o json --only-show-errors >"$ROLLOUT_EVIDENCE_DIR/replicas-$label.json"
  jq -e --arg app "$APP_NAME" --arg mode "$mode" --argjson expected "$expected_dapr" --slurpfile revision "$ROLLOUT_EVIDENCE_DIR/revision-$label.json" '
    length >= $revision[0].min and all(.[];
      (.name | type == "string" and length > 0)
      and any(.containers[]; .name == $app)
      and (([.containers[] | select(.name == "daprd")] | length) == (if $expected then 1 else 0 end))
      and all(.containers[]; .ready == true and .started == true
        and (.restarts | type == "number" and . >= 0)
        and ($mode == "rollback" or .restarts == 0)))
  ' "$ROLLOUT_EVIDENCE_DIR/replicas-$label.json" >/dev/null
  if [[ "$label" == end ]]; then
    jq -e --slurpfile before "$ROLLOUT_EVIDENCE_DIR/replicas-start.json" '
      . as $after |
      all($before[0][]; .name as $previous | any($after[]; .name == $previous))
      and all(.[]; . as $replica | all(.containers[]; . as $container |
        .restarts <= ([$before[0][] | select(.name == $replica.name) | .containers[] | select(.name == $container.name) | .restarts][0] // 0)))
    ' "$ROLLOUT_EVIDENCE_DIR/replicas-end.json" >/dev/null
  fi
}

sample() {
  local label="$1" url="$2" metrics status latency code=0 request_id
  stage="public-$label"
  : >"$scratch/headers"
  : >"$scratch/body"
  metrics="$(curl --silent --max-time 5 --dump-header "$scratch/headers" --output "$scratch/body" --write-out '%{http_code} %{time_total}' "$url")" || code=$?
  read -r status latency <<<"$metrics"
  [[ "$status" =~ ^[0-9]{3}$ ]] || status=000
  [[ "$latency" =~ ^[0-9]+([.][0-9]+)?$ ]] || latency=null
  request_id="$(awk 'tolower($1)=="x-hhc-request-id:" {gsub(/\r/,"",$2);print $2}' "$scratch/headers" | tail -1)"
  [[ "$request_id" =~ ^[a-f0-9]{32}$ ]] || request_id=''
  jq -nc --arg time "$(date -u +%FT%TZ)" --arg route "$label" --arg status "$status" --argjson latency "$latency" --argjson transport_error "$code" --arg request_id "$request_id" \
    '{time:$time,route:$route,status:($status|tonumber),latency:$latency,transport_error:$transport_error,request_id:$request_id}' >>"$ROLLOUT_EVIDENCE_DIR/samples.jsonl"
  if [[ "$label" == cms && "$public_expectation" == maintenance ]]; then
    [[ "$code" == 0 ]] && maintenance_response "$status" "$scratch/headers" "$scratch/body"
    return
  fi
  [[ "$code" == 0 && "$status" == 200 ]] || return 1
  case "$label" in
    ready) jq -e '.status == "ready"' "$scratch/body" >/dev/null ;;
    cms) jq -e '.error == null and (.data | type == "object") and (.meta | type == "object")' "$scratch/body" >/dev/null ;;
    oidc) jq -e '.issuer == "https://account.alive.org.tw" and .jwks_uri == "https://account.alive.org.tw/.well-known/jwks.json"' "$scratch/body" >/dev/null ;;
  esac
}

[[ "$public_expectation" != maintenance ]] || require_gateway_mode on
snapshot start
for round in {1..30}; do
  sample ready https://www.alive.org.tw/ready
  sample cms 'https://www.alive.org.tw/api/site-layout?locale=zh-Hant'
  sample oidc https://account.alive.org.tw/.well-known/openid-configuration
  sleep 2
done
snapshot end
if [[ "$public_expectation" == maintenance ]]; then
  require_gateway_mode on
  stage=runtime-verified-public-pending
else
  stage=passed
fi
