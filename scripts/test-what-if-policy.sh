#!/bin/sh
set -eu
eval "$(python3 scripts/test_no_cloud.py)"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

cat >"$tmp/safe.json" <<'JSON'
{"changes":[
  {"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers","propertyChangeType":"Array","children":[{"path":"image","propertyChangeType":"Modify"}]}]},
  {"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-bulletin-worker","changeType":"Create","delta":[]},
  {"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/jobs/hhc-web-migrate","changeType":"Modify","delta":[]},
  {"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/jobs/hhc-web-content-import","changeType":"Create","delta":[]},
  {"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.CognitiveServices/accounts/example-openai/raiPolicies/hhc-cms-translation-v1","changeType":"Create","delta":[]},
  {"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api/authConfigs/current","changeType":"Create","delta":[]},
  {"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.KeyVault/vaults/other","changeType":"Ignore","delta":[]}
]}
JSON
./scripts/check-what-if.sh "$tmp/safe.json"

cat >"$tmp/cms-http-scaler-retirement.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","after":{"properties":{"template":{"containers":[{"name":"hhc-web-api","resources":{"cpu":0.25,"memory":"0.5Gi"}}],"scale":{"minReplicas":1,"maxReplicas":12,"pollingInterval":30,"cooldownPeriod":300,"rules":[{"name":"cpu-scaler","custom":{"type":"cpu","metadata":{"type":"Utilization","value":"50"}}}]}}}},"delta":[{"path":"properties.template.scale.rules","propertyChangeType":"Array","children":[{"path":"1","propertyChangeType":"Delete","before":{"name":"http-scaler","http":{"metadata":{"concurrentRequests":"10"}}}}]}]}]}
JSON
./scripts/check-what-if.sh "$tmp/cms-http-scaler-retirement.json"
jq '.changes[0].delta = [.changes[0].delta[0].children[0] | .path = "properties.template.scale.rules[1]"]' \
  "$tmp/cms-http-scaler-retirement.json" > "$tmp/cms-http-scaler-flat-retirement.json"
./scripts/check-what-if.sh "$tmp/cms-http-scaler-flat-retirement.json"
for mutation in \
  '.changes[0].resourceId |= sub("hhc-web-api$"; "other")' \
  '.changes[0].delta[0].path = "properties.template.containers"' \
  '.changes[0].delta[0].children[0].before.name = "cpu-scaler"' \
  '.changes[0].delta[0].children[0].before.http.metadata.concurrentRequests = "20"' \
  '.changes[0].after.properties.template.scale.rules[0].custom.metadata.value = "60"' \
  '.changes[0].after.properties.template.scale.rules += [.changes[0].after.properties.template.scale.rules[0]]' \
  '.changes[0].after.properties.template.scale.maxReplicas = 8' \
  '.changes[0].after.properties.template.containers[0].resources.cpu = 0.5' \
  'del(.changes[0].after)'; do
  jq "$mutation" "$tmp/cms-http-scaler-retirement.json" > "$tmp/cms-http-scaler-invalid.json"
  if ./scripts/check-what-if.sh "$tmp/cms-http-scaler-invalid.json" 2>/dev/null; then
    echo "unsafe CMS scaler retirement was accepted: $mutation" >&2
    exit 1
  fi
done

cat >"$tmp/retired-operations-workload.json" <<'JSON'
{"changes":[
  {"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers","propertyChangeType":"Array","children":[{"path":"0","propertyChangeType":"Modify","children":[{"path":"env","propertyChangeType":"Array","children":[{"path":"13","propertyChangeType":"Delete","before":{"name":"OPERATIONS_WORKLOAD_TENANT_ID","value":"tenant"}}]}]}]}]},
  {"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api/authConfigs/current","changeType":"Modify","delta":[{"path":"properties.identityProviders","propertyChangeType":"Delete","before":{"azureActiveDirectory":{"enabled":true}}}]}
]}
JSON
./scripts/check-what-if.sh "$tmp/retired-operations-workload.json"

cat >"$tmp/retired-operations-caller-env.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers","propertyChangeType":"Array","children":[{"path":"0","propertyChangeType":"Modify","children":[{"path":"env","propertyChangeType":"Array","children":[{"path":"13","propertyChangeType":"Delete","before":{"name":"OPERATIONS_ALLOWED_CALLER_APP_IDS","value":"asset-api"}}]}]}]}]}]}
JSON
./scripts/check-what-if.sh "$tmp/retired-operations-caller-env.json"

