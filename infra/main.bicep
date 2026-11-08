targetScope = 'resourceGroup'

// Both normal releases and config-only updates own these explicit env overrides.
// Preserve existing entry positions: ARM interprets index shifts as field deletions.
var runtimeOverrides = loadJsonContent('runtime-env.json').resources
var configuredEnv = toObject(items(runtimeOverrides), resource => resource.key, resource => map(
  filter(items(resource.value), entry => entry.value != null), entry => union({name: entry.key}, entry.value)
))


param location string = resourceGroup().location
param containerAppEnvironmentName string
param containerRegistryName string
param runtimeKeyVaultName string
param migrationKeyVaultName string
param legacyKeyVaultName string
param accountKeyVaultName string
param engagementKeyVaultName string
param queueStorageAccountName string
param azureOpenAIAccountName string
@minLength(1)
param migrationImage string
@minLength(1)
param runtimeImage string
@description('Exact verified watermark-worker image; required when deployWatermarkWorker is true.')
param watermarkWorkerImage string = ''
@minLength(1)
param runtimeCpu string
@minLength(1)
param runtimeMemory string
param release string = 'unknown'
param runtimeRevisionSuffix string = ''
param bulletinWatermarkPrivacyVersion string = ''
param deployRuntime bool = true
param deployRuntimeCompanions bool = true
param enableGatewayLocationsAlias bool = false
@maxLength(32)
param gatewayLocationsSecretVersion string = ''
param cmsPrivateServiceVersions object = {}
param cmsDaprEnabled bool = true
param auditPrivateHTTPS bool = false
param assetPrivateHTTPS bool = false
@description('Rollback only: the persistent Dapr worker App. Releases deploy the Event Job instead.')
param deployWatermarkWorker bool = false
@description('Deploy the event-driven watermark worker Job (hhc-web-bulletin-watermark).')
param deployWatermarkJob bool = false
@description('Concurrent watermark Job executions per polling interval. Each holds one hhc_web_jobs connection.')
@minValue(0)
@maxValue(20)
param watermarkJobMaxExecutions int = 20
param deployBulletinExtractor bool = false
param bulletinExtractionEnabled bool = false
param bulletinAutoExtractionEnabled bool = false
param bulletinLLMExtractionEnabled bool = false
param bulletinExtractorImage string = ''
param bulletinExtractorAssetAudience string = ''
@description('Attach hhc-web-bulletin-worker-identity (created by infra/bulletin-worker-identity.bicep) to the watermark worker.')
param bulletinWorkerIdentityEnabled bool = false
@description('Watermark worker Asset API authentication: dapr, or workload with the dedicated identity.')
@allowed([
  'dapr'
  'workload'
])
param bulletinWorkerAssetAuth string = 'dapr'
param deployMigrationJob bool = true
param provisionPermissions bool = true
param retainLegacyRuntimeSecret bool = false
param cmsTranslationEnabled bool = false
param azureOpenAIEndpoint string = ''
param azureOpenAIDeployment string = ''
param broadcastWritersEnabled bool = false
param studioWritersEnabled bool = false
param streamKeyEncryptionID string = 'v1'
param videoInteractionsEnabled bool = false
param videoAnalyticsEnabled bool = false
param videoMediaBaseURL string = ''

var acrPullRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '7f951dda-4ed3-4680-a7ca-43fe172d538d')
var keyVaultSecretsUserRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '4633458b-17de-408a-b874-0445c86b69e6')
var storageQueueDataContributorRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '974c5e8b-45b9-4653-ba55-5f855dd0fb88')
var watermarkQueueName = 'bulletin-watermark'
var watermarkPoisonQueueName = 'bulletin-watermark-poison'
var watermarkQueueUrl = 'https://${queueStorageAccountName}.queue.${az.environment().suffixes.storage}/${watermarkQueueName}'
var watermarkPoisonQueueUrl = 'https://${queueStorageAccountName}.queue.${az.environment().suffixes.storage}/${watermarkPoisonQueueName}'
var azureOpenAIRaiPolicyName = 'hhc-cms-translation-v1'
var translationConfigured = !empty(azureOpenAIEndpoint) && !empty(azureOpenAIDeployment)
var commonEnvironment = [
  { name: 'PORT', value: '8082' }
  { name: 'ENVIRONMENT', value: 'production' }
  { name: 'RELEASE', value: release }
  { name: 'OTEL_TRACES_SAMPLER_ARG', value: '0.1' }
  { name: 'DB_MAX_OPEN_CONNS', value: '20' }
  { name: 'DB_MAX_IDLE_CONNS', value: '10' }
  { name: 'DB_CONN_MAX_LIFETIME', value: '30m' }
  { name: 'ASSET_API_BASE_URL', value: 'http://localhost:3500/v1.0/invoke/asset-api/method' }
  { name: 'INTERNAL_CALLER_APP_ID', value: 'hhc-web-api' }
  { name: 'ADMIN_ALLOWED_CALLER_APP_ID', value: 'api-gateway' }
  { name: 'ALLOW_DEV_CALLER_HEADER', value: 'false' }
  { name: 'ENABLE_FIVE_LOCALE_BULLETIN_NOTIFICATIONS_AFTER_FLUENT_REVIEW', value: 'false' }
  { name: 'CMS_TRANSLATION_ENABLED', value: cmsTranslationEnabled ? 'true' : 'false' }
  { name: 'PUBLIC_BASE_URL', value: 'https://www.alive.org.tw/assets' }
  { name: 'OUTBOX_MAX_ATTEMPTS', value: '20' }
]
var translationEnvironment = translationConfigured ? [
  { name: 'AZURE_OPENAI_ENDPOINT', value: azureOpenAIEndpoint }
  { name: 'AZURE_OPENAI_DEPLOYMENT', value: azureOpenAIDeployment }
  { name: 'AZURE_OPENAI_API_KEY', secretRef: 'azure-openai-api-key' }
  { name: 'AZURE_OPENAI_RAI_POLICY', value: azureOpenAIRaiPolicyName }
] : []
var streamKeyEnvironment = studioWritersEnabled ? [
  { name: 'STREAM_KEY_ENCRYPTION_ID', value: streamKeyEncryptionID }
  { name: 'STREAM_KEY_ENCRYPTION_KEYS', secretRef: 'stream-key-encryption-keys' }
] : []
var videoMediaConfigured = !empty(videoMediaBaseURL)
var videoMediaEnvironment = videoMediaConfigured ? [
  { name: 'VIDEO_MEDIA_BASE_URL', value: videoMediaBaseURL }
  { name: 'VIDEO_WATERMARK_KEY_HEX', secretRef: 'video-watermark-key' }
] : []

resource environment 'Microsoft.App/managedEnvironments@2024-03-01' existing = {
  name: containerAppEnvironmentName
}

resource assetAPI 'Microsoft.App/containerApps@2025-01-01' existing = {
  name: 'asset-api'
}

resource registry 'Microsoft.ContainerRegistry/registries@2023-07-01' existing = {
  name: containerRegistryName
}

resource queueStorage 'Microsoft.Storage/storageAccounts@2023-05-01' existing = {
  name: queueStorageAccountName
}

resource queueService 'Microsoft.Storage/storageAccounts/queueServices@2023-05-01' existing = {
  parent: queueStorage
  name: 'default'
}

resource watermarkQueue 'Microsoft.Storage/storageAccounts/queueServices/queues@2023-05-01' existing = {
  parent: queueService
  name: watermarkQueueName
}

resource watermarkPoisonQueue 'Microsoft.Storage/storageAccounts/queueServices/queues@2023-05-01' existing = {
  parent: queueService
  name: watermarkPoisonQueueName
}

resource extractionQueue 'Microsoft.Storage/storageAccounts/queueServices/queues@2023-05-01' existing = {
  parent: queueService
  name: 'bulletin-extraction'
}
resource extractionPoisonQueue 'Microsoft.Storage/storageAccounts/queueServices/queues@2023-05-01' existing = {
  parent: queueService
  name: 'bulletin-extraction-poison'
}

resource runtimeVault 'Microsoft.KeyVault/vaults@2026-02-01' existing = {
  name: runtimeKeyVaultName
}

resource migrationVault 'Microsoft.KeyVault/vaults@2026-02-01' existing = {
  name: migrationKeyVaultName
}

resource legacyVault 'Microsoft.KeyVault/vaults@2026-02-01' existing = {
  name: legacyKeyVaultName
}

resource azureOpenAIAccount 'Microsoft.CognitiveServices/accounts@2024-10-01' existing = {
  name: azureOpenAIAccountName
}

resource translationRAIPolicy 'Microsoft.CognitiveServices/accounts/raiPolicies@2024-10-01' existing = if (translationConfigured) {
  parent: azureOpenAIAccount
  name: azureOpenAIRaiPolicyName
}

resource apiIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = {
  name: 'hhc-web-api-identity'
}

resource migrateIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = {
  name: 'hhc-web-migrate-identity'
}

// Provisioned out of band by infra/bulletin-worker-identity.bicep (privileged).
resource bulletinWorkerIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = if (bulletinWorkerIdentityEnabled || deployWatermarkJob) {
  name: 'hhc-web-bulletin-worker-identity'
}

var bulletinWorkerWorkloadAuth = bulletinWorkerIdentityEnabled && bulletinWorkerAssetAuth == 'workload'

resource apiAcrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (provisionPermissions) {
  name: guid(registry.id, apiIdentity.id, 'acr-pull')
  scope: registry
  properties: {
    principalId: apiIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: acrPullRole
  }
}

