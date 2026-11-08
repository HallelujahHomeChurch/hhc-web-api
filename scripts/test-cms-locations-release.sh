#!/usr/bin/env bash
set -euo pipefail
eval "$(python3 scripts/test_no_cloud.py)"

tmp="$(mktemp -d)"
export RUNNER_TEMP="$tmp"
trap 'rm -r -- "$tmp"' EXIT
export PATH="$tmp:$PATH" API_APP_NAME=hhc-web-api RESOURCE_GROUP=fixture
export GITHUB_ENV="$tmp/env" FIXTURE_VERSION=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
export FIXTURE_REVISION=hhc-web-api--0000298
cat >"$tmp/az" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
identity='/subscriptions/test/resourceGroups/fixture/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity'
url="https://example-runtime-kv.vault.azure.net/secrets/cms-gateway-locations-token/$FIXTURE_VERSION"
inline=false
if [[ "${FIXTURE_NONEMPTY_SECRET_VALUES:-}" == true ||
      ( "${FIXTURE_NONEMPTY_PRIVATE_VALUES:-}" == true && "$*" == *privateBindings* ) ||
      ( "${FIXTURE_EMPTY_SECRET_VALUES:-}" == true && "$*" != *"value != ''"* ) ]]; then
  inline=true
fi
case "$*" in
  'containerapp show '*)
    if [[ "${FIXTURE_PRIVATE:-}" == retirement && "$*" != *'-n hhc-web-api '* ]]; then
      consumer="$(sed -E 's/.* -n ([^ ]+).*/\1/' <<< "$*")"
      jq -nc --arg consumer "$consumer" --arg version "$FIXTURE_CALLER_VERSION" '
        {latest:($consumer+"--ready"),ready:($consumer+"--ready"),
         secrets:(["account","operations","engagement","line"] | map({name:("cms-"+.+"-private-token"),url:("https://example-runtime-kv.vault.azure.net/secrets/cms-"+.+"-private-token/"+$version)})),
         refs:(["account","operations","engagement","line"] | map("cms-"+.+"-private-token"))}'
      exit 0
    fi
    jq -nc --arg identity "$identity" --arg url "$url" --argjson inline "$inline" '{
      latest:env.FIXTURE_REVISION, ready:env.FIXTURE_REVISION, mode:"Single",
      ingress:{external:(env.FIXTURE_STATE == "unsafe-ingress"),allowInsecure:false,targetPort:8082,transport:"Auto",fqdn:"hhc-web-api.internal.example.eastasia.azurecontainerapps.io",traffic:[{latestRevision:true,weight:100}]},
      dapr:{enabled:(env.FIXTURE_STATE != "dapr-off"),appId:"hhc-web-api",appPort:8082}, identities:{($identity):{}},
      credential:(if ["enabled","retained","inline"] | index(env.FIXTURE_STATE)
        then [{url:$url,identity:$identity}] else [] end),
      privateCredentials:(if env.FIXTURE_PRIVATE == "active" or env.FIXTURE_PRIVATE == "wrong-identity"
        then [{name:"cms-account-private-token",url:("https://example-runtime-kv.vault.azure.net/secrets/cms-account-private-token/"+env.FIXTURE_VERSION),
               identity:(if env.FIXTURE_PRIVATE == "wrong-identity" then "/fixture/untrusted" else $identity end)}] else [] end),
      runtimeFlags:(if env.FIXTURE_STATE == "dapr-off" then [{name:"CMS_DAPR_ENABLED",value:"false"}] else [] end),
      privateBindings:(if env.FIXTURE_PRIVATE == "active" or env.FIXTURE_PRIVATE == "wrong-identity"
        then [{name:"CMS_ACCOUNT_PRIVATE_TOKEN",ref:"cms-account-private-token",inline:false}] else [] end)
    } | if env.FIXTURE_PRIVATE == "retirement" then
      .runtimeFlags = [{name:"AUDIT_PRIVATE_HTTPS",value:"true"},{name:"ASSET_PRIVATE_HTTPS",value:"true"}] |
      [{secret:"cms-account-private-token",name:"CMS_ACCOUNT_PRIVATE_TOKEN",vault:"example-runtime-kv"},
       {secret:"cms-operations-private-token",name:"CMS_OPERATIONS_PRIVATE_TOKEN",vault:"example-runtime-kv"},
       {secret:"cms-engagement-private-token",name:"CMS_ENGAGEMENT_PRIVATE_TOKEN",vault:"example-runtime-kv"},
       {secret:"cms-line-private-token",name:"CMS_LINE_PRIVATE_TOKEN",vault:"example-runtime-kv"},
       {secret:"cms-gateway-private-token",name:"CMS_GATEWAY_PRIVATE_TOKEN",vault:"example-runtime-kv"},
       {secret:"cms-gateway-live-private-token",name:"CMS_GATEWAY_LIVE_PRIVATE_TOKEN",vault:"example-runtime-kv"},
       {secret:"account-cms-private-token",name:"ACCOUNT_CMS_PRIVATE_TOKEN",vault:"example-account-kv"},
       {secret:"engagement-cms-private-token",name:"ENGAGEMENT_CMS_PRIVATE_TOKEN",vault:"example-engagement-kv"},
       {secret:"operations-cms-private-token",name:"OPERATIONS_CMS_PRIVATE_TOKEN",vault:"example-legacy-kv"}] as $pins |
      .privateCredentials = ($pins | map({name:.secret,identity:$identity,url:("https://"+.vault+".vault.azure.net/secrets/"+.secret+"/"+env.FIXTURE_CALLER_VERSION)})) |
      .privateBindings = ($pins | map({name:.name,ref:.secret,inline:$inline}))
    else . end' ;;
  'containerapp revision show '*)
    case "$FIXTURE_STATE" in
      enabled) jq -nc --argjson inline "$inline" '[{inline:$inline,ref:"cms-gateway-locations-token"}]' ;;
      inline) echo '[{"ref":null,"inline":true}]' ;;
      malformed) echo 'invalid response' ;;
      *) echo '[]' ;;
    esac ;;
  'keyvault secret list-versions '*)
    if [[ "$*" == *'--name cms-account-private-token '* ]]; then
      url="https://example-runtime-kv.vault.azure.net/secrets/cms-account-private-token/$FIXTURE_VERSION"
    fi
    jq -nc --arg url "$url" '[$url]' ;;
  *) exit 1 ;;