cat >"$tmp/worker-queue-shape-rewrite.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-bulletin-worker","changeType":"Modify","delta":[{"path":"properties.template.scale.rules","propertyChangeType":"Array","children":[{"path":"0","propertyChangeType":"Modify","children":[{"path":"azureQueue","propertyChangeType":"Delete","before":{"accountName":"examplestorage","identity":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity","queueLength":1,"queueName":"bulletin-watermark"}},{"path":"custom","propertyChangeType":"Create","after":{"type":"azure-queue"}}]}]}]}]}
JSON
./scripts/check-what-if.sh "$tmp/worker-queue-shape-rewrite.json"

cat >"$tmp/other-worker-queue-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-bulletin-worker","changeType":"Modify","delta":[{"path":"properties.template.scale.rules","propertyChangeType":"Array","children":[{"path":"0","propertyChangeType":"Modify","children":[{"path":"azureQueue","propertyChangeType":"Delete","before":{"queueName":"other"}},{"path":"custom","propertyChangeType":"Create"}]}]}]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/other-worker-queue-delete.json" 2>/dev/null; then
  echo "unrelated worker queue deletion was not rejected" >&2
  exit 1
fi

cat >"$tmp/translation-disabled-retained.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers[0].env[?name=='CMS_TRANSLATION_ENABLED'].value","propertyChangeType":"Modify"},{"path":"properties.template.containers[0].env[?name=='AZURE_OPENAI_ENDPOINT']","propertyChangeType":"NoEffect"},{"path":"properties.configuration.secrets[?name=='azure-openai-api-key']","propertyChangeType":"NoEffect"}]}]}
JSON
./scripts/check-what-if.sh "$tmp/translation-disabled-retained.json"

cat >"$tmp/translation-first-disabled-unbound.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers[0].env[?name=='CMS_TRANSLATION_ENABLED']","propertyChangeType":"Add"}]}]}
JSON
./scripts/check-what-if.sh "$tmp/translation-first-disabled-unbound.json"

cat >"$tmp/revision-suffix-recovery.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.revisionSuffix","propertyChangeType":"Delete"}]}]}
JSON
if ! ./scripts/check-what-if.sh "$tmp/revision-suffix-recovery.json"; then
  echo "hhc-web-api revision suffix recovery was rejected" >&2
  exit 1
fi

cat >"$tmp/mixed-case-revision-suffix-recovery.json" <<'JSON'
{"changes":[{"resourceId":"/SUBSCRIPTIONS/test/RESOURCEGROUPS/alive/PROVIDERS/microsoft.app/CONTAINERAPPS/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.revisionSuffix","propertyChangeType":"Delete"}]}]}
JSON
if ! ./scripts/check-what-if.sh "$tmp/mixed-case-revision-suffix-recovery.json"; then
  echo "mixed-case hhc-web-api revision suffix recovery was rejected" >&2
  exit 1
fi

cat >"$tmp/other-resource-revision-suffix-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/jobs/hhc-web-migrate","changeType":"Modify","delta":[{"path":"properties.template.revisionSuffix","propertyChangeType":"Delete"}]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/other-resource-revision-suffix-delete.json" 2>/dev/null; then
  echo "revision suffix delete on another resource was not rejected" >&2
  exit 1
fi

cat >"$tmp/nested-revision-suffix-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template","propertyChangeType":"Object","children":[{"path":"properties.template.revisionSuffix","propertyChangeType":"Delete"}]}]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/nested-revision-suffix-delete.json" 2>/dev/null; then
  echo "nested revision suffix delete was not rejected" >&2
  exit 1
fi

cat >"$tmp/extension-resource-revision-suffix-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Contoso.Extensions/widgets/example/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.revisionSuffix","propertyChangeType":"Delete"}]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/extension-resource-revision-suffix-delete.json" 2>/dev/null; then
  echo "extension resource revision suffix delete was not rejected" >&2
  exit 1
fi

cat >"$tmp/web-api-resource-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Delete","delta":[]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/web-api-resource-delete.json" 2>/dev/null; then
  echo "hhc-web-api resource delete was not rejected" >&2
  exit 1
fi

cat >"$tmp/nested-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers","propertyChangeType":"Array","children":[{"path":"env","propertyChangeType":"Delete"}]}]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/nested-delete.json" 2>/dev/null; then
  echo "nested delete was not rejected" >&2
  exit 1
fi

cat >"$tmp/translation-binding-delete.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.configuration.secrets[?name=='azure-openai-api-key']","propertyChangeType":"Delete"}]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/translation-binding-delete.json" 2>/dev/null; then
  echo "translation binding delete was not rejected" >&2
  exit 1
fi

cat >"$tmp/unrelated-modify.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.KeyVault/vaults/alive-vault","changeType":"Modify","delta":[]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/unrelated-modify.json" 2>/dev/null; then
  echo "unrelated infrastructure modification was not rejected" >&2
  exit 1
fi