resource migrateAcrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (provisionPermissions) {
  name: guid(registry.id, migrateIdentity.id, 'acr-pull')
  scope: registry
  properties: {
    principalId: migrateIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: acrPullRole
  }
}

resource runtimeSecretAccess 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (provisionPermissions) {
  name: guid(runtimeVault.id, apiIdentity.id, 'key-vault-secrets-user')
  scope: runtimeVault
  properties: {
    principalId: apiIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: keyVaultSecretsUserRole
  }
}

resource watermarkQueueAccess 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (provisionPermissions) {
  name: guid(watermarkQueue.id, apiIdentity.id, 'queue-data-contributor')
  scope: watermarkQueue
  properties: {
    principalId: apiIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: storageQueueDataContributorRole
  }
}

resource watermarkPoisonQueueAccess 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (provisionPermissions) {
  name: guid(watermarkPoisonQueue.id, apiIdentity.id, 'queue-data-contributor')
  scope: watermarkPoisonQueue
  properties: {
    principalId: apiIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: storageQueueDataContributorRole
  }
}

resource migrationSecretAccess 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (provisionPermissions) {
  name: guid(migrationVault.id, migrateIdentity.id, 'key-vault-secrets-user')
  scope: migrationVault
  properties: {
    principalId: migrateIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: keyVaultSecretsUserRole
  }
}

var privateServiceCredentials = [
  { name: 'cms-account-private-token', env: 'CMS_ACCOUNT_PRIVATE_TOKEN', vault: runtimeVault.name, version: cmsPrivateServiceVersions.?account ?? '' }
  { name: 'cms-operations-private-token', env: 'CMS_OPERATIONS_PRIVATE_TOKEN', vault: runtimeVault.name, version: cmsPrivateServiceVersions.?operations ?? '' }
  { name: 'cms-engagement-private-token', env: 'CMS_ENGAGEMENT_PRIVATE_TOKEN', vault: runtimeVault.name, version: cmsPrivateServiceVersions.?engagement ?? '' }
  { name: 'cms-line-private-token', env: 'CMS_LINE_PRIVATE_TOKEN', vault: runtimeVault.name, version: cmsPrivateServiceVersions.?line ?? '' }
  { name: 'cms-gateway-private-token', env: 'CMS_GATEWAY_PRIVATE_TOKEN', vault: runtimeVault.name, version: cmsPrivateServiceVersions.?gateway ?? '' }
  { name: 'cms-gateway-live-private-token', env: 'CMS_GATEWAY_LIVE_PRIVATE_TOKEN', vault: runtimeVault.name, version: cmsPrivateServiceVersions.?gatewayLive ?? '' }
  { name: 'account-cms-private-token', env: 'ACCOUNT_CMS_PRIVATE_TOKEN', vault: accountKeyVaultName, version: cmsPrivateServiceVersions.?accountOutbound ?? '' }
  { name: 'engagement-cms-private-token', env: 'ENGAGEMENT_CMS_PRIVATE_TOKEN', vault: engagementKeyVaultName, version: cmsPrivateServiceVersions.?engagementOutbound ?? '' }
  { name: 'operations-cms-private-token', env: 'OPERATIONS_CMS_PRIVATE_TOKEN', vault: legacyKeyVaultName, version: cmsPrivateServiceVersions.?operationsOutbound ?? '' }
]
var privateServiceSecrets = [
  for credential in filter(privateServiceCredentials, credential => !empty(credential.version)): {
    name: credential.name
    #disable-next-line no-hardcoded-env-urls
    keyVaultUrl: 'https://${credential.vault}.vault.azure.net/secrets/${credential.name}/${credential.version}'
    identity: apiIdentity.id
  }
]
var privateServiceEnvironment = [
  for credential in filter(privateServiceCredentials, credential => !empty(credential.version)): {
    name: credential.env
    secretRef: credential.name
  }
]

