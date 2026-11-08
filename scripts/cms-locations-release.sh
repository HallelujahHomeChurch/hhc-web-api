#!/usr/bin/env bash
set -euo pipefail
python3 scripts/deployment_config.py targets >/dev/null

operation="${1:?expected capture, stage-retirement, verify, or verify-rollback}"
revision="${2:?expected ready revision}"
[[ "$operation" == capture || "$operation" == stage-retirement || "$operation" == verify || "$operation" == verify-rollback ]] || exit 1
[[ "${API_APP_NAME:?}" == hhc-web-api ]] || exit 1

app="$(az containerapp show -g "${RESOURCE_GROUP:?}" -n "$API_APP_NAME" \
  --query "{latest:properties.latestRevisionName,ready:properties.latestReadyRevisionName,mode:properties.configuration.activeRevisionsMode,ingress:properties.configuration.ingress,dapr:properties.configuration.dapr,identities:identity.userAssignedIdentities,credential:properties.configuration.secrets[?name=='cms-gateway-locations-token'].{url:keyVaultUrl,identity:identity},privateCredentials:properties.configuration.secrets[?ends_with(name,'-private-token')].{name:name,url:keyVaultUrl,identity:identity},privateBindings:properties.template.containers[0].env[?ends_with(name,'_PRIVATE_TOKEN')].{name:name,ref:secretRef,inline:type(value) != 'null' && value != ''},runtimeFlags:properties.template.containers[0].env[?name=='AUDIT_PRIVATE_HTTPS' || name=='ASSET_PRIVATE_HTTPS' || name=='CMS_DAPR_ENABLED'].{name:name,value:value}}" \
  -o json --only-show-errors)"