cat >"$tmp/unrelated-worker.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-bulletin-worker-other","changeType":"Create","delta":[]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/unrelated-worker.json" 2>/dev/null; then
  echo "unrelated worker creation was not rejected" >&2
  exit 1
fi

cat >"$tmp/unrelated-ai-policy.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.CognitiveServices/accounts/example-openai/raiPolicies/other-policy","changeType":"Create","delta":[]}]}
JSON
if ./scripts/check-what-if.sh "$tmp/unrelated-ai-policy.json" 2>/dev/null; then
  echo "unrelated AI policy creation was not rejected" >&2
  exit 1
fi

# Exercise the full isolated job payload, not merely a resource-name allowlist.
jq -n '{changes:[{
  resourceId:"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/jobs/hhc-web-bulletin-extractor",
  changeType:"Create",delta:[],after:{
    identity:{type:"UserAssigned",userAssignedIdentities:{"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity":{}}},
    properties:{environmentId:"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/managedEnvironments/example-env",workloadProfileName:"Consumption",
      configuration:{triggerType:"Event",replicaTimeout:150,replicaRetryLimit:0,eventTriggerConfig:{parallelism:1,replicaCompletionCount:1,scale:{pollingInterval:10,minExecutions:0,maxExecutions:1,rules:[{name:"bulletin-extraction-queue",type:"azure-queue",identity:"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity",metadata:{accountName:"examplestorage",queueName:"bulletin-extraction",queueLength:"1",queueLengthStrategy:"visibleonly"}}]}},
        registries:[{server:"example.azurecr.io",identity:"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity"}],
        secrets:[{name:"database-url",keyVaultUrl:"https://example-runtime-kv.vault.azure.net/secrets/jobs-database-url",identity:"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity"}]},
      template:{containers:[{name:"bulletin-extractor",image:("example.azurecr.io/example/hhc-web-bulletin-extractor@sha256:"+("a"*64)),command:["/bulletin-extractor"],resources:{cpu:1,memory:"2Gi"},env:[
        {name:"DATABASE_URL",secretRef:"database-url"},
        {name:"BULLETIN_EXTRACTION_ENABLED",value:"false"},
        {name:"BULLETIN_EXTRACTION_QUEUE_URL",value:"https://examplestorage.queue.core.windows.net/bulletin-extraction"},
        {name:"BULLETIN_EXTRACTION_POISON_QUEUE_URL",value:"https://examplestorage.queue.core.windows.net/bulletin-extraction-poison"},
        {name:"BULLETIN_EXTRACTION_TIMEOUT",value:"120s"},
        {name:"BULLETIN_EXTRACTION_BATCH_SIZE",value:"1"},
        {name:"BULLETIN_EXTRACTION_MAX_ATTEMPTS",value:"3"},
        {name:"AZURE_CLIENT_ID",value:"00000000-0000-4000-8000-000000000001"},
        {name:"ASSET_API_BASE_URL",value:"https://asset-api.internal.example.azurecontainerapps.io"},
        {name:"ASSET_API_AUDIENCE",value:"api://asset-api"},
        {name:"BULLETIN_EXTRACTOR_ASSET_ORIGIN",value:"https://www.alive.org.tw"}
      ]}]}
    }
  }
}]}' > "$tmp/extractor.json"
./scripts/check-what-if.sh "$tmp/extractor.json"
jq '.changes[0].delta=[{path:"properties.configuration.scheduleTriggerConfig",propertyChangeType:"Delete"}]' "$tmp/extractor.json" > "$tmp/extractor-remove-schedule.json"
./scripts/check-what-if.sh "$tmp/extractor-remove-schedule.json"
jq '.changes[0].delta[0].path="properties.configuration.secrets"' "$tmp/extractor-remove-schedule.json" > "$tmp/unsafe-extractor-delete.json"
if ./scripts/check-what-if.sh "$tmp/unsafe-extractor-delete.json" 2>/dev/null; then
  echo 'unrelated extractor deletion accepted' >&2; exit 1
fi
jq '.changes[0].after.identity.userAssignedIdentities |= with_entries(.key |= ascii_downcase)
  | .changes[0].after.properties.environmentId |= ascii_downcase
  | .changes[0].after.properties.configuration.registries[0].identity |= ascii_downcase
  | .changes[0].after.properties.configuration.secrets[0].identity |= ascii_downcase' "$tmp/extractor.json" > "$tmp/extractor-arm-id-case.json"
./scripts/check-what-if.sh "$tmp/extractor-arm-id-case.json"
jq '.changes[0].after.identity.userAssignedIdentities |= with_entries(.key |= sub("hhc-web-api-identity"; "other-identity"))' "$tmp/extractor-arm-id-case.json" > "$tmp/unsafe-extractor.json"
if ./scripts/check-what-if.sh "$tmp/unsafe-extractor.json" 2>/dev/null; then
  echo 'wrong identity accepted after ARM ID case normalization' >&2
  exit 1