var hhcWebApiEnvironment = concat(commonEnvironment, translationEnvironment, [
  { name: 'DATABASE_URL', secretRef: 'database-url-v2' }
  { name: 'BULLETIN_EXTRACTION_QUEUE_URL', value: 'https://${queueStorageAccountName}.queue.${az.environment().suffixes.storage}/${extractionQueue.name}' }
  { name: 'BULLETIN_EXTRACTION_POISON_QUEUE_URL', value: 'https://${queueStorageAccountName}.queue.${az.environment().suffixes.storage}/${extractionPoisonQueue.name}' }
  { name: 'AZURE_CLIENT_ID', value: apiIdentity.properties.clientId }
  { name: 'BULLETIN_WATERMARK_PRIVACY_VERSION', value: bulletinWatermarkPrivacyVersion }
  { name: 'BULLETIN_WATERMARK_QUEUE_URL', value: watermarkQueueUrl }
  { name: 'BULLETIN_WATERMARK_POISON_QUEUE_URL', value: watermarkPoisonQueueUrl }
  { name: 'AUDIT_DISPATCH_ENABLED', value: 'true' }
  { name: 'AUDIT_APP_ID', value: 'audit-log' }
  { name: 'AUDIT_TOKEN', secretRef: 'audit-token' }
], videoMediaEnvironment, [
  { name: 'BULLETIN_AUTO_EXTRACTION_ENABLED', value: bulletinAutoExtractionEnabled ? 'true' : 'false' }
], streamKeyEnvironment, [
  { name: 'BROADCAST_WRITERS_ENABLED', value: broadcastWritersEnabled ? 'true' : 'false' }
  { name: 'STUDIO_WRITERS_ENABLED', value: studioWritersEnabled ? 'true' : 'false' }
  { name: 'VIDEO_INTERACTIONS_ENABLED', value: videoInteractionsEnabled ? 'true' : 'false' }
  { name: 'VIDEO_ANALYTICS_ENABLED', value: videoAnalyticsEnabled ? 'true' : 'false' }
], enableGatewayLocationsAlias ? [
  { name: 'CMS_GATEWAY_LOCATIONS_TOKEN', secretRef: 'cms-gateway-locations-token' }
] : [], [
  { name: 'CMS_DAPR_ENABLED', value: cmsDaprEnabled ? 'true' : 'false' }
], auditPrivateHTTPS ? [
  { name: 'AUDIT_PRIVATE_HTTPS', value: 'true' }
] : [], assetPrivateHTTPS ? [
  { name: 'ASSET_PRIVATE_HTTPS', value: 'true' }
] : [], privateServiceEnvironment, [
  { name: 'BULLETIN_LLM_EXTRACTION_ENABLED', value: bulletinLLMExtractionEnabled ? 'true' : 'false' }
])

resource api 'Microsoft.App/containerApps@2025-01-01' = if (deployRuntime) {
  name: 'hhc-web-api'
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${apiIdentity.id}': {}
    }
  }
  properties: {
    managedEnvironmentId: environment.id
    workloadProfileName: 'Consumption'
    configuration: {
      activeRevisionsMode: 'Single'
      dapr: {
        enabled: cmsDaprEnabled
        appId: 'hhc-web-api'
        appPort: 8082
        appProtocol: 'http'
        logLevel: 'warn'
      }
      ingress: {
        external: false
        allowInsecure: false
        targetPort: 8082
        exposedPort: 0
        transport: 'auto'
        traffic: [
          {
            latestRevision: true
            weight: 100
          }
        ]
      }
      registries: [
        {
          server: registry.properties.loginServer
          identity: apiIdentity.id
        }
      ]
      secrets: concat([
        {
          name: 'database-url-v2'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/database-url'
          identity: apiIdentity.id
        }
      ], translationConfigured ? [
        {
          name: 'azure-openai-api-key'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/hhc-web-azure-openai-api-key'
          identity: apiIdentity.id
        }
      ] : [], [
        {
          name: 'audit-token'
          keyVaultUrl: '${legacyVault.properties.vaultUri}secrets/audit-log-production-token-hhc-web-api'
          identity: apiIdentity.id
        }
      ], videoMediaConfigured ? [
        {
          name: 'video-watermark-key'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/member-video-watermark-key-hex'
          identity: apiIdentity.id
        }
      ] : [], studioWritersEnabled ? [
        {
          name: 'stream-key-encryption-keys'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/studio-stream-key-encryption-keys'
          identity: apiIdentity.id
        }
      ] : [], retainLegacyRuntimeSecret ? [
        {
          name: 'database-url'
          keyVaultUrl: '${legacyVault.properties.vaultUri}secrets/hhc-web-database-url'
          identity: apiIdentity.id
        }
      ] : [], !empty(gatewayLocationsSecretVersion) ? [
        {
          name: 'cms-gateway-locations-token'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/cms-gateway-locations-token/${gatewayLocationsSecretVersion}'
          identity: apiIdentity.id
        }
      ] : [], privateServiceSecrets)
    }
    template: {
      revisionSuffix: empty(runtimeRevisionSuffix) ? null : runtimeRevisionSuffix
      containers: [
        {
          name: 'hhc-web-api'
          image: runtimeImage
          env: concat(
            map(
              filter(hhcWebApiEnvironment, entry => contains(runtimeOverrides['hhc-web-api'], entry.name) ? any(runtimeOverrides['hhc-web-api'])[entry.name] != null : true),
              entry => contains(runtimeOverrides['hhc-web-api'], entry.name) ? union({name: entry.name}, any(runtimeOverrides['hhc-web-api'])[entry.name]) : entry
            ),
            filter(configuredEnv['hhc-web-api'], entry => !contains(map(hhcWebApiEnvironment, original => original.name), entry.name))
          )
          resources: {
            cpu: json(runtimeCpu)
            memory: runtimeMemory
          }
          probes: [
            {
              type: 'Startup'
              httpGet: { path: '/health/live', port: 8082 }
              initialDelaySeconds: 1
              periodSeconds: 2
              timeoutSeconds: 3
              failureThreshold: 30
            }
            {
              type: 'Liveness'
              httpGet: { path: '/health/live', port: 8082 }
              initialDelaySeconds: 10
              periodSeconds: 30
              timeoutSeconds: 3
              failureThreshold: 3
            }
            {
              type: 'Readiness'
              httpGet: { path: '/health/ready', port: 8082 }
              initialDelaySeconds: 10
              periodSeconds: 10
              timeoutSeconds: 3
              failureThreshold: 3
            }
          ]
        }
      ]
      scale: {
        minReplicas: 1
        maxReplicas: 12
        rules: [
          {
            name: 'cpu-scaler'
            custom: {
              type: 'cpu'
              metadata: {
                type: 'Utilization'
                value: '50'
              }
            }
          }
        ]
        cooldownPeriod: 300
        pollingInterval: 30
      }
    }
  }
  dependsOn: [
    apiAcrPull
    runtimeSecretAccess
    translationRAIPolicy
  ]
}

