#!/usr/bin/env bash
set -euo pipefail

smoke_mode="${SMOKE_MODE:-}"
case "$smoke_mode" in
  forward|rollback) ;;
  *) echo 'SMOKE_MODE must be forward or rollback' >&2; exit 1 ;;
esac

resource_group="${RESOURCE_GROUP:-alive}"
gateway_app="${API_GATEWAY_APP_NAME:-api-gateway}"
public_url="${PUBLIC_SMOKE_URL:-https://www.alive.org.tw/api/home?locale=zh-Hant}"
locations_url="${LOCATIONS_SMOKE_URL:-https://www.alive.org.tw/api/locations?locale=zh-Hant}"

output="$(timeout 60s script -q -e -c \
  "az containerapp exec -g \"$resource_group\" -n \"$gateway_app\" --command \"/bin/sh -c \\\"/usr/bin/wget -qO- http://localhost:3500/v1.0/invoke/hhc-web-api/method/health/ready >/dev/null && echo READY_OK; /usr/bin/wget -qO- http://localhost:3500/v1.0/invoke/hhc-web-api/method/api/home?locale=zh-Hant >/dev/null && echo PUBLIC_OK; /usr/bin/wget -S -O- http://localhost:3500/v1.0/invoke/hhc-web-api/method/api/admin/bulletins 2>&1 || true\\\"\"" \
  /dev/null 2>&1)"
output="${output//$'\r'/}"
printf '%s\n' "$output"
grep -Fq 'READY_OK' <<<"$output"
grep -Fq 'PUBLIC_OK' <<<"$output"
grep -Eq 'HTTP/1\.[01][[:space:]]+401' <<<"$output"
curl --fail --silent --show-error --max-time 30 "$public_url" >/dev/null

if [[ "$smoke_mode" == forward ]]; then
  smoke_dir="$(mktemp -d)"
  trap 'rm -rf "$smoke_dir"' EXIT
  status="$(curl --silent --show-error --max-time 30 --dump-header "$smoke_dir/headers" \
    --output "$smoke_dir/body" --write-out '%{http_code}' "$locations_url")"
  [[ "$status" == 200 ]] || { echo "Locations smoke returned HTTP $status" >&2; exit 1; }
  content_type="$(awk 'tolower($1) == "content-type:" { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); print }' "$smoke_dir/headers" | tail -1)"
  media_type="$(printf '%s\n' "${content_type%%;*}" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')"
  [[ "$media_type" == application/json ]] || { echo "Locations smoke returned Content-Type $content_type" >&2; exit 1; }
  jq -e '
    .error == null
    and (.data | type == "array")
    and all(.data[];
      type == "object"
      and (.id | type == "string")
      and (.name | type == "string")
      and (.address | type == "string")
      and (.mapHref | type == "string")
      and (.sortOrder | type == "number")
      and (.resolvedLocale | type == "string")
      and (.availableLocales | type == "array"))
  ' "$smoke_dir/body" >/dev/null
fi