fi
jq '.changes[0].changeType="Modify"
  | .changes[0].after.properties.configuration.registries[0] += {username:null,passwordSecretRef:null}
  | .changes[0].after.properties.configuration.secrets[0].value=null
  | .changes[0].after.properties.template += {initContainers:null,volumes:null}
  | .changes[0].after.properties.configuration.secrets += [{name:"azure-openai-api-key",keyVaultUrl:"https://example-runtime-kv.vault.azure.net/secrets/hhc-web-azure-openai-api-key",identity:"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity",value:null}]
  | .changes[0].after.properties.template.containers[0].env += [
    {name:"AZURE_OPENAI_ENDPOINT",value:"https://example.openai.azure.com/"},
    {name:"AZURE_OPENAI_DEPLOYMENT",value:"classifier"},
    {name:"AZURE_OPENAI_API_KEY",secretRef:"azure-openai-api-key",value:null},
    {name:"AZURE_OPENAI_RAI_POLICY",value:"hhc-cms-translation-v1"}]' "$tmp/extractor.json" > "$tmp/extractor-ai.json"
./scripts/check-what-if.sh "$tmp/extractor-ai.json"
jq '.changes[0].after.properties.configuration.replicaTimeout=720 | .changes[0].after.properties.template.containers[0].env |= (map(if .name=="BULLETIN_EXTRACTION_ENABLED" then .value="true" else . end) + [{name:"BULLETIN_LLM_EXTRACTION_ENABLED",value:"true"}])' "$tmp/extractor-ai.json" > "$tmp/extractor-llm.json"
./scripts/check-what-if.sh "$tmp/extractor-llm.json"
jq '.changes[0].after.properties.configuration.replicaTimeout=150' "$tmp/extractor-llm.json" > "$tmp/extractor-llm-short.json"
if ./scripts/check-what-if.sh "$tmp/extractor-llm-short.json" 2>/dev/null; then
 echo 'LLM worker accepted with an expired replica envelope' >&2
 exit 1
fi
jq '.changes[0].after as $job
  | "/subscriptions/test/resourceGroups/example-group/providers/" as $scope
  | .changes[0].after.properties.configuration.registries[0].server = ("[reference(" + "\u0027" + $scope + "Microsoft.ContainerRegistry/registries/example\u0027, \u00272023-07-01\u0027).loginServer]")
  | .changes[0].after.properties.configuration.secrets |= map(.name="*******" | .keyVaultUrl = ("[format(\u0027{0}secrets/" + (.keyVaultUrl | split("/secrets/")[1]) + "\u0027, reference(\u0027" + $scope + "Microsoft.KeyVault/vaults/example-runtime-kv\u0027, \u00272026-02-01\u0027).vaultUri)]"))
  | .changes[0].after.properties.template.containers[0].env |= map(
      if .secretRef then .secretRef="*******"
      elif .name=="AZURE_CLIENT_ID" then .value=("[reference(\u0027" + $scope + "Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity\u0027, \u00272023-01-31\u0027).clientId]")
      elif .name=="ASSET_API_BASE_URL" then .value=("[format(\u0027https://{0}\u0027, reference(\u0027" + $scope + "Microsoft.App/containerApps/asset-api\u0027, \u00272025-01-01\u0027).configuration.ingress.fqdn)]")
      else . end)' "$tmp/extractor-ai.json" > "$tmp/extractor-preview.json"
./scripts/check-what-if.sh "$tmp/extractor-preview.json" preview
if ./scripts/check-what-if.sh "$tmp/extractor-preview.json" 2>/dev/null; then
  echo 'redacted preview accepted as deployed configuration' >&2
  exit 1
fi
for mutation in \
  '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl |= sub("jobs-database-url"; "database-url")' \
  '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl |= sub("2026-02-01"; "2023-07-01")' \
  '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl |= sub("example-runtime-kv"; "other-vault")' \
  '.changes[0].after.properties.configuration.secrets[1] = .changes[0].after.properties.configuration.secrets[0]' \
  '.changes[0].after.properties.configuration.secrets[1].keyVaultUrl="https://example-runtime-kv.vault.azure.net/secrets/database-url"' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="AZURE_CLIENT_ID" then .value |= sub("hhc-web-api-identity"; "other-identity") else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="ASSET_API_BASE_URL" then .value |= sub("containerApps/asset-api"; "containerApps/other") else . end)'; do
  jq "$mutation" "$tmp/extractor-preview.json" > "$tmp/unsafe-extractor.json"
  if ./scripts/check-what-if.sh "$tmp/unsafe-extractor.json" preview 2>/dev/null; then
    echo 'unsafe preview resource reference accepted' >&2
    exit 1
  fi
