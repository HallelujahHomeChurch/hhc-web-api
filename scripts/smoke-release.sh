#!/usr/bin/env bash
set -euo pipefail

resource_group="${RESOURCE_GROUP:-alive}"
gateway_app="${API_GATEWAY_APP_NAME:-api-gateway}"
public_url="${PUBLIC_SMOKE_URL:-https://www.alive.org.tw/api/home?locale=zh-Hant}"

output="$(timeout 60s script -q -e -c \
  "az containerapp exec -g \"$resource_group\" -n \"$gateway_app\" --command \"/bin/sh -c \\\"/usr/bin/wget -qO- http://localhost:3500/v1.0/invoke/hhc-web-api/method/health/ready >/dev/null && echo READY_OK; /usr/bin/wget -qO- http://localhost:3500/v1.0/invoke/hhc-web-api/method/api/home?locale=zh-Hant >/dev/null && echo PUBLIC_OK; /usr/bin/wget -S -O- http://localhost:3500/v1.0/invoke/hhc-web-api/method/api/admin/bulletins 2>&1 || true\\\"\"" \
  /dev/null 2>&1)"
output="${output//$'\r'/}"
printf '%s\n' "$output"
grep -Fq 'READY_OK' <<<"$output"
grep -Fq 'PUBLIC_OK' <<<"$output"
grep -Eq 'HTTP/1\.[01][[:space:]]+401' <<<"$output"
curl --fail --silent --show-error --max-time 30 "$public_url" >/dev/null
