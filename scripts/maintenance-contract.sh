#!/usr/bin/env bash
# Shared smoke expectation; never infer maintenance from an HTTP failure.
public_expectation="${RELEASE_PUBLIC_EXPECTATION:-normal}"
case "$public_expectation" in normal|maintenance) ;; *) echo 'Invalid RELEASE_PUBLIC_EXPECTATION' >&2; exit 1 ;; esac

observed_gateway_mode() {
  local revisions revision modes names mode observed= total
  revisions="$(az containerapp revision list -g "${RESOURCE_GROUP:?}" -n "${API_GATEWAY_APP_NAME:-api-gateway}" \
    --query '[].{name:name,active:properties.active,traffic:properties.trafficWeight}' -o json --only-show-errors)" || return 1
  total="$(jq -er '[.[] | select(.active == true and (.traffic | type == "number") and .traffic > 0)] | if length > 0 and (map(.traffic) | add) == 100 then map(.name)[] else error("unknown Gateway traffic") end' <<<"$revisions")" || return 1
  while IFS= read -r revision; do
    names="$(az containerapp revision show -g "${RESOURCE_GROUP:?}" -n "${API_GATEWAY_APP_NAME:-api-gateway}" --revision "$revision" \
      --query 'properties.template.containers[].name' -o json --only-show-errors)" || return 1
    jq -e 'map(select(. == "api-gateway")) | length == 1' <<<"$names" >/dev/null || return 1
    modes="$(az containerapp revision show -g "${RESOURCE_GROUP:?}" -n "${API_GATEWAY_APP_NAME:-api-gateway}" --revision "$revision" \
      --query "properties.template.containers[?name=='api-gateway'].env[] | [?name=='MAINTENANCE_MODE'].value" -o json --only-show-errors)" || return 1
    mode="$(jq -er 'if . == [] or . == ["off"] then "off" elif . == ["on"] then "on" else error("unknown Gateway maintenance mode") end' <<<"$modes")" || return 1
    [[ -z "$observed" || "$observed" == "$mode" ]] || { echo 'Mixed Gateway maintenance state' >&2; return 1; }
    observed="$mode"
  done <<<"$total"
  printf '%s\n' "$observed"
}

require_gateway_mode() {
  [[ "$(observed_gateway_mode)" == "$1" ]] || { echo 'Gateway maintenance expectation mismatch' >&2; return 1; }
}

maintenance_response() {
  local status="$1" headers="$2" body="$3" retry cache content
  [[ "$status" == 503 ]] || return 1
  retry="$(awk 'tolower($1)=="retry-after:" {sub(/^[^:]*:[[:space:]]*/, "");sub(/\r$/, "");print}' "$headers")"
  cache="$(awk 'tolower($1)=="cache-control:" {sub(/^[^:]*:[[:space:]]*/, "");sub(/\r$/, "");print}' "$headers")"
  content="$(awk 'tolower($1)=="content-type:" {sub(/^[^:]*:[[:space:]]*/, "");sub(/\r$/, "");print tolower($0)}' "$headers")"
  [[ "$retry" == 300 && "$cache" == 'private, no-store' && "${content%%;*}" == application/json ]] || return 1
  jq -e '. == {error:"Service Unavailable",service:"api-gateway"}' "$body" >/dev/null
}

maintenance_request() {
  local url="$1" directory status result=0
  directory="$(mktemp -d)"
  status="$(curl --silent --show-error --max-time 30 --dump-header "$directory/headers" --output "$directory/body" --write-out '%{http_code}' "$url")" || result=$?
  if [[ "$result" == 0 ]]; then maintenance_response "$status" "$directory/headers" "$directory/body" || result=1; fi
  rm -rf "$directory"
  return "$result"
}