done
jq '.changes[0].after.properties.configuration.registries[0].server |= sub("registries/example"; "registries/other")' "$tmp/extractor-preview.json" > "$tmp/unsafe-extractor.json"
if ./scripts/check-what-if.sh "$tmp/unsafe-extractor.json" preview 2>/dev/null; then
  echo 'unrelated ARM resource accepted' >&2
  exit 1
fi
for mutation in \
  '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl |= sub("jobs-database-url"; "database-url")' \
  'del(.changes[0].after.properties.template.containers[0].env[-1])' \
  '.changes[0].after.properties.template.containers[0].env[-2].value="inline-secret"'; do
  jq "$mutation" "$tmp/extractor-ai.json" > "$tmp/unsafe-extractor.json"
  if ./scripts/check-what-if.sh "$tmp/unsafe-extractor.json" 2>/dev/null; then
    echo "unsafe extractor AI configuration accepted" >&2
    exit 1
  fi
done
for mutation in \
  '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl="https://example-runtime-kv.vault.azure.net/secrets/database-url"' \
  '.changes[0].resourceId += "-other"' \
  '.changes[0].changeType="Delete"' \
  '.changes[0].after.properties.template.containers[0].image="example.azurecr.io/example/hhc-web-bulletin-extractor:main"' \
  '.changes[0].after.properties.template.containers[0].resources.cpu=2' \
  '.changes[0].after.properties.template.containers[0].resources.memory="4Gi"' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.parallelism=2' \
  '.changes[0].after.properties.configuration.scheduleTriggerConfig={cronExpression:"* * * * *"}' \
  '.changes[0].after.properties.configuration.replicaTimeout=900' \
  '.changes[0].after.properties.configuration.dapr={enabled:true}' \
  '.changes[0].after.properties.template.containers[0].env += [{name:"APP_API_TOKEN",value:"not-allowed"}]' \
  '.changes[0].after.properties.configuration.secrets[0].value="inline-secret"' \
  '.changes[0].after.identity.type="SystemAssigned"' \
  '.changes[0].after.properties.template.containers[0].env[7].value=""' \
  '.changes[0].after.properties.template.containers[0].env[1].value="yes"'; do
  jq "$mutation" "$tmp/extractor.json" > "$tmp/unsafe-extractor.json"
  if ./scripts/check-what-if.sh "$tmp/unsafe-extractor.json" 2>/dev/null; then
    echo "unsafe extractor payload accepted: $mutation" >&2
    exit 1
  fi
done