resource operationsWorkloadAuth 'Microsoft.App/containerApps/authConfigs@2025-01-01' = if (deployRuntime && deployRuntimeCompanions) {
  parent: api
  name: 'current'
  properties: {
    platform: { enabled: false }
    httpSettings: { requireHttps: true }
    globalValidation: { unauthenticatedClientAction: 'AllowAnonymous' }
    identityProviders: {}
  }
}

// Rollback path only (scripts/rollback-watermark-job.sh): releases deploy the
// Event Job below with deployWatermarkWorker=false and delete this App after the
// Job preflight passes. Remove this App and the Dapr asset-auth branch once the
// Job has been stable in production for 2 weeks (PLAN-v2 W5).
var hhcWebBulletinWorkerEnvironment = [
  { name: 'ENVIRONMENT', value: 'production' }
  { name: 'RELEASE', value: release }
  { name: 'DATABASE_URL', secretRef: 'database-url' }
  { name: 'DB_MAX_OPEN_CONNS', value: '2' }
  { name: 'DB_MAX_IDLE_CONNS', value: '1' }
  { name: 'DB_CONN_MAX_LIFETIME', value: '30m' }
  { name: 'ASSET_API_BASE_URL', value: 'http://localhost:3500/v1.0/invoke/asset-api/method' }
  { name: 'ACCOUNT_API_BASE_URL', value: 'http://localhost:3500/v1.0/invoke/account-api/method' }
  { name: 'INTERNAL_CALLER_APP_ID', value: 'hhc-web-api' }
  { name: 'ALLOW_DEV_CALLER_HEADER', value: 'false' }
  { name: 'CMS_TRANSLATION_ENABLED', value: 'false' }
  { name: 'PUBLIC_BASE_URL', value: 'https://www.alive.org.tw/assets' }
  { name: 'BULLETIN_WATERMARK_PRIVACY_VERSION', value: bulletinWatermarkPrivacyVersion }
  { name: 'BULLETIN_WATERMARK_QUEUE_URL', value: watermarkQueueUrl }
  { name: 'BULLETIN_WATERMARK_POISON_QUEUE_URL', value: watermarkPoisonQueueUrl }
  { name: 'BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED', value: 'true' }
  { name: 'AZURE_CLIENT_ID', value: apiIdentity.properties.clientId }
  { name: 'BULLETIN_WORKER_ASSET_AUTH', value: bulletinWorkerWorkloadAuth ? 'workload' : 'dapr' }
  { name: 'BULLETIN_WORKER_ASSET_API_BASE_URL', value: bulletinWorkerWorkloadAuth ? 'https://${assetAPI.properties.configuration.ingress.fqdn}' : '' }
  { name: 'BULLETIN_WORKER_ASSET_API_AUDIENCE', value: bulletinWorkerWorkloadAuth ? bulletinExtractorAssetAudience : '' }
  { name: 'BULLETIN_WORKER_ASSET_CLIENT_ID', value: bulletinWorkerWorkloadAuth ? bulletinWorkerIdentity!.properties.clientId : '' }
]