jq -e --arg revision "$revision" '
  .latest == $revision and .ready == $revision and .mode == "Single"
  and .ingress.external == false and .ingress.allowInsecure == false and .ingress.targetPort == 8082
  and (.ingress.transport | strings | ascii_downcase) == "auto"
  and (.ingress.fqdn | strings | startswith("hhc-web-api.internal.") and endswith(".azurecontainerapps.io"))
  and .ingress.traffic == [{"latestRevision":true,"weight":100}]
  and (.ingress | (.additionalPortMappings // []) == [] and (.customDomains // []) == []
    and (.ipSecurityRestrictions // []) == [] and .clientCertificateMode == null
    and .corsPolicy == null and .stickySessions == null and .targetPortHttpScheme == null)
  and (.dapr.enabled == false or (.dapr.enabled == true and .dapr.appId == "hhc-web-api" and .dapr.appPort == 8082))
  and (.credential | type == "array" and length <= 1)
' <<<"$app" >/dev/null || { echo 'Unexpected CMS app state for locations alias' >&2; exit 1; }

live_dapr="$(jq -r '.dapr.enabled' <<< "$app")"
jq -e --arg enabled "$live_dapr" --arg operation "$operation" '
  [(.runtimeFlags // [])[] | select(.name == "CMS_DAPR_ENABLED") | .value] as $values |
  $values == [$enabled] or ($enabled == "true" and $values == []) or
  ($operation == "stage-retirement" and $enabled == "true" and $values == ["false"])
' <<< "$app" >/dev/null || { echo 'CMS app and runtime Dapr settings differ' >&2; exit 1; }
dapr_desired="$live_dapr"
if [[ "$operation" == verify || "$operation" == verify-rollback ]]; then
  expected_dapr="${CMS_DAPR_ENABLED:-true}"
  if [[ "$operation" == verify-rollback ]]; then
    expected_dapr="${CMS_DAPR_PREVIOUS_ENABLED:-true}"
  fi
  [[ "$live_dapr" == "$expected_dapr" ]] || { echo 'CMS Dapr state differs from the expected release state' >&2; exit 10; }
fi

url_prefix="https://${RUNTIME_KEY_VAULT_NAME:?}.vault.azure.net/secrets/cms-gateway-locations-token/"
live_version=''
if jq -e '.credential | length == 1' <<<"$app" >/dev/null; then
  url="$(jq -er --arg prefix "$url_prefix" '
    . as $app | .credential[0] | .identity as $identity
    | select(($identity | strings | ascii_downcase | endswith("/userassignedidentities/hhc-web-api-identity"))
      and ($app.identities | keys | map(ascii_downcase) | index($identity | ascii_downcase) != null))
    | .url | strings | select(startswith($prefix))
  ' <<<"$app")"
  live_version="${url#"$url_prefix"}"
  [[ "$live_version" =~ ^[a-f0-9]{32}$ ]] || { echo 'Unexpected CMS credential reference version' >&2; exit 1; }
fi

binding="$(az containerapp revision show -g "$RESOURCE_GROUP" -n "$API_APP_NAME" --revision "$revision" \
  --query "properties.template.containers[?name=='hhc-web-api'] | [0].env[?name=='CMS_GATEWAY_LOCATIONS_TOKEN'].{ref:secretRef,inline:type(value) != 'null' && value != ''}" \
  -o json --only-show-errors)"
binding_json="$(jq -cSe 'select(type == "array")' <<<"$binding")" ||
  { echo 'CMS locations alias environment readback is unavailable' >&2; exit 1; }
case "$binding_json" in
  '[]') live_enabled=false ;;
  '[{"inline":false,"ref":"cms-gateway-locations-token"}]')
    [[ -n "$live_version" ]] || { echo 'CMS alias credential is not backed by the expected vault' >&2; exit 1; }
    live_enabled=true ;;
  *)
    echo 'Unexpected CMS locations alias environment binding' >&2
    if [[ "$operation" == verify ]]; then exit 10; fi
    exit 1 ;;

esac

private_previous='{}'
private_desired='{}'
while read -r caller secret env_name version_var vault producer port; do
  prefix="https://$vault.vault.azure.net/secrets/$secret/"
  observed="$(jq -er --arg secret "$secret" --arg env "$env_name" --arg prefix "$prefix" '
    . as $app
    | [(.privateBindings // [])[] | select(.name == $env)] as $bindings
    | [(.privateCredentials // [])[] | select(.name == $secret)] as $refs
    | if ($bindings | length) == 0 then ""
      elif $bindings == [{name:$env,ref:$secret,inline:false}] and ($refs | length) == 1
        and ($refs[0].identity | strings | ascii_downcase | endswith("/userassignedidentities/hhc-web-api-identity"))
        and ($app.identities | keys | map(ascii_downcase) | index($refs[0].identity | ascii_downcase) != null)
        and ($refs[0].url | strings | startswith($prefix)) then $refs[0].url | ltrimstr($prefix)
      else error("unexpected CMS private service binding") end
  ' <<< "$app")"
  [[ -z "$observed" || "$observed" =~ ^[0-9a-f]{32}$ ]] || exit 1
  if [[ -n "$observed" ]]; then
    private_previous="$(jq -c --arg caller "$caller" --arg version "$observed" '. + {($caller):$version}' <<< "$private_previous")"
  fi

  requested=""
  if [[ "$operation" == capture && "${CMS_LOCATIONS_TRANSITION:-preserve}" == preserve ]]; then
    requested="${!version_var:-}"
  fi
  if [[ -n "$requested" ]]; then
    [[ "$requested" =~ ^[0-9a-f]{32}$ && ( -z "$observed" || "$requested" == "$observed" ) ]] ||
      { echo "CMS private credential version differs from the serving binding: $caller" >&2; exit 1; }
    if [[ "$producer" == "-" ]]; then
      az keyvault secret list-versions --vault-name "$vault" --name "$secret" \
        --query '[?attributes.enabled == `true`].id' -o json --only-show-errors |
        jq -e --arg id "$prefix$requested" 'any(.[]; . == $id)' >/dev/null ||
        { echo "CMS private credential version is unavailable: $caller" >&2; exit 1; }
    else
      downstream="$(az containerapp show -g "$RESOURCE_GROUP" -n "$producer" \
        --query '{latest:properties.latestRevisionName,ready:properties.latestReadyRevisionName,ingress:properties.configuration.ingress,secrets:properties.configuration.secrets[].{name:name,url:keyVaultUrl},env:properties.template.containers[0].env[].{name:name,ref:secretRef}}' \
        -o json --only-show-errors)"
      jq -e --arg env "$env_name" --arg secret "$secret" --arg uri "$prefix$requested" \
        --arg host "$producer.internal.gentleriver-81abd7bc.eastasia.azurecontainerapps.io" --argjson port "$port" '
          .latest != null and .latest == .ready and
          (.ingress | .fqdn == $host and .targetPort == $port and .external == false and .allowInsecure == false) and
          [.secrets[] | select(.name == $secret) | .url] == [$uri] and
          [.env[] | select(.name == $env) | .ref] == [$secret]
        ' <<< "$downstream" >/dev/null || { echo "CMS dependency is not ready: $producer" >&2; exit 1; }
    fi
  fi
  version="${requested:-$observed}"
  if [[ -n "$version" ]]; then
    private_desired="$(jq -c --arg caller "$caller" --arg version "$version" '. + {($caller):$version}' <<< "$private_desired")"
  fi
done <<SCOPES
account cms-account-private-token CMS_ACCOUNT_PRIVATE_TOKEN CMS_ACCOUNT_PRIVATE_SECRET_VERSION ${RUNTIME_KEY_VAULT_NAME:?} - -
operations cms-operations-private-token CMS_OPERATIONS_PRIVATE_TOKEN CMS_OPERATIONS_PRIVATE_SECRET_VERSION ${RUNTIME_KEY_VAULT_NAME:?} - -
engagement cms-engagement-private-token CMS_ENGAGEMENT_PRIVATE_TOKEN CMS_ENGAGEMENT_PRIVATE_SECRET_VERSION ${RUNTIME_KEY_VAULT_NAME:?} - -
line cms-line-private-token CMS_LINE_PRIVATE_TOKEN CMS_LINE_PRIVATE_SECRET_VERSION ${RUNTIME_KEY_VAULT_NAME:?} - -
gateway cms-gateway-private-token CMS_GATEWAY_PRIVATE_TOKEN CMS_GATEWAY_PRIVATE_SECRET_VERSION ${RUNTIME_KEY_VAULT_NAME:?} - -
gatewayLive cms-gateway-live-private-token CMS_GATEWAY_LIVE_PRIVATE_TOKEN CMS_GATEWAY_LIVE_PRIVATE_SECRET_VERSION ${RUNTIME_KEY_VAULT_NAME:?} - -
accountOutbound account-cms-private-token ACCOUNT_CMS_PRIVATE_TOKEN ACCOUNT_CMS_PRIVATE_SECRET_VERSION ${ACCOUNT_KEY_VAULT_NAME:?} account-api 8081
engagementOutbound engagement-cms-private-token ENGAGEMENT_CMS_PRIVATE_TOKEN ENGAGEMENT_CMS_PRIVATE_TOKEN_VERSION ${ENGAGEMENT_KEY_VAULT_NAME:?} engagement-api 8082
operationsOutbound operations-cms-private-token OPERATIONS_CMS_PRIVATE_TOKEN OPERATIONS_CMS_PRIVATE_TOKEN_VERSION ${LEGACY_KEY_VAULT_NAME:?} operations-api 8081
SCOPES
if [[ "$operation" == verify ]]; then
  expected_private="${CMS_PRIVATE_SERVICE_VERSIONS:-}"
  [[ -n "$expected_private" ]] || expected_private='{}'
  [[ "$private_previous" == "$expected_private" ]] ||
    { echo 'CMS private service bindings differ from the expected release state' >&2; exit 10; }
fi

if [[ "$operation" == verify || "$operation" == verify-rollback ]]; then
  expected_enabled="${CMS_LOCATIONS_ENABLED:?}"
  expected_version="${CMS_LOCATIONS_VERSION-}"
  if [[ "$operation" == verify-rollback ]]; then
    expected_enabled="${CMS_LOCATIONS_PREVIOUS_ENABLED:?}"
    # A failed first enable can leave the intended reference staged, or make no app write.
    if [[ "$expected_enabled" == false && "$CMS_LOCATIONS_ENABLED" == true &&
          -z "${CMS_LOCATIONS_PREVIOUS_VERSION-}" && -z "$live_version" ]]; then
      expected_version=''
    fi
  fi
  [[ "$live_enabled" == "$expected_enabled" && "$live_version" == "$expected_version" ]] ||
    { echo 'CMS locations alias differs from the expected release state' >&2; exit 10; }
  exit 0
fi

transition="${CMS_LOCATIONS_TRANSITION:-preserve}"
expected="${CMS_LOCATIONS_EXPECTED_REVISION:-}"
requested_version="${CMS_LOCATIONS_REQUESTED_VERSION:-}"
enabled="$live_enabled"
version="$live_version"
invalid_transition() { echo 'CMS locations alias transition does not match the captured state' >&2; exit 1; }
case "$transition" in
  preserve) [[ -z "$expected" && -z "$requested_version" ]] || invalid_transition ;;
  enable)
    [[ "$expected" == "$revision" && "$live_enabled" == false && "$requested_version" =~ ^[a-f0-9]{32}$ ]] || invalid_transition
    [[ -z "$live_version" || "$live_version" == "$requested_version" ]] || invalid_transition
    version="$requested_version"
    enabled=true ;;
  disable)
    [[ "$expected" == "$revision" && "$live_enabled" == true && -z "$requested_version" ]] || invalid_transition
    enabled=false ;;
  *) invalid_transition ;;
esac
if [[ "$transition" == disable && "$live_dapr" == false ]]; then
  echo 'CMS locations alias cannot be disabled without its legacy transport' >&2
  exit 1
fi

requested_dapr="${CMS_DAPR_ENABLED_REQUESTED:-}"
if [[ "$transition" == preserve && -n "$requested_dapr" ]]; then
  [[ "$requested_dapr" == "$live_dapr" || "$requested_dapr" == false ]] ||
    { echo 'CMS Dapr reactivation is restricted to reviewed recovery' >&2; exit 1; }
  dapr_desired="$requested_dapr"
fi

if [[ "$live_dapr" == true && "$dapr_desired" == false ]]; then
  [[ "${CMS_DAPR_OFF_EXPECTED_CMS_REVISION:-}" == "$revision" && "$live_enabled" == true &&
     "${CMS_ASSET_PRIVATE_HTTPS:-}" == true && "${CMS_AUDIT_PRIVATE_HTTPS:-}" == true ]] ||
    { echo 'CMS sidecar retirement requires the verified private runtime' >&2; exit 1; }
  jq -e -n --argjson previous "$private_previous" --argjson desired "$private_desired" '
    ($desired | length) == 9 and
    ($previous | has("account") and has("operations") and has("engagement") and has("line") and
      has("gateway") and has("gatewayLive"))
  ' >/dev/null || { echo 'CMS sidecar retirement requires all direct pins and active inbound bindings' >&2; exit 1; }
  jq -e '
    [.runtimeFlags[] | select(.name == "AUDIT_PRIVATE_HTTPS" or .name == "ASSET_PRIVATE_HTTPS")
      | select(.value == "true") | .name] | sort == ["ASSET_PRIVATE_HTTPS", "AUDIT_PRIVATE_HTTPS"]
  ' <<< "$app" >/dev/null || { echo 'CMS outbound private transports must already be active' >&2; exit 1; }

  require_ready() {
    local consumer="$1" expected="$2"
    [[ "$expected" == "$consumer--"* ]] || return 1
    az containerapp show -g "$RESOURCE_GROUP" -n "$consumer" \
      --query '{latest:properties.latestRevisionName,ready:properties.latestReadyRevisionName}' -o json --only-show-errors |
      jq -e --arg revision "$expected" '.latest == $revision and .ready == $revision' >/dev/null
  }
  require_ready api-gateway "${CMS_DAPR_OFF_EXPECTED_GATEWAY_REVISION:-}" &&
    require_ready hhc-web "${CMS_DAPR_OFF_EXPECTED_WEBSITE_REVISION:-}" ||
    { echo 'CMS sidecar retirement requires the verified serving Gateway and Website revisions' >&2; exit 1; }

  for spec in 'account:account-api' 'operations:operations-api' 'engagement:engagement-api' 'line:hhc-line-function-bot'; do
    consumer="${spec#*:}"
    caller="${spec%:*}"
    secret="cms-$caller-private-token"
    caller_version="$(jq -er --arg caller "$caller" '.[$caller]' <<< "$private_previous")"
    az containerapp show -g "$RESOURCE_GROUP" -n "$consumer" \
      --query '{latest:properties.latestRevisionName,ready:properties.latestReadyRevisionName,secrets:properties.configuration.secrets[].{name:name,url:keyVaultUrl},refs:properties.template.containers[0].env[].secretRef}' \
      -o json --only-show-errors |
      jq -e --arg secret "$secret" --arg uri "https://${RUNTIME_KEY_VAULT_NAME:?}.vault.azure.net/secrets/$secret/$caller_version" '
        .latest != null and .latest == .ready and
        [.secrets[] | select(.name == $secret) | .url] == [$uri] and
        [.refs[] | select(. == $secret)] == [$secret]
      ' >/dev/null || { echo "CMS caller still needs the legacy transport: $consumer" >&2; exit 1; }
  done
fi

if [[ "$enabled" == true ]]; then
  az keyvault secret list-versions --vault-name ${RUNTIME_KEY_VAULT_NAME:?} --name cms-gateway-locations-token \
    --query '[?attributes.enabled == `true`].id' -o json --only-show-errors |
    jq -e --arg id "$url_prefix$version" 'any(.[]; . == $id)' >/dev/null || { echo 'CMS locations credential version is unavailable' >&2; exit 1; }
fi
{
  echo "CMS_LOCATIONS_ENABLED=$enabled"
  echo "CMS_LOCATIONS_VERSION=$version"
  echo "CMS_LOCATIONS_PREVIOUS_ENABLED=$live_enabled"
  echo "CMS_LOCATIONS_PREVIOUS_VERSION=$live_version"
  echo "CMS_PRIVATE_SERVICE_VERSIONS=$private_desired"
  echo "CMS_PRIVATE_SERVICE_PREVIOUS_VERSIONS=$private_previous"
  echo "CMS_DAPR_ENABLED=$dapr_desired"
  echo "CMS_DAPR_PREVIOUS_ENABLED=$live_dapr"
} >> "${GITHUB_ENV:?}"