# The event-driven watermark Job (PLAN-v2 W5) is admitted only with its exact
# identity, trigger, secret, image and env shape. Deployed readback first.
jq -n '"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-bulletin-worker-identity" as $identity
  | {changes:[{
  resourceId:"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/jobs/hhc-web-bulletin-watermark",
  changeType:"Create",delta:[],after:{
    identity:{type:"UserAssigned",userAssignedIdentities:{($identity):{}}},
    properties:{environmentId:"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/managedEnvironments/example-env",workloadProfileName:"Consumption",
      configuration:{triggerType:"Event",replicaTimeout:300,replicaRetryLimit:0,
        eventTriggerConfig:{parallelism:1,replicaCompletionCount:1,scale:{pollingInterval:10,minExecutions:0,maxExecutions:20,rules:[
          {name:"bulletin-watermark-queue",type:"azure-queue",identity:$identity,
           metadata:{accountName:"examplestorage",queueName:"bulletin-watermark",queueLength:"1",queueLengthStrategy:"visibleonly"}}]}},
        registries:[{server:"example.azurecr.io",identity:$identity}],
        secrets:[{name:"database-url",keyVaultUrl:"https://example-runtime-kv.vault.azure.net/secrets/jobs-database-url",identity:$identity}]},
      template:{containers:[{name:"bulletin-watermark",image:("example.azurecr.io/example/hhc-web-api@sha256:"+("b"*64)),
        command:["/hhc-web-bulletin-worker"],args:["--once"],resources:{cpu:2,memory:"4Gi"},env:[
        {name:"ENVIRONMENT",value:"production"},
        {name:"RELEASE",value:("c"*40)},
        {name:"DATABASE_URL",secretRef:"database-url"},
        {name:"DB_MAX_OPEN_CONNS",value:"1"},
        {name:"DB_MAX_IDLE_CONNS",value:"1"},
        {name:"DB_CONN_MAX_LIFETIME",value:"30m"},
        {name:"ALLOW_DEV_CALLER_HEADER",value:"false"},
        {name:"CMS_TRANSLATION_ENABLED",value:"false"},
        {name:"BULLETIN_WATERMARK_PRIVACY_VERSION",value:"privacy-test"},
        {name:"BULLETIN_WATERMARK_QUEUE_URL",value:"https://examplestorage.queue.core.windows.net/bulletin-watermark"},
        {name:"BULLETIN_WATERMARK_POISON_QUEUE_URL",value:"https://examplestorage.queue.core.windows.net/bulletin-watermark-poison"},
        {name:"BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED",value:"false"},
        {name:"AZURE_CLIENT_ID",value:"00000000-0000-4000-8000-000000000002"},
        {name:"BULLETIN_WORKER_ASSET_AUTH",value:"workload"},
        {name:"BULLETIN_WORKER_ASSET_API_BASE_URL",value:"https://asset-api.internal.example.eastasia.azurecontainerapps.io"},
        {name:"BULLETIN_WORKER_ASSET_API_AUDIENCE",value:"api://asset-api"},
        {name:"BULLETIN_WORKER_ASSET_CLIENT_ID",value:"00000000-0000-4000-8000-000000000002"}
      ]}]}
    }
  }
}]}' > "$tmp/watermark-job.json"
./scripts/check-what-if.sh "$tmp/watermark-job.json"
# Azure readback shape: lower-case ARM IDs, empty registry credentials and
# scale-rule auth, explicit null trigger configs, Modify.
jq '.changes[0].changeType="Modify"
  | .changes[0].after.identity.userAssignedIdentities |= with_entries(.key |= ascii_downcase)
  | .changes[0].after.properties.configuration.registries[0] += {username:"",passwordSecretRef:""}
  | .changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0] += {auth:[]}
  | .changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0].identity |= ascii_downcase
  | .changes[0].after.properties.configuration += {manualTriggerConfig:null,scheduleTriggerConfig:null,dapr:null,ingress:null}
  | .changes[0].after.properties.template += {initContainers:null,volumes:null}
  | .changes[0].after.properties.template.containers[0].resources += {ephemeralStorage:"8Gi"}' "$tmp/watermark-job.json" > "$tmp/watermark-job-readback.json"
./scripts/check-what-if.sh "$tmp/watermark-job-readback.json"
# The rollback disables admission with maxExecutions 0; that stays valid.
jq '.changes[0].after.properties.configuration.eventTriggerConfig.scale.maxExecutions=0' "$tmp/watermark-job.json" > "$tmp/watermark-job-paused.json"
./scripts/check-what-if.sh "$tmp/watermark-job-paused.json"
# Preview: masked secrets and unresolved existing-resource references.
jq '"/subscriptions/test/resourceGroups/example-group/providers/" as $scope
  | .changes[0].after.properties.configuration.registries[0].server = ("[reference(\u0027" + $scope + "Microsoft.ContainerRegistry/registries/example\u0027, \u00272023-07-01\u0027).loginServer]")
  | .changes[0].after.properties.configuration.secrets[0] |= (.name="*******" | .keyVaultUrl=("[format(\u0027{0}secrets/jobs-database-url\u0027, reference(\u0027" + $scope + "Microsoft.KeyVault/vaults/example-runtime-kv\u0027, \u00272026-02-01\u0027).vaultUri)]"))
  | ("[reference(\u0027" + $scope + "Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-bulletin-worker-identity\u0027, \u00272023-01-31\u0027).clientId]") as $client
  | .changes[0].after.properties.template.containers[0].env |= map(
      if .secretRef then .secretRef="*******"
      elif .name=="AZURE_CLIENT_ID" or .name=="BULLETIN_WORKER_ASSET_CLIENT_ID" then .value=$client
      elif .name=="BULLETIN_WORKER_ASSET_API_BASE_URL" then .value=("[format(\u0027https://{0}\u0027, reference(\u0027" + $scope + "Microsoft.App/containerApps/asset-api\u0027, \u00272025-01-01\u0027).configuration.ingress.fqdn)]")
      else . end)' "$tmp/watermark-job.json" > "$tmp/watermark-job-preview.json"
./scripts/check-what-if.sh "$tmp/watermark-job-preview.json" preview
if ./scripts/check-what-if.sh "$tmp/watermark-job-preview.json" 2>/dev/null; then
  echo 'redacted watermark Job preview accepted as deployed configuration' >&2
  exit 1