resource watermarkWorker 'Microsoft.App/containerApps@2025-01-01' = if (deployWatermarkWorker) {
  name: 'hhc-web-bulletin-worker'
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: union({ '${apiIdentity.id}': {} }, bulletinWorkerIdentityEnabled ? { '${bulletinWorkerIdentity.id}': {} } : {})
  }
  properties: {
    managedEnvironmentId: environment.id
    workloadProfileName: 'Consumption'
    configuration: {
      activeRevisionsMode: 'Single'
      maxInactiveRevisions: 100
      dapr: {
        enabled: true
        appId: 'hhc-web-bulletin-worker'
        logLevel: 'warn'
      }
      registries: [
        {
          server: registry.properties.loginServer
          identity: apiIdentity.id
        }
      ]
      secrets: [
        {
          name: 'database-url'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/database-url'
          identity: apiIdentity.id
        }
      ]
    }
    template: {
      containers: [
        {
          name: 'hhc-web-bulletin-worker'
          image: watermarkWorkerImage
          command: ['/hhc-web-bulletin-worker']
          env: concat(
            map(
              filter(hhcWebBulletinWorkerEnvironment, entry => contains(runtimeOverrides['hhc-web-bulletin-worker'], entry.name) ? any(runtimeOverrides['hhc-web-bulletin-worker'])[entry.name] != null : true),
              entry => contains(runtimeOverrides['hhc-web-bulletin-worker'], entry.name) ? union({name: entry.name}, any(runtimeOverrides['hhc-web-bulletin-worker'])[entry.name]) : entry
            ),
            filter(configuredEnv['hhc-web-bulletin-worker'], entry => !contains(map(hhcWebBulletinWorkerEnvironment, original => original.name), entry.name))
          )
          resources: {
            cpu: json('2')
            memory: '4Gi'
          }
        }
      ]
      scale: {
        pollingInterval: 10
        cooldownPeriod: 300
        minReplicas: 0
        maxReplicas: 1
        rules: [
          {
            name: 'bulletin-watermark-queue'
            custom: {
              type: 'azure-queue'
              metadata: {
                accountName: queueStorageAccountName
                queueName: watermarkQueue.name
                queueLength: '1'
              }
              identity: apiIdentity.id
            }
          }
        ]
      }
    }
  }
  dependsOn: [
    apiAcrPull
    runtimeSecretAccess
    watermarkQueueAccess
    watermarkPoisonQueueAccess
  ]
}

// Event-driven watermark worker (PLAN-v2 W5): KEDA starts one --once execution
// per visible queue message. No Dapr or ingress; Asset API is called with the
// dedicated identity's Entra token, the queue and ACR with the same identity,
// and PostgreSQL with the capped hhc_web_jobs login (one connection per
// execution).
var hhcWebBulletinWatermarkEnvironment = [
  { name: 'ENVIRONMENT', value: 'production' }
  { name: 'RELEASE', value: release }
  { name: 'DATABASE_URL', secretRef: 'database-url' }
  { name: 'DB_MAX_OPEN_CONNS', value: '1' }
  { name: 'DB_MAX_IDLE_CONNS', value: '1' }
  { name: 'DB_CONN_MAX_LIFETIME', value: '30m' }
  { name: 'ALLOW_DEV_CALLER_HEADER', value: 'false' }
  { name: 'CMS_TRANSLATION_ENABLED', value: 'false' }
  { name: 'BULLETIN_WATERMARK_PRIVACY_VERSION', value: bulletinWatermarkPrivacyVersion }
  { name: 'BULLETIN_WATERMARK_QUEUE_URL', value: watermarkQueueUrl }
  { name: 'BULLETIN_WATERMARK_POISON_QUEUE_URL', value: watermarkPoisonQueueUrl }
  { name: 'BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED', value: 'false' }
  { name: 'AZURE_CLIENT_ID', value: bulletinWorkerIdentity!.properties.clientId }
  { name: 'BULLETIN_WORKER_ASSET_AUTH', value: 'workload' }
  { name: 'BULLETIN_WORKER_ASSET_API_BASE_URL', value: 'https://${assetAPI.properties.configuration.ingress.fqdn}' }
  { name: 'BULLETIN_WORKER_ASSET_API_AUDIENCE', value: bulletinExtractorAssetAudience }
  { name: 'BULLETIN_WORKER_ASSET_CLIENT_ID', value: bulletinWorkerIdentity!.properties.clientId }
]