esac
MOCK
chmod +x "$tmp/az"

capture() {
  FIXTURE_STATE="$1" CMS_LOCATIONS_TRANSITION="$2" CMS_LOCATIONS_EXPECTED_REVISION="$3" \
    CMS_LOCATIONS_REQUESTED_VERSION="$4" ./scripts/cms-locations-release.sh capture "$FIXTURE_REVISION"
}
reject() {
  if capture "$@" 2>/dev/null; then
    echo 'CMS release accepted an unsafe locations transition' >&2
    exit 1
  fi
}
capture disabled preserve '' ''
grep -Fxq 'CMS_LOCATIONS_ENABLED=false' "$GITHUB_ENV"
grep -Fxq 'CMS_LOCATIONS_VERSION=' "$GITHUB_ENV"
: >"$GITHUB_ENV"
capture disabled enable "$FIXTURE_REVISION" "$FIXTURE_VERSION"
grep -Fxq 'CMS_LOCATIONS_ENABLED=true' "$GITHUB_ENV"
grep -Fxq "CMS_LOCATIONS_VERSION=$FIXTURE_VERSION" "$GITHUB_ENV"
: >"$GITHUB_ENV"
capture enabled preserve '' ''
grep -Fxq 'CMS_LOCATIONS_ENABLED=true' "$GITHUB_ENV"
: >"$GITHUB_ENV"
capture enabled disable "$FIXTURE_REVISION" ''
grep -Fxq 'CMS_LOCATIONS_ENABLED=false' "$GITHUB_ENV"
grep -Fxq "CMS_LOCATIONS_VERSION=$FIXTURE_VERSION" "$GITHUB_ENV"
grep -Fxq 'CMS_LOCATIONS_PREVIOUS_ENABLED=true' "$GITHUB_ENV"
FIXTURE_STATE=retained CMS_LOCATIONS_ENABLED=false CMS_LOCATIONS_VERSION="$FIXTURE_VERSION" \
  ./scripts/cms-locations-release.sh verify "$FIXTURE_REVISION"