fi
for mutation in \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="AZURE_CLIENT_ID" then .value |= sub("hhc-web-bulletin-worker-identity"; "hhc-web-api-identity") else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="BULLETIN_WORKER_ASSET_CLIENT_ID" then .value |= sub("hhc-web-bulletin-worker-identity"; "other-identity") else . end)' \
  '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl |= sub("jobs-database-url"; "database-url")' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="BULLETIN_WORKER_ASSET_API_BASE_URL" then .value |= sub("containerApps/asset-api"; "containerApps/other") else . end)'; do
  jq "$mutation" "$tmp/watermark-job-preview.json" > "$tmp/unsafe-watermark-job.json"
  if ./scripts/check-what-if.sh "$tmp/unsafe-watermark-job.json" preview 2>/dev/null; then
    echo "unsafe watermark Job preview accepted: $mutation" >&2
    exit 1
  fi
done
api_identity=/subscriptions/test/resourceGroups/example-group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity
for mutation in \
  '.changes[0].changeType="Delete"' \
  '.changes[0].resourceId += "-other"' \
  ".changes[0].after.identity.userAssignedIdentities += {\"$api_identity\":{}}" \
  ".changes[0].after.identity.userAssignedIdentities = {\"$api_identity\":{}}" \
  '.changes[0].after.identity.type="SystemAssigned,UserAssigned"' \
  ".changes[0].after.properties.configuration.registries[0].identity=\"$api_identity\"" \
  ".changes[0].after.properties.configuration.secrets[0].identity=\"$api_identity\"" \
  ".changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0].identity=\"$api_identity\"" \
  'del(.changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0].identity)' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0] += {auth:[{secretRef:"queue-key",triggerParameter:"connection"}]}' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0].metadata.queueLengthStrategy="all"' \
  'del(.changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0].metadata.queueLengthStrategy)' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0].metadata.queueName="bulletin-watermark-poison"' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0].metadata.queueLength="5"' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.scale.rules += [.changes[0].after.properties.configuration.eventTriggerConfig.scale.rules[0]]' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.scale.maxExecutions=21' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.scale.maxExecutions="20"' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.scale.minExecutions=1' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.scale.pollingInterval=30' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.parallelism=2' \
  '.changes[0].after.properties.configuration.eventTriggerConfig.replicaCompletionCount=2' \
  '.changes[0].after.properties.configuration.triggerType="Manual"' \
  '.changes[0].after.properties.configuration += {manualTriggerConfig:{parallelism:1,replicaCompletionCount:1}}' \
  '.changes[0].after.properties.configuration.replicaTimeout=900' \
  '.changes[0].after.properties.configuration.replicaRetryLimit=1' \
  '.changes[0].after.properties.configuration.dapr={enabled:true,appId:"hhc-web-bulletin-worker"}' \
  '.changes[0].after.properties.configuration.ingress={external:false,targetPort:8082}' \
  '.changes[0].after.properties.configuration.registries[0].server="docker.io"' \
  '.changes[0].after.properties.configuration.registries[0] += {username:"alive",passwordSecretRef:"acr-password"}' \
  '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl="https://example-runtime-kv.vault.azure.net/secrets/database-url"' \
  '.changes[0].after.properties.configuration.secrets[0].value="inline-secret"' \
  '.changes[0].after.properties.configuration.secrets += [{name:"audit-token",keyVaultUrl:"https://alive-vault.vault.azure.net/secrets/audit",identity:.changes[0].after.properties.configuration.secrets[0].identity}]' \
  '.changes[0].after.properties.template.containers[0].image="example.azurecr.io/example/hhc-web-api:main"' \
  '.changes[0].after.properties.template.containers[0].image=("example.azurecr.io/example/hhc-web-bulletin-extractor@sha256:"+("b"*64))' \
  '.changes[0].after.properties.template.containers[0].args=[]' \
  'del(.changes[0].after.properties.template.containers[0].args)' \
  '.changes[0].after.properties.template.containers[0].args=["--preflight"]' \
  '.changes[0].after.properties.template.containers[0].command=["/bin/sh"]' \
  '.changes[0].after.properties.template.containers[0].name="hhc-web-bulletin-worker"' \
  '.changes[0].after.properties.template.containers[0].resources.cpu=1' \
  '.changes[0].after.properties.template.containers[0].resources.memory="2Gi"' \
  '.changes[0].after.properties.template.containers += [.changes[0].after.properties.template.containers[0]]' \
  '.changes[0].after.properties.template.initContainers=[{name:"init",image:"busybox"}]' \
  '.changes[0].after.properties.template.volumes=[{name:"scratch",storageType:"EmptyDir"}]' \
  '.changes[0].after.properties.template.containers[0].env += [{name:"APP_API_TOKEN",value:"not-allowed"}]' \
  '.changes[0].after.properties.template.containers[0].env += [{name:"AZURE_CLIENT_SECRET",secretRef:"database-url"}]' \
  'del(.changes[0].after.properties.template.containers[0].env[] | select(.name=="BULLETIN_WORKER_ASSET_API_AUDIENCE"))' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="DATABASE_URL" then {name,value:"postgres://inline"} else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="DB_MAX_OPEN_CONNS" then .value="2" else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED" then .value="true" else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="BULLETIN_WORKER_ASSET_AUTH" then .value="dapr" else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="BULLETIN_WORKER_ASSET_CLIENT_ID" then .value="00000000-0000-4000-8000-000000000003" else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="BULLETIN_WORKER_ASSET_API_BASE_URL" then .value="http://localhost:3500/v1.0/invoke/asset-api/method" else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="BULLETIN_WATERMARK_QUEUE_URL" then .value="https://other.queue.core.windows.net/bulletin-watermark" else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="BULLETIN_WATERMARK_PRIVACY_VERSION" then .value="" else . end)' \
  '.changes[0].after.properties.template.containers[0].env |= map(if .name=="RELEASE" then .value="unknown" else . end)'; do
  jq "$mutation" "$tmp/watermark-job.json" > "$tmp/unsafe-watermark-job.json"
  if ./scripts/check-what-if.sh "$tmp/unsafe-watermark-job.json" 2>/dev/null; then
    echo "unsafe watermark Job payload accepted: $mutation" >&2
    exit 1
  fi