resource watermarkJob 'Microsoft.App/jobs@2025-01-01' = if (deployWatermarkJob) {
  name: 'hhc-web-bulletin-watermark'
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${bulletinWorkerIdentity.id}': {}
    }
  }
  properties: {
    environmentId: environment.id
    workloadProfileName: 'Consumption'
    configuration: {
      triggerType: 'Event'
      replicaTimeout: 300
      replicaRetryLimit: 0
      eventTriggerConfig: {
        parallelism: 1
        replicaCompletionCount: 1
        scale: {
          pollingInterval: 10
          minExecutions: 0
          maxExecutions: watermarkJobMaxExecutions
          rules: [
            {
              name: 'bulletin-watermark-queue'
              type: 'azure-queue'
              metadata: {
                accountName: queueStorageAccountName
                queueName: watermarkQueue.name
                queueLength: '1'
                queueLengthStrategy: 'visibleonly'
              }
              identity: bulletinWorkerIdentity.id
            }
          ]
        }
      }
      registries: [
        { server: registry.properties.loginServer, identity: bulletinWorkerIdentity.id }
      ]
      secrets: [
        {
          name: 'database-url'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/jobs-database-url'
          identity: bulletinWorkerIdentity.id
        }
      ]
    }
    template: {
      containers: [
        {
          name: 'bulletin-watermark'
          image: watermarkWorkerImage
          command: ['/hhc-web-bulletin-worker']
          args: ['--once']
          // The Dapr-only ASSET_API_BASE_URL/ACCOUNT_API_BASE_URL/INTERNAL_CALLER_APP_ID/
          // PUBLIC_BASE_URL of the App are intentionally absent: config.LoadWorker does
          // not require them and workload Asset auth does not read them.
          env: concat(
            map(
              filter(hhcWebBulletinWatermarkEnvironment, entry => contains(runtimeOverrides['hhc-web-bulletin-watermark'], entry.name) ? any(runtimeOverrides['hhc-web-bulletin-watermark'])[entry.name] != null : true),
              entry => contains(runtimeOverrides['hhc-web-bulletin-watermark'], entry.name) ? union({name: entry.name}, any(runtimeOverrides['hhc-web-bulletin-watermark'])[entry.name]) : entry
            ),
            filter(configuredEnv['hhc-web-bulletin-watermark'], entry => !contains(map(hhcWebBulletinWatermarkEnvironment, original => original.name), entry.name))
          )
          resources: {
            cpu: json('2')
            memory: '4Gi'
          }
        }
      ]
    }
  }
}

var hhcWebMigrateEnvironment = [
  { name: 'DATABASE_URL', secretRef: 'database-url' }
]

resource migrate 'Microsoft.App/jobs@2024-03-01' = if (deployMigrationJob) {
  name: 'hhc-web-migrate'
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${migrateIdentity.id}': {}
    }
  }
  properties: {
    environmentId: environment.id
    workloadProfileName: 'Consumption'
    configuration: {
      triggerType: 'Manual'
      replicaTimeout: 300
      replicaRetryLimit: 0
      manualTriggerConfig: {
        parallelism: 1
        replicaCompletionCount: 1
      }
      registries: [
        {
          server: registry.properties.loginServer
          identity: migrateIdentity.id
        }
      ]
      secrets: [
        {
          name: 'database-url'
          keyVaultUrl: '${migrationVault.properties.vaultUri}secrets/database-url'
          identity: migrateIdentity.id
        }
      ]
    }
    template: {
      containers: [
        {
          name: 'hhc-web-migrate'
          image: migrationImage
          command: ['/hhc-web-migrate']
          env: concat(
            map(
              filter(hhcWebMigrateEnvironment, entry => contains(runtimeOverrides['hhc-web-migrate'], entry.name) ? any(runtimeOverrides['hhc-web-migrate'])[entry.name] != null : true),
              entry => contains(runtimeOverrides['hhc-web-migrate'], entry.name) ? union({name: entry.name}, any(runtimeOverrides['hhc-web-migrate'])[entry.name]) : entry
            ),
            filter(configuredEnv['hhc-web-migrate'], entry => !contains(map(hhcWebMigrateEnvironment, original => original.name), entry.name))
          )
          resources: {
            cpu: json('0.25')
            memory: '0.5Gi'
          }
        }
      ]
    }
  }
  dependsOn: [
    migrateAcrPull
    migrationSecretAccess
  ]
}

var hhcWebContentImportEnvironment = [
  { name: 'DATABASE_URL', secretRef: 'database-url' }
]

resource contentImport 'Microsoft.App/jobs@2024-03-01' = if (deployRuntimeCompanions) {
  name: 'hhc-web-content-import'
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${apiIdentity.id}': {}
    }
  }
  properties: {
    environmentId: environment.id
    workloadProfileName: 'Consumption'
    configuration: {
      triggerType: 'Manual'
      replicaTimeout: 900
      replicaRetryLimit: 0
      manualTriggerConfig: {
        parallelism: 1
        replicaCompletionCount: 1
      }
      registries: [
        {
          server: registry.properties.loginServer
          identity: apiIdentity.id
        }
      ]
      secrets: [
        {
          name: 'database-url'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/jobs-database-url'
          identity: apiIdentity.id
        }
      ]
    }
    template: {
      containers: [
        {
          name: 'content-import'
          image: runtimeImage
          command: ['/hhc-web-content-import']
          args: ['--mode=inventory']
          env: concat(
            map(
              filter(hhcWebContentImportEnvironment, entry => contains(runtimeOverrides['hhc-web-content-import'], entry.name) ? any(runtimeOverrides['hhc-web-content-import'])[entry.name] != null : true),
              entry => contains(runtimeOverrides['hhc-web-content-import'], entry.name) ? union({name: entry.name}, any(runtimeOverrides['hhc-web-content-import'])[entry.name]) : entry
            ),
            filter(configuredEnv['hhc-web-content-import'], entry => !contains(map(hhcWebContentImportEnvironment, original => original.name), entry.name))
          )
          resources: {
            cpu: json('0.25')
            memory: '0.5Gi'
          }
        }
      ]
    }
  }
  dependsOn: [
    apiAcrPull
    runtimeSecretAccess
  ]
}

