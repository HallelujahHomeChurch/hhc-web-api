#!/usr/bin/env bash
set -euo pipefail

smoke_mode="${SMOKE_MODE:-}"
case "$smoke_mode" in
  forward|rollback) ;;
  *) echo 'SMOKE_MODE must be forward or rollback' >&2; exit 1 ;;
esac

home_page_contract_mode="${HOME_PAGE_CONTRACT_MODE:-dual}"
case "$home_page_contract_mode" in
  dual|v2-only) ;;
  *) echo 'HOME_PAGE_CONTRACT_MODE must be dual or v2-only' >&2; exit 1 ;;
esac

resource_group="${RESOURCE_GROUP:-alive}"
gateway_app="${API_GATEWAY_APP_NAME:-api-gateway}"
public_url="${PUBLIC_SMOKE_URL:-https://www.alive.org.tw/api/home?locale=zh-Hant}"
home_smoke_base_url="${HOME_SMOKE_BASE_URL:-https://www.alive.org.tw/api/home}"
locations_url="${LOCATIONS_SMOKE_URL:-https://www.alive.org.tw/api/locations?locale=zh-Hant}"
site_layout_url="${SITE_LAYOUT_SMOKE_URL:-https://www.alive.org.tw/api/site-layout?locale=zh-Hant}"
page_smoke_base_url="${PAGE_SMOKE_BASE_URL:-https://www.alive.org.tw/api/pages}"

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
  expected_video_ids=
  for locale in zh-Hant zh-Hans en ja ko; do
    curl --fail --silent --show-error --max-time 30 "${home_smoke_base_url}?locale=$locale" >"$smoke_dir/home-$locale"
    video_ids="$(jq -ce 'if (.error == null and (.data.videos | type == "array")) then [.data.videos[].id] else error("invalid Home response") end' "$smoke_dir/home-$locale")"
    if [[ -z "$expected_video_ids" ]]; then
      expected_video_ids="$video_ids"
    elif [[ "$video_ids" != "$expected_video_ids" ]]; then
      echo "Home Video order differs for $locale" >&2
      exit 1
    fi
  done
  curl --fail --silent --show-error --max-time 30 "${home_smoke_base_url}?locale=zh-Hant" >"$smoke_dir/home-zh-Hant-repeat"
  repeated_video_ids="$(jq -ce '[.data.videos[].id]' "$smoke_dir/home-zh-Hant-repeat")"
  [[ "$repeated_video_ids" == "$expected_video_ids" ]] || { echo 'Home Video order changed across repeated zh-Hant reads' >&2; exit 1; }

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

  status="$(curl --silent --show-error --max-time 30 --dump-header "$smoke_dir/site-layout-headers" \
    --output "$smoke_dir/site-layout-body" --write-out '%{http_code}' "$site_layout_url")"
  content_type="$(awk 'tolower($1) == "content-type:" { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); print }' "$smoke_dir/site-layout-headers" | tail -1)"
  etag="$(awk 'tolower($1) == "etag:" { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); print }' "$smoke_dir/site-layout-headers" | tail -1)"
  media_type="$(printf '%s\n' "${content_type%%;*}" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')"
  [[ "$media_type" == application/json ]] || { echo "Site Layout smoke returned Content-Type $content_type" >&2; exit 1; }
  case "$status" in
    200)
      jq -e '
        (keys == ["data", "error", "meta"])
        and .error == null
        and (.meta | type == "object")
        and (.data | keys == ["allRightsReserved", "copyrightHolder", "englishName", "header", "legal", "links", "locale", "publishedAt", "seoDescriptionFallback", "seoTitleSuffix", "siteName", "version"])
        and .data.locale == "zh-Hant"
        and ([.data.siteName, .data.englishName, .data.copyrightHolder, .data.allRightsReserved, .data.seoTitleSuffix, .data.seoDescriptionFallback, .data.publishedAt] | all(type == "string" and length > 0))
        and (.data.version | type == "number" and . >= 1 and floor == .)
        and (.data.header | type == "array" and length == 3)
        and (.data.legal | type == "array" and length == 2)
        and ([.data.header[].key] | sort == ["about", "literature-ministry", "news"])
        and ([.data.legal[].key] | sort == ["privacy-policy", "terms-of-use"])
        and all(.data.header[];
          keys == ["href", "key", "label", "visible"]
          and (.key | type == "string")
          and (.label | type == "string")
          and (.href | type == "string")
          and (.visible | type == "boolean")
          and (.visible == false or (.label | length > 0))
          and ((.key == "about" and .href == "/zh-Hant/about")
            or (.key == "news" and .href == "/zh-Hant/news")
            or (.key == "literature-ministry" and .href == "/zh-Hant/literature-ministry")))
        and all(.data.legal[];
          keys == ["href", "key", "label", "visible"]
          and (.key | type == "string")
          and (.label | type == "string")
          and (.href | type == "string")
          and (.visible | type == "boolean")
          and (.visible == false or (.label | length > 0))
          and ((.key == "privacy-policy" and .href == "/zh-Hant/privacy-policy")
            or (.key == "terms-of-use" and .href == "/zh-Hant/terms-of-use")))
        and (.data.links | keys == ["churchFacebook", "churchYoutube", "musicYoutube"])
        and ([.data.links.churchFacebook, .data.links.churchYoutube, .data.links.musicYoutube] | all(type == "string" and startswith("https://")))
      ' "$smoke_dir/site-layout-body" >/dev/null
      expected_etag="$(jq -r '"\"site-layout-\(.data.version)\""' "$smoke_dir/site-layout-body")"
      [[ "$etag" == "$expected_etag" ]] || { echo "Site Layout smoke returned ETag $etag, expected $expected_etag" >&2; exit 1; }
      ;;
    404)
      jq -e '
        (keys == ["data", "error", "meta"])
        and .data == null
        and (.meta | type == "object")
        and (.error | keys == ["code", "message"])
        and .error.code == "not_found"
        and (.error.message | type == "string" and length > 0)
      ' "$smoke_dir/site-layout-body" >/dev/null
      ;;
    *) echo "Site Layout smoke returned HTTP $status" >&2; exit 1 ;;
  esac

  for page_key in home about privacy-policy terms-of-use; do
    case "$page_key" in
      home) page_template=; route_path=/ ;;
      about) page_template=about.v1; route_path=/about ;;
      privacy-policy) page_template=legal.v1; route_path=/privacy-policy ;;
      terms-of-use) page_template=legal.v1; route_path=/terms-of-use ;;
    esac
    status="$(curl --silent --show-error --max-time 30 --dump-header "$smoke_dir/page-$page_key-headers" \
      --output "$smoke_dir/page-$page_key-body" --write-out '%{http_code}' \
      "${page_smoke_base_url%/}/$page_key?locale=zh-Hant")"
    content_type="$(awk 'tolower($1) == "content-type:" { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); print }' "$smoke_dir/page-$page_key-headers" | tail -1)"
    etag="$(awk 'tolower($1) == "etag:" { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); print }' "$smoke_dir/page-$page_key-headers" | tail -1)"
    cache_control="$(awk 'tolower($1) == "cache-control:" { sub(/^[^:]*:[[:space:]]*/, ""); sub(/\r$/, ""); print }' "$smoke_dir/page-$page_key-headers" | tail -1)"
    media_type="$(printf '%s\n' "${content_type%%;*}" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')"
    [[ "$media_type" == application/json ]] || { echo "Page $page_key smoke returned Content-Type $content_type" >&2; exit 1; }
    case "$status" in
      200)
        [[ "$etag" =~ ^\"[^\"]+\"$ ]] || { echo "Page $page_key smoke returned non-strong ETag $etag" >&2; exit 1; }
        [[ "$cache_control" == 'public, max-age=30, must-revalidate' ]] || { echo "Page $page_key smoke returned Cache-Control $cache_control" >&2; exit 1; }
        jq -e --arg key "$page_key" --arg template "$page_template" --arg route "$route_path" --arg home_contract "$home_page_contract_mode" '
          (keys == ["data", "error", "meta"])
          and .error == null
          and (.meta | type == "object")
          and (.data | keys == ["availableLocales", "content", "indexable", "pageKey", "publishedAt", "resolvedLocale", "routePath", "template", "version"])
          and .data.pageKey == $key
          and ((.data.pageKey == "home" and (($home_contract == "dual" and (.data.template == "home.v1" or .data.template == "home.v2")) or ($home_contract == "v2-only" and .data.template == "home.v2"))) or (.data.pageKey != "home" and .data.template == $template))
          and .data.routePath == $route
          and (.data.indexable | type == "boolean")
          and .data.resolvedLocale == "zh-Hant"
          and (.data.availableLocales | type == "array" and index("zh-Hant") != null)
          and (.data.version | type == "number" and . >= 1 and floor == .)
          and (.data.publishedAt | type == "string" and length > 0)
          and (.data.content | type == "object")
          and .data.content.template == .data.template
          and (.data.content.data | type == "object")
          and (
            (.data.template == "home.v1"
              and .data.content.schemaVersion == 1
              and (.data.content.data | keys == ["aboutBody", "aboutCta", "aboutTitle", "downloadWeekly", "heroSubtitle", "heroTitle", "locationsTitle", "mapLink", "moreNews", "newsTitle", "videosSubtitle", "videosTitle", "watchMore", "weeklyTitle"])
              and ([.data.content.data[]] | all(type == "string" and length > 0)))
            or (.data.template == "home.v2"
              and .data.content.schemaVersion == 2
              and (.data.content.data | keys == ["aboutDescription", "bannerImageUrl", "heroSubtitle", "heroTitle", "kingdomJoyDescription", "links", "locations"])
              and ([.data.content.data.heroTitle, .data.content.data.heroSubtitle, .data.content.data.kingdomJoyDescription, .data.content.data.aboutDescription, .data.content.data.bannerImageUrl] | all(type == "string" and length > 0))
              and (.data.content.data.links | keys == ["churchFacebook", "churchYoutube", "musicYoutube"])
              and ([.data.content.data.links.churchFacebook, .data.content.data.links.churchYoutube, .data.content.data.links.musicYoutube] | all(type == "string" and startswith("https://")))
              and (.data.content.data.locations | type == "array" and all(keys == ["address", "key", "mapHref", "name", "sortOrder"] and ([.address, .key, .mapHref, .name] | all(type == "string" and length > 0)) and (.sortOrder | type == "number"))))
            or (.data.template == "about.v1"
              and .data.content.schemaVersion == 1
              and (.data.content.data | keys == ["heroSubtitle", "heroTitle", "history", "vision"])
              and ([.data.content.data.heroTitle, .data.content.data.heroSubtitle] | all(type == "string" and length > 0))
              and (.data.content.data.vision | keys == ["actionsImageAlt", "imageAlt", "intro", "sections"])
              and ([.data.content.data.vision.intro, .data.content.data.vision.imageAlt, .data.content.data.vision.actionsImageAlt] | all(type == "string" and length > 0))
              and (.data.content.data.vision.sections | type == "array" and length == 4)
              and (.data.content.data.vision.sections[0:2] | all(keys == ["body", "eyebrow", "title"] and ([.body, .eyebrow, .title] | all(type == "string" and length > 0))))
              and (.data.content.data.vision.sections[2:4] | all(keys == ["cards", "eyebrow", "title"] and ([.eyebrow, .title] | all(type == "string" and length > 0)) and (.cards | type == "array" and length > 0 and all(keys == ["body", "title"] and ([.body, .title] | all(type == "string" and length > 0))))))
              and (.data.content.data.history | keys == ["imageAlt", "intro", "scripture", "title"])
              and ([.data.content.data.history.imageAlt, .data.content.data.history.intro, .data.content.data.history.title] | all(type == "string" and length > 0))
              and (.data.content.data.history.scripture | type == "array" and length > 0 and all(keys == ["cite", "lines"] and (.cite | type == "string" and length > 0) and (.lines | type == "array" and length > 0 and all(type == "string" and length > 0)))))
            or (.data.template == "legal.v1"
              and .data.content.schemaVersion == 1
              and ((.data.content.data | keys) == ["heroSubtitle", "heroTitle", "intro", "sections", "updatedAt", "updatedAtLabel"] or (.data.content.data | keys) == ["heroTitle", "intro", "sections", "updatedAt", "updatedAtLabel"])
              and ([.data.content.data.heroTitle, .data.content.data.updatedAtLabel, .data.content.data.updatedAt, .data.content.data.intro] | all(type == "string" and length > 0))
              and ((.data.content.data | has("heroSubtitle") | not) or (.data.content.data.heroSubtitle | type == "string"))
              and (.data.content.data.sections | type == "array" and length > 0 and all(keys == ["body", "title"] and (.title | type == "string" and length > 0) and (.body | type == "array" and length > 0 and all(type == "string" and length > 0)))))
          )
        ' "$smoke_dir/page-$page_key-body" >/dev/null
        ;;
      404)
        jq -e '
          (keys == ["data", "error", "meta"])
          and .data == null
          and (.meta | type == "object")
          and (.error | keys == ["code", "message"])
          and .error.code == "not_found"
          and (.error.message | type == "string" and length > 0)
        ' "$smoke_dir/page-$page_key-body" >/dev/null
        ;;
      *) echo "Page $page_key smoke returned HTTP $status" >&2; exit 1 ;;
    esac
  done
fi