done

cat >"$tmp/cms-locations-disable.json" <<'JSON'
{"changes":[{"resourceId":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api","changeType":"Modify","delta":[{"path":"properties.template.containers","propertyChangeType":"Array","children":[{"path":"0","propertyChangeType":"Modify","children":[{"path":"env","propertyChangeType":"Array","children":[{"path":"34","propertyChangeType":"Delete","before":{"name":"CMS_GATEWAY_LOCATIONS_TOKEN","secretRef":"cms-gateway-locations-token"}}]}]}]}],"after":{"properties":{"template":{"containers":[{"name":"hhc-web-api","env":[]}]},"configuration":{"secrets":[{"name":"cms-gateway-locations-token","keyVaultUrl":"https://example-runtime-kv.vault.azure.net/secrets/cms-gateway-locations-token/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","identity":"/subscriptions/test/resourceGroups/example-group/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity"}]}}}}]}
JSON
./scripts/check-what-if.sh "$tmp/cms-locations-disable.json" preview disable aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
expression="[format('{0}secrets/cms-gateway-locations-token/{1}', reference('/subscriptions/test/resourceGroups/example-group/providers/Microsoft.KeyVault/vaults/example-runtime-kv', '2026-02-01').vaultUri, 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')]"
jq --arg uri "$expression" '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl=$uri' \
  "$tmp/cms-locations-disable.json" >"$tmp/cms-locations-expression.json"
./scripts/check-what-if.sh "$tmp/cms-locations-expression.json" preview disable aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
jq '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl |= sub("resourceGroups/example-group";"resourceGroups/other")' \
  "$tmp/cms-locations-expression.json" >"$tmp/cms-locations-unsafe-expression.json"
! cmp -s "$tmp/cms-locations-expression.json" "$tmp/cms-locations-unsafe-expression.json"
if ./scripts/check-what-if.sh "$tmp/cms-locations-unsafe-expression.json" preview disable aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2>/dev/null; then
  echo 'Alias rollback accepted a different vault resource scope' >&2
  exit 1
fi
if ./scripts/check-what-if.sh "$tmp/cms-locations-disable.json" preview 2>/dev/null; then
  echo 'Alias removal requires explicit disable intent' >&2
  exit 1
fi
for mutation in \
  '.changes[0].resourceId |= sub("containerApps/hhc-web-api";"jobs/hhc-web-migrate")' \
  '.changes[0].delta[0].children[0].path="1"' \
  '.changes[0].delta[0].children[0].children[0].path="secrets"' \
  '.changes[0].delta[0].children[0].children[0].children[0].before.secretRef="other"' \
  '.changes[0].delta[0].children[0].children[0].children[0].before.value="inline"' \
  '.changes[0].after.properties.configuration.secrets=[]' \
  '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl |= sub("aaaa";"bbbb")' \
  '.changes[0].after.properties.configuration.secrets[0].keyVaultUrl="*******"' \
  '.changes[0].after.properties.configuration.secrets[0].identity |= sub("hhc-web-api";"other")' \
  '.changes[0].after.properties.configuration.secrets[0].identity="*******"' \
  '.changes[0].after.properties.configuration.secrets[0].value="inline"'; do
  jq "$mutation" "$tmp/cms-locations-disable.json" >"$tmp/cms-locations-unsafe.json"
  if ./scripts/check-what-if.sh "$tmp/cms-locations-unsafe.json" preview disable aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2>/dev/null; then
    echo "Unsafe alias rollback accepted: $mutation" >&2
    exit 1
  fi
done