// No HTTP ingress or Dapr sidecar: this process claims a bounded batch and exits.
var hhcWebBulletinExtractorEnvironment = concat([
  { name: 'ENVIRONMENT', value: 'production' }
  { name: 'RELEASE', value: release }
  { name: 'DATABASE_URL', secretRef: 'database-url' }
  { name: 'BULLETIN_EXTRACTION_QUEUE_URL', value: 'https://${queueStorageAccountName}.queue.${az.environment().suffixes.storage}/${extractionQueue.name}' }
  { name: 'BULLETIN_EXTRACTION_POISON_QUEUE_URL', value: 'https://${queueStorageAccountName}.queue.${az.environment().suffixes.storage}/${extractionPoisonQueue.name}' }
  { name: 'AZURE_CLIENT_ID', value: apiIdentity.properties.clientId }
  { name: 'BULLETIN_EXTRACTION_ENABLED', value: bulletinExtractionEnabled ? 'true' : 'false' }
  { name: 'BULLETIN_EXTRACTION_TIMEOUT', value: '120s' }
  { name: 'BULLETIN_EXTRACTION_BATCH_SIZE', value: '1' }
  { name: 'BULLETIN_EXTRACTION_MAX_ATTEMPTS', value: '3' }
  { name: 'ASSET_API_BASE_URL', value: 'https://${assetAPI.properties.configuration.ingress.fqdn}' }
  { name: 'ASSET_API_AUDIENCE', value: bulletinExtractorAssetAudience }
  { name: 'BULLETIN_EXTRACTOR_ASSET_ORIGIN', value: 'https://www.alive.org.tw' }
], translationEnvironment, [
  { name: 'BULLETIN_LLM_EXTRACTION_ENABLED', value: bulletinLLMExtractionEnabled ? 'true' : 'false' }
])

resource bulletinExtractor 'Microsoft.App/jobs@2025-01-01' = if (deployBulletinExtractor) {
  name: 'hhc-web-bulletin-extractor'
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${apiIdentity.id}': {}
    }
  }
  properties: {
    environmentId: environment.id
    workloadProfileName: 'Consumption'
    configuration: {
      triggerType: 'Event'
      replicaTimeout: bulletinLLMExtractionEnabled ? 720 : 150
      replicaRetryLimit: 0
      eventTriggerConfig: {
        parallelism: 1
        replicaCompletionCount: 1
        scale: {
          pollingInterval: 10
          minExecutions: 0
          maxExecutions: 1
          rules: [
            {
              name: 'bulletin-extraction-queue'
              type: 'azure-queue'
              identity: apiIdentity.id
              metadata: {
                accountName: queueStorageAccountName
                queueName: extractionQueue.name
                queueLength: '1'
                queueLengthStrategy: 'visibleonly'
              }
            }
          ]
        }
      }
      registries: [
        { server: registry.properties.loginServer, identity: apiIdentity.id }
      ]
      secrets: concat([
        {
          name: 'database-url'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/jobs-database-url'
          identity: apiIdentity.id
        }
      ], translationConfigured ? [
        {
          name: 'azure-openai-api-key'
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/hhc-web-azure-openai-api-key'
          identity: apiIdentity.id
        }
      ] : [])
    }
    template: {
      containers: [
        {
          name: 'bulletin-extractor'
          image: bulletinExtractorImage
          command: ['/bulletin-extractor']
          env: concat(
            map(
              filter(hhcWebBulletinExtractorEnvironment, entry => contains(runtimeOverrides['hhc-web-bulletin-extractor'], entry.name) ? any(runtimeOverrides['hhc-web-bulletin-extractor'])[entry.name] != null : true),
              entry => contains(runtimeOverrides['hhc-web-bulletin-extractor'], entry.name) ? union({name: entry.name}, any(runtimeOverrides['hhc-web-bulletin-extractor'])[entry.name]) : entry
            ),
            filter(configuredEnv['hhc-web-bulletin-extractor'], entry => !contains(map(hhcWebBulletinExtractorEnvironment, original => original.name), entry.name))
          )
          resources: {
            cpu: json('1')
            memory: '2Gi'
          }
        }
      ]
    }
  }
  dependsOn: [
    apiAcrPull
    runtimeSecretAccess
    translationRAIPolicy
  ]
}

output apiName string = 'hhc-web-api'
output migrationJobName string = 'hhc-web-migrate'
output watermarkWorkerName string = 'hhc-web-bulletin-worker'
output watermarkJobName string = 'hhc-web-bulletin-watermark'
output bulletinExtractorJobName string = 'hhc-web-bulletin-extractor'