if FIXTURE_STATE=enabled CMS_LOCATIONS_ENABLED=false CMS_LOCATIONS_VERSION="$FIXTURE_VERSION" \
    ./scripts/cms-locations-release.sh verify "$FIXTURE_REVISION" 2>/dev/null; then
  echo 'CMS alias readback accepted enabled state when disabled was expected' >&2
  exit 1
fi
FIXTURE_STATE=enabled CMS_LOCATIONS_ENABLED=true CMS_LOCATIONS_VERSION="$FIXTURE_VERSION" \
  ./scripts/cms-locations-release.sh verify "$FIXTURE_REVISION"
if FIXTURE_STATE=enabled CMS_LOCATIONS_ENABLED=true CMS_LOCATIONS_VERSION=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
    ./scripts/cms-locations-release.sh verify "$FIXTURE_REVISION" 2>/dev/null; then
  echo 'CMS alias readback accepted the wrong retained version' >&2
  exit 1
fi
if FIXTURE_STATE=disabled CMS_LOCATIONS_ENABLED=false CMS_LOCATIONS_VERSION="$FIXTURE_VERSION" \
    ./scripts/cms-locations-release.sh verify "$FIXTURE_REVISION" 2>/dev/null; then
  echo 'CMS alias readback accepted a missing retained reference after rollback' >&2
  exit 1
fi
for mode in retained disabled; do
  FIXTURE_STATE="$mode" CMS_LOCATIONS_ENABLED=true CMS_LOCATIONS_VERSION="$FIXTURE_VERSION" \
    CMS_LOCATIONS_PREVIOUS_ENABLED=false CMS_LOCATIONS_PREVIOUS_VERSION='' \
    ./scripts/cms-locations-release.sh verify-rollback "$FIXTURE_REVISION"
done
FIXTURE_STATE=enabled CMS_LOCATIONS_ENABLED=false CMS_LOCATIONS_VERSION="$FIXTURE_VERSION" \
  CMS_LOCATIONS_PREVIOUS_ENABLED=true CMS_LOCATIONS_PREVIOUS_VERSION="$FIXTURE_VERSION" \
  ./scripts/cms-locations-release.sh verify-rollback "$FIXTURE_REVISION"
if FIXTURE_STATE=retained CMS_LOCATIONS_ENABLED=true CMS_LOCATIONS_VERSION=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
    CMS_LOCATIONS_PREVIOUS_ENABLED=false CMS_LOCATIONS_PREVIOUS_VERSION='' \
    ./scripts/cms-locations-release.sh verify-rollback "$FIXTURE_REVISION" 2>/dev/null; then
  echo 'CMS first-enable rollback accepted a different credential version' >&2
  exit 1
fi
reject disabled enable hhc-web-api--stale "$FIXTURE_VERSION"
reject disabled enable "$FIXTURE_REVISION" ''
reject retained enable "$FIXTURE_REVISION" bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
reject disabled disable "$FIXTURE_REVISION" ''
reject enabled preserve "$FIXTURE_REVISION" ''
reject enabled disable "$FIXTURE_REVISION" "$FIXTURE_VERSION"
reject unsafe-ingress preserve '' ''
reject inline preserve '' ''
for outcome in inline malformed; do
  result=0
  FIXTURE_STATE="$outcome" CMS_LOCATIONS_ENABLED=false CMS_LOCATIONS_VERSION="$FIXTURE_VERSION" \
    ./scripts/cms-locations-release.sh verify "$FIXTURE_REVISION" 2>/dev/null || result=$?
  if [[ "$outcome" == inline ]]; then [[ "$result" == 10 ]] || exit 1
  else [[ "$result" == 1 ]] || exit 1
  fi
