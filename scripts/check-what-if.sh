#!/bin/sh
set -eu

file="${1:?what-if JSON is required}"

jq -e --argjson targets "$(python3 scripts/deployment_config.py targets)" --arg mode "${2:-deployed}" --arg locations_transition "${3:-preserve}" --arg locations_version "${4:-}" '
  # Preview masks secret references and leaves exact existing-resource ARM
  # expressions unresolved. Deployed readback must still contain real values.
  def reference($scope; $resource; $version; $property):
    "[reference(\u0027" + $scope + "/providers/" + $resource + "\u0027, \u0027" + $version + "\u0027)." + $property + "]";
  def matches($literal; $expression):
    . == $literal or ($mode == "preview" and . == $expression);
  def secret_name($name):
    . == $name or ($mode == "preview" and . == "*******");
  def is_hhc_web_api_resource:
    test("^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\\.App/containerApps/hhc-web-api$"; "i");
  def is_bulletin_worker:
    test("^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\\.App/containerApps/hhc-web-bulletin-worker$"; "i");
  def is_bulletin_extractor:
    test("^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\\.App/jobs/hhc-web-bulletin-extractor$"; "i");
  def valid_bulletin_extractor:
    . as $change
    | (.resourceId | sub("/providers/Microsoft.App/jobs/hhc-web-bulletin-extractor$"; ""; "i")) as $scope
    | ($scope + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity") as $identity
    | .after as $job
    | $job.properties.configuration as $config
    | $job.properties.template.containers[0] as $container
    | ($container.env | map({key:.name,value:.}) | from_entries) as $env
    | ($env | has("AZURE_OPENAI_ENDPOINT")) as $ai
    | ($job.identity.type == "UserAssigned")
    and (($job.identity.userAssignedIdentities | keys | map(ascii_downcase)) == [($identity | ascii_downcase)])
    and (($job.properties.environmentId | ascii_downcase) == (($scope + "/providers/Microsoft.App/managedEnvironments/\($targets.CONTAINER_APP_ENVIRONMENT_NAME)") | ascii_downcase))
    and ($job.properties.workloadProfileName == "Consumption")
    and ($config.triggerType == "Event" and $config.replicaTimeout == (if $env.BULLETIN_LLM_EXTRACTION_ENABLED.value == "true" then 720 else 150 end) and $config.replicaRetryLimit == 0)
    and ($config.scheduleTriggerConfig == null and $config.manualTriggerConfig == null)
    and ($config.eventTriggerConfig.parallelism == 1 and $config.eventTriggerConfig.replicaCompletionCount == 1)
    and ($config.eventTriggerConfig.scale.minExecutions == 0 and $config.eventTriggerConfig.scale.maxExecutions == 1 and $config.eventTriggerConfig.scale.pollingInterval == 10)
    and (($config.eventTriggerConfig.scale.rules | length) == 1)
    and ($config.eventTriggerConfig.scale.rules[0] as $rule |
      $rule.name == "bulletin-extraction-queue" and $rule.type == "azure-queue"
      and ($rule.identity | ascii_downcase) == ($identity | ascii_downcase)
      and (($rule.auth // []) | length) == 0
      and $rule.metadata == {accountName:"\($targets.QUEUE_STORAGE_ACCOUNT_NAME)",queueName:"bulletin-extraction",queueLength:"1",queueLengthStrategy:"visibleonly"})
    and ($config.dapr == null and $config.ingress == null)
    and (($config.registries | length) == 1)
    and ($config.registries[0].server | matches("\($targets.ACR_LOGIN_SERVER)"; reference($scope; "Microsoft.ContainerRegistry/registries/\($targets.ACR_NAME)"; "2023-07-01"; "loginServer")))
    and (($config.registries[0].identity | ascii_downcase) == ($identity | ascii_downcase))
    and (($config.registries[0].username // "") == "" and ($config.registries[0].passwordSecretRef // "") == "")
    and (($config.secrets | length) == (if $ai then 2 else 1 end))
    and all($config.secrets[];
      (.identity | ascii_downcase) == ($identity | ascii_downcase) and (.value // "") == "" and
      (((.name | secret_name("database-url")) and (.keyVaultUrl | matches("https://\($targets.RUNTIME_KEY_VAULT_NAME).vault.azure.net/secrets/jobs-database-url"; "[format(\u0027{0}secrets/jobs-database-url\u0027, reference(\u0027" + $scope + "/providers/Microsoft.KeyVault/vaults/\($targets.RUNTIME_KEY_VAULT_NAME)\u0027, \u00272026-02-01\u0027).vaultUri)]")))
      or ($ai and (.name | secret_name("azure-openai-api-key")) and (.keyVaultUrl | matches("https://\($targets.RUNTIME_KEY_VAULT_NAME).vault.azure.net/secrets/hhc-web-azure-openai-api-key"; "[format(\u0027{0}secrets/hhc-web-azure-openai-api-key\u0027, reference(\u0027" + $scope + "/providers/Microsoft.KeyVault/vaults/\($targets.RUNTIME_KEY_VAULT_NAME)\u0027, \u00272026-02-01\u0027).vaultUri)]")))))
    and ([$config.secrets[] | select(.keyVaultUrl | contains("secrets/jobs-database-url"))] | length == 1)
    and (($job.properties.template.containers | length) == 1)
    and (($job.properties.template.initContainers // [] | length) == 0)
    and (($job.properties.template.volumes // [] | length) == 0)
    and ($container.name == "bulletin-extractor" and $container.command == ["/bulletin-extractor"])
    and (($container.args // [] | length) == 0)
    and (($container.image // "") | test($targets.extractorImagePattern))
    and ($container.resources.cpu == 1 and $container.resources.memory == "2Gi")
    and (($container.env | length) == ($env | length))
    and all($container.env[]; .name == "DATABASE_URL" or .name == "AZURE_OPENAI_API_KEY" or .secretRef == null)
    and all($env | keys[]; . as $name | ["DATABASE_URL","BULLETIN_EXTRACTION_QUEUE_URL","BULLETIN_EXTRACTION_POISON_QUEUE_URL","BULLETIN_EXTRACTION_ENABLED","BULLETIN_LLM_EXTRACTION_ENABLED","BULLETIN_EXTRACTION_TIMEOUT","BULLETIN_EXTRACTION_BATCH_SIZE","BULLETIN_EXTRACTION_MAX_ATTEMPTS","AZURE_CLIENT_ID","ASSET_API_BASE_URL","ASSET_API_AUDIENCE","BULLETIN_EXTRACTOR_ASSET_ORIGIN","ENVIRONMENT","RELEASE","AZURE_OPENAI_ENDPOINT","AZURE_OPENAI_DEPLOYMENT","AZURE_OPENAI_API_KEY","AZURE_OPENAI_RAI_POLICY"] | index($name) != null)
    and (($env.DATABASE_URL.secretRef | secret_name("database-url")) and ($env.DATABASE_URL.value // "") == "")
    and (($env.BULLETIN_LLM_EXTRACTION_ENABLED.value // "false") as $llm | ($llm == "true" or $llm == "false") and ($llm != "true" or ($ai and $env.BULLETIN_EXTRACTION_ENABLED.value == "true")))
    and ($env.BULLETIN_EXTRACTION_ENABLED.value == "false" or $env.BULLETIN_EXTRACTION_ENABLED.value == "true")
    and ($env.BULLETIN_EXTRACTION_QUEUE_URL.value == "https://\($targets.QUEUE_STORAGE_ACCOUNT_NAME).queue.core.windows.net/bulletin-extraction")
    and ($env.BULLETIN_EXTRACTION_POISON_QUEUE_URL.value == "https://\($targets.QUEUE_STORAGE_ACCOUNT_NAME).queue.core.windows.net/bulletin-extraction-poison")
    and ($env.BULLETIN_EXTRACTION_TIMEOUT.value == "120s" and $env.BULLETIN_EXTRACTION_BATCH_SIZE.value == "1" and $env.BULLETIN_EXTRACTION_MAX_ATTEMPTS.value == "3")
    and (($env.AZURE_CLIENT_ID.value // "") | test("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$") or ($mode == "preview" and . == reference($scope; "Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity"; "2023-01-31"; "clientId")))
    and (($env.ASSET_API_BASE_URL.value // "") | test("^https://asset-api\\.internal\\.[a-z0-9.-]+\\.azurecontainerapps\\.io$") or ($mode == "preview" and . == "[format(\u0027https://{0}\u0027, reference(\u0027" + $scope + "/providers/Microsoft.App/containerApps/asset-api\u0027, \u00272025-01-01\u0027).configuration.ingress.fqdn)]"))
    and (($env.ASSET_API_AUDIENCE.value // "" | length) > 0)
    and ($env.BULLETIN_EXTRACTOR_ASSET_ORIGIN.value == "https://www.alive.org.tw")
    and (if $ai then
      (($env.AZURE_OPENAI_ENDPOINT.value // "") | test("^https://[a-zA-Z0-9.-]+/?$"))
      and (($env.AZURE_OPENAI_DEPLOYMENT.value // "" | length) > 0)
      and (($env.AZURE_OPENAI_API_KEY.secretRef | secret_name("azure-openai-api-key")) and ($env.AZURE_OPENAI_API_KEY.value // "") == "")
      and ($env.AZURE_OPENAI_RAI_POLICY.value == "hhc-cms-translation-v1")
      else all(["AZURE_OPENAI_DEPLOYMENT","AZURE_OPENAI_API_KEY","AZURE_OPENAI_RAI_POLICY"][]; . as $key | ($env | has($key) | not)) end);
  def is_watermark_job:
    test("^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\\.App/jobs/hhc-web-bulletin-watermark$"; "i");
  # The event-driven watermark worker (PLAN-v2 W5): only the dedicated worker
  # identity, queue-triggered --once executions, the capped Job DSN, no Dapr or
  # ingress, and an exact env allow-list.
  def valid_watermark_job:
    . as $change
    | (.resourceId | sub("/providers/Microsoft.App/jobs/hhc-web-bulletin-watermark$"; ""; "i")) as $scope
    | ($scope + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-bulletin-worker-identity") as $identity
    | reference($scope; "Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-bulletin-worker-identity"; "2023-01-31"; "clientId") as $clientIdExpression
    | .after as $job
    | $job.properties.configuration as $config
    | $config.eventTriggerConfig as $event
    | ($event.scale.rules // []) as $rules
    | $job.properties.template.containers[0] as $container
    | ($container.env | map({key:.name,value:.}) | from_entries) as $env
    | def client_id: (. // "") | test("^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$") or ($mode == "preview" and . == $clientIdExpression);
    ($job.identity.type == "UserAssigned")
    and (($job.identity.userAssignedIdentities | keys | map(ascii_downcase)) == [($identity | ascii_downcase)])
    and (($job.properties.environmentId | ascii_downcase) == (($scope + "/providers/Microsoft.App/managedEnvironments/\($targets.CONTAINER_APP_ENVIRONMENT_NAME)") | ascii_downcase))
    and ($job.properties.workloadProfileName == "Consumption")
    and ($config.triggerType == "Event" and $config.replicaTimeout == 300 and $config.replicaRetryLimit == 0)
    and ($config.manualTriggerConfig == null and $config.scheduleTriggerConfig == null)
    and ($event.parallelism == 1 and $event.replicaCompletionCount == 1)
    and ($event.scale.pollingInterval == 10 and $event.scale.minExecutions == 0)
    and (($event.scale.maxExecutions | type) == "number" and $event.scale.maxExecutions >= 0 and $event.scale.maxExecutions <= 20)
    and (($rules | length) == 1)
    and ($rules[0].name == "bulletin-watermark-queue" and $rules[0].type == "azure-queue")
    and ($rules[0].metadata == {accountName:"\($targets.QUEUE_STORAGE_ACCOUNT_NAME)",queueName:"bulletin-watermark",queueLength:"1",queueLengthStrategy:"visibleonly"})
    and (($rules[0].identity // "" | ascii_downcase) == ($identity | ascii_downcase))
    and (($rules[0].auth // [] | length) == 0)
    and ($config.dapr == null and $config.ingress == null)
    and (($config.registries | length) == 1)
    and ($config.registries[0].server | matches("\($targets.ACR_LOGIN_SERVER)"; reference($scope; "Microsoft.ContainerRegistry/registries/\($targets.ACR_NAME)"; "2023-07-01"; "loginServer")))
    and (($config.registries[0].identity | ascii_downcase) == ($identity | ascii_downcase))
    and (($config.registries[0].username // "") == "" and ($config.registries[0].passwordSecretRef // "") == "")
    and (($config.secrets | length) == 1)
    and ($config.secrets[0] | (.identity | ascii_downcase) == ($identity | ascii_downcase) and (.value // "") == ""
      and (.name | secret_name("database-url"))
      and (.keyVaultUrl | matches("https://\($targets.RUNTIME_KEY_VAULT_NAME).vault.azure.net/secrets/jobs-database-url"; "[format(\u0027{0}secrets/jobs-database-url\u0027, reference(\u0027" + $scope + "/providers/Microsoft.KeyVault/vaults/\($targets.RUNTIME_KEY_VAULT_NAME)\u0027, \u00272026-02-01\u0027).vaultUri)]")))
    and (($job.properties.template.containers | length) == 1)
    and (($job.properties.template.initContainers // [] | length) == 0)
    and (($job.properties.template.volumes // [] | length) == 0)
    and ($container.name == "bulletin-watermark" and $container.command == ["/hhc-web-bulletin-worker"] and $container.args == ["--once"])
    and (($container.image // "") | test($targets.apiImagePattern))
    and ($container.resources.cpu == 2 and $container.resources.memory == "4Gi")
    and (($container.probes // [] | length) == 0)
    and (($container.env | length) == ($env | length))
    and all($container.env[]; .name == "DATABASE_URL" or .secretRef == null)
    and (($env | keys) == (["ENVIRONMENT","RELEASE","DATABASE_URL","DB_MAX_OPEN_CONNS","DB_MAX_IDLE_CONNS","DB_CONN_MAX_LIFETIME","ALLOW_DEV_CALLER_HEADER","CMS_TRANSLATION_ENABLED","BULLETIN_WATERMARK_PRIVACY_VERSION","BULLETIN_WATERMARK_QUEUE_URL","BULLETIN_WATERMARK_POISON_QUEUE_URL","BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED","AZURE_CLIENT_ID","BULLETIN_WORKER_ASSET_AUTH","BULLETIN_WORKER_ASSET_API_BASE_URL","BULLETIN_WORKER_ASSET_API_AUDIENCE","BULLETIN_WORKER_ASSET_CLIENT_ID"] | sort))
    and (($env.DATABASE_URL.secretRef | secret_name("database-url")) and ($env.DATABASE_URL.value // "") == "")
    and ($env.ENVIRONMENT.value == "production")
    and (($env.RELEASE.value // "") | test("^[0-9a-f]{40}$"))
    and ($env.DB_MAX_OPEN_CONNS.value == "1" and $env.DB_MAX_IDLE_CONNS.value == "1" and $env.DB_CONN_MAX_LIFETIME.value == "30m")
    and ($env.ALLOW_DEV_CALLER_HEADER.value == "false" and $env.CMS_TRANSLATION_ENABLED.value == "false")
    and (($env.BULLETIN_WATERMARK_PRIVACY_VERSION.value // "" | length) > 0)
    and ($env.BULLETIN_WATERMARK_QUEUE_URL.value == "https://\($targets.QUEUE_STORAGE_ACCOUNT_NAME).queue.core.windows.net/bulletin-watermark")
    and ($env.BULLETIN_WATERMARK_POISON_QUEUE_URL.value == "https://\($targets.QUEUE_STORAGE_ACCOUNT_NAME).queue.core.windows.net/bulletin-watermark-poison")
    and ($env.BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED.value == "false")
    and ($env.BULLETIN_WORKER_ASSET_AUTH.value == "workload")
    and ($env.AZURE_CLIENT_ID.value | client_id)
    and ($env.BULLETIN_WORKER_ASSET_CLIENT_ID.value == $env.AZURE_CLIENT_ID.value)
    and (($env.BULLETIN_WORKER_ASSET_API_BASE_URL.value // "") | test("^https://asset-api\\.internal\\.[a-z0-9.-]+\\.azurecontainerapps\\.io$") or ($mode == "preview" and . == "[format(\u0027https://{0}\u0027, reference(\u0027" + $scope + "/providers/Microsoft.App/containerApps/asset-api\u0027, \u00272025-01-01\u0027).configuration.ingress.fqdn)]"))
    and (($env.BULLETIN_WORKER_ASSET_API_AUDIENCE.value // "" | length) > 0);
  def is_operations_auth:
    endswith("/Microsoft.App/containerApps/hhc-web-api/authConfigs/current");
  def is_locations_alias_disable($change; $propertyPath; $property):
    ($change.resourceId | sub("/providers/Microsoft\\.App/containerApps/hhc-web-api$"; ""; "i")) as $scope
    | ($scope + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity") as $identity
    | ("https://\($targets.RUNTIME_KEY_VAULT_NAME).vault.azure.net/secrets/cms-gateway-locations-token/" + $locations_version) as $uri
    | ("[format(\u0027{0}secrets/cms-gateway-locations-token/{1}\u0027, reference(\u0027"
      + $scope + "/providers/Microsoft.KeyVault/vaults/\($targets.RUNTIME_KEY_VAULT_NAME)\u0027, \u00272026-02-01\u0027).vaultUri, \u0027"
      + $locations_version + "\u0027)]") as $uri_expression
    | $mode == "preview" and $locations_transition == "disable"
    and ($change.resourceId | is_hhc_web_api_resource)
    and $change.changeType == "Modify"
    and ($propertyPath | length == 8)
    and $propertyPath[0] == "delta"
    and $propertyPath[2] == "children"
    and $propertyPath[4] == "children"
    and $propertyPath[6] == "children"
    and ($change | getpath($propertyPath[0:2]).path) == "properties.template.containers"
    and ($change | getpath($propertyPath[0:4]).path) == "0"
    and ($change | getpath($propertyPath[0:6]).path) == "env"
    and ($property.path | test("^[0-9]+$"))
    and $property.before.name == "CMS_GATEWAY_LOCATIONS_TOKEN"
    and $property.before.secretRef == "cms-gateway-locations-token"
    and $property.before.value == null and $property.after == null and $property.children == null
    and ($change.after.properties.template.containers | length == 1 and .[0].name == "hhc-web-api"
      and all(.[0].env[]; .name != "CMS_GATEWAY_LOCATIONS_TOKEN"))
    and ($locations_version | test("^[a-f0-9]{32}$"))
    and ([$change.after.properties.configuration.secrets[] | select(.name == "cms-gateway-locations-token")]
      | length == 1 and (.[0].keyVaultUrl | matches($uri; $uri_expression)) and .[0].value == null
      and (.[0].identity | strings | ascii_downcase) == ($identity | ascii_downcase));
  def is_retired_operations_workload_env($change; $property):
    ($change.resourceId | is_hhc_web_api_resource)
    and (($property.before.name? // "") as $name | [
      "OPERATIONS_WORKLOAD_TENANT_ID",
      "OPERATIONS_WORKLOAD_ISSUER",
      "OPERATIONS_WORKLOAD_AUDIENCE",
      "OPERATIONS_WORKLOAD_CLIENT_ID",
      "OPERATIONS_WORKLOAD_OBJECT_ID",
      "OPERATIONS_SECONDARY_WORKLOAD_CLIENT_ID",
      "OPERATIONS_SECONDARY_WORKLOAD_OBJECT_ID",
      "OPERATIONS_ALLOWED_CALLER_APP_IDS"
    ] | index($name) != null)
    and ($property.after == null)
    and ($property.children == null);
  def is_retired_operations_workload_auth_provider($change; $property):
    ($change.resourceId | is_operations_auth)
    and ($property.path? == "properties.identityProviders")
    and ($property.before.azureActiveDirectory.enabled? == true)
    and ($property.after == null)
    and ($property.children == null);
  def is_cms_http_scaler_retirement($change; $propertyPath; $property):
    $change.after.properties.template as $template
    | ($change.resourceId | is_hhc_web_api_resource)
    and $change.changeType == "Modify"
    and $propertyPath[0] == "delta"
    and (((($propertyPath | length) == 4) and $propertyPath[2] == "children"
      and ($change | getpath($propertyPath[0:2]).path) == "properties.template.scale.rules"
      and ($property.path | test("^[0-9]+$")))
      or ((($propertyPath | length) == 2)
        and ($property.path | test("^properties\\.template\\.scale\\.rules\\[[0-9]+\\]$"))))
    and $property.before == {name:"http-scaler",http:{metadata:{concurrentRequests:"10"}}}
    and $property.after == null and $property.children == null
    and ($template.containers | length) == 1
    and $template.containers[0].name == "hhc-web-api"
    and $template.containers[0].resources.cpu == 0.25
    and $template.containers[0].resources.memory == "0.5Gi"
    and $template.scale.minReplicas == 1 and $template.scale.maxReplicas == 12
    and $template.scale.pollingInterval == 30 and $template.scale.cooldownPeriod == 300
    and $template.scale.rules == [{name:"cpu-scaler",custom:{type:"cpu",metadata:{type:"Utilization",value:"50"}}}];
  def is_bulletin_queue_shape_rewrite($change; $property):
    ($change.resourceId | is_bulletin_worker)
    and ($property.path? == "azureQueue")
    and ($property.before.queueName? == "bulletin-watermark")
    and any($change.delta[]?;
      .path == "properties.template.scale.rules"
      and any(.children[]?;
        any(.children[]?; .path == "custom" and .propertyChangeType == "Create")
      )
    );

  ([.changes[] | select(.changeType == "Delete" or .changeType == "Unsupported")] | length == 0)
  and
  ([.changes[] as $change
    | $change
    | path(.. | objects | select(.propertyChangeType? == "Delete")) as $propertyPath
    | getpath($propertyPath) as $property
    | select((
        ((($propertyPath | length) == 2)
        and ($propertyPath[0] == "delta")
        and (($propertyPath[1] | type) == "number")
        and ($change.resourceId | is_hhc_web_api_resource)
        and ($property.path? == "properties.template.revisionSuffix"))
      or (($change.resourceId | is_bulletin_extractor) and ($change | valid_bulletin_extractor) and ($propertyPath | length) == 2 and $propertyPath[0] == "delta" and $property.path == "properties.configuration.scheduleTriggerConfig")
      or is_bulletin_queue_shape_rewrite($change; $property)
      or is_cms_http_scaler_retirement($change; $propertyPath; $property)
      or is_locations_alias_disable($change; $propertyPath; $property)
      or is_retired_operations_workload_env($change; $property)
      or is_retired_operations_workload_auth_provider($change; $property)
      ) | not)
  ] | length == 0)
  and
  ([.changes[]
    | select(.changeType != "Ignore" and .changeType != "NoChange")
    | select((
        (.changeType == "Modify" and (.resourceId | is_hhc_web_api_resource or endswith("/Microsoft.App/jobs/hhc-web-migrate")))
        or
        ((.changeType == "Create" or .changeType == "Modify") and (.resourceId | is_bulletin_worker))
        or
        ((.changeType == "Create" or .changeType == "Modify") and (.resourceId | is_bulletin_extractor) and valid_bulletin_extractor)
        or
        ((.changeType == "Create" or .changeType == "Modify") and (.resourceId | is_watermark_job) and valid_watermark_job)
        or
        ((.changeType == "Create" or .changeType == "Modify") and (.resourceId | endswith("/Microsoft.App/jobs/hhc-web-content-import")))
        or
        ((.changeType == "Create" or .changeType == "Modify") and (.resourceId | endswith("/Microsoft.CognitiveServices/accounts/\($targets.AZURE_OPENAI_ACCOUNT_NAME)/raiPolicies/hhc-cms-translation-v1")))
        or
        ((.changeType == "Create" or .changeType == "Modify") and (.resourceId | is_operations_auth))
      ) | not)
  ] | length == 0)
' "$file" >/dev/null