done
echo 'CMS locations release state checks passed'


: >"$GITHUB_ENV"
CMS_ACCOUNT_PRIVATE_SECRET_VERSION="$FIXTURE_VERSION" capture disabled preserve '' ''
grep -Fxq "CMS_PRIVATE_SERVICE_VERSIONS={\"account\":\"$FIXTURE_VERSION\"}" "$GITHUB_ENV"
grep -Fxq 'CMS_PRIVATE_SERVICE_PREVIOUS_VERSIONS={}' "$GITHUB_ENV"
: >"$GITHUB_ENV"
FIXTURE_PRIVATE=active capture disabled preserve '' ''
grep -Fxq "CMS_PRIVATE_SERVICE_VERSIONS={\"account\":\"$FIXTURE_VERSION\"}" "$GITHUB_ENV"
FIXTURE_STATE=disabled FIXTURE_PRIVATE=active CMS_LOCATIONS_ENABLED=false CMS_LOCATIONS_VERSION='' \
  CMS_PRIVATE_SERVICE_VERSIONS="{\"account\":\"$FIXTURE_VERSION\"}" \
  ./scripts/cms-locations-release.sh verify "$FIXTURE_REVISION"
FIXTURE_PRIVATE=wrong-identity reject disabled preserve '' ''


: >"$GITHUB_ENV"
capture dapr-off preserve '' ''
grep -Fxq 'CMS_DAPR_ENABLED=false' "$GITHUB_ENV"
grep -Fxq 'CMS_DAPR_PREVIOUS_ENABLED=false' "$GITHUB_ENV"
CMS_DAPR_ENABLED_REQUESTED=false reject disabled preserve '' ''
CMS_DAPR_ENABLED_REQUESTED=true reject dapr-off preserve '' ''

# Retirement must retain the locations version after checking distinct caller pins.
: >"$GITHUB_ENV"
retirement_capture() {
FIXTURE_EMPTY_SECRET_VALUES=true FIXTURE_PRIVATE=retirement FIXTURE_CALLER_VERSION=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
  CMS_DAPR_ENABLED_REQUESTED=false CMS_DAPR_OFF_EXPECTED_CMS_REVISION="$FIXTURE_REVISION" \
  CMS_DAPR_OFF_EXPECTED_GATEWAY_REVISION=api-gateway--ready \
  CMS_DAPR_OFF_EXPECTED_WEBSITE_REVISION=hhc-web--ready \
  CMS_ASSET_PRIVATE_HTTPS=true CMS_AUDIT_PRIVATE_HTTPS=true capture enabled preserve '' ''
}
retirement_capture
grep -Fxq "CMS_LOCATIONS_VERSION=$FIXTURE_VERSION" "$GITHUB_ENV"
grep -Fxq "CMS_LOCATIONS_PREVIOUS_VERSION=$FIXTURE_VERSION" "$GITHUB_ENV"
grep -Fxq 'CMS_DAPR_ENABLED=false' "$GITHUB_ENV"

if FIXTURE_NONEMPTY_SECRET_VALUES=true retirement_capture 2>/dev/null; then
  echo 'CMS retirement accepted a nonempty inline credential' >&2
  exit 1
fi

if FIXTURE_NONEMPTY_PRIVATE_VALUES=true retirement_capture 2>/dev/null; then
  echo 'CMS retirement accepted a nonempty private-service inline credential' >&2
  exit 1
fi
