targetScope = 'resourceGroup'

param location string = resourceGroup().location
param containerAppEnvironmentName string = 'alive-env'
param containerRegistryName string = 'alive'
param runtimeKeyVaultName string = 'alive-hhw-runtime-kv'
param migrationKeyVaultName string = 'alive-hhw-migrate-kv'
param legacyKeyVaultName string = 'alive-vault'
@minLength(1)
param migrationImage string
@minLength(1)
param runtimeImage string
@minLength(1)
param runtimeCpu string
@minLength(1)
param runtimeMemory string
param release string = 'unknown'
param deployRuntime bool = true
param deployMigrationJob bool = true
param provisionPermissions bool = true
param retainLegacyRuntimeSecret bool = false
param cmsTranslationEnabled bool = false
param azureOpenAIEndpoint string = ''
param azureOpenAIDeployment string = ''
param operationsWorkloadAudience string = ''
param operationsWorkloadClientId string = ''
param operationsWorkloadObjectId string = ''

var acrPullRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '7f951dda-4ed3-4680-a7ca-43fe172d538d')
var keyVaultSecretsUserRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '4633458b-17de-408a-b874-0445c86b69e6')
var azureOpenAIAccountName = 'bible-text-embedding-resource'
var azureOpenAIRaiPolicyName = 'hhc-cms-translation-v1'
var translationConfigured = !empty(azureOpenAIEndpoint) && !empty(azureOpenAIDeployment)
var operationsWorkloadConfigured = !empty(operationsWorkloadAudience) && !empty(operationsWorkloadClientId) && !empty(operationsWorkloadObjectId)
var commonEnvironment = [
  { name: 'PORT', value: '8082' }
  { name: 'ENVIRONMENT', value: 'production' }
  { name: 'RELEASE', value: release }
  { name: 'OTEL_TRACES_SAMPLER_ARG', value: '0.1' }
  { name: 'DB_MAX_OPEN_CONNS', value: '10' }
  { name: 'DB_MAX_IDLE_CONNS', value: '5' }
  { name: 'DB_CONN_MAX_LIFETIME', value: '30m' }
  { name: 'ASSET_API_BASE_URL', value: 'http://localhost:3500/v1.0/invoke/asset-api/method' }
  { name: 'INTERNAL_CALLER_APP_ID', value: 'hhc-web-api' }
  { name: 'ADMIN_ALLOWED_CALLER_APP_ID', value: 'api-gateway' }
  { name: 'OPERATIONS_ALLOWED_CALLER_APP_IDS', value: 'asset-api,hhc-line-function-bot' }
  { name: 'OPERATIONS_WORKLOAD_TENANT_ID', value: operationsWorkloadConfigured ? subscription().tenantId : '' }
  { name: 'OPERATIONS_WORKLOAD_ISSUER', value: operationsWorkloadConfigured ? 'https://sts.windows.net/${subscription().tenantId}/' : '' }
  { name: 'OPERATIONS_WORKLOAD_AUDIENCE', value: operationsWorkloadConfigured ? operationsWorkloadAudience : '' }
  { name: 'OPERATIONS_WORKLOAD_CLIENT_ID', value: operationsWorkloadConfigured ? operationsWorkloadClientId : '' }
  { name: 'OPERATIONS_WORKLOAD_OBJECT_ID', value: operationsWorkloadConfigured ? operationsWorkloadObjectId : '' }
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

resource environment 'Microsoft.App/managedEnvironments@2024-03-01' existing = {
  name: containerAppEnvironmentName
}

resource registry 'Microsoft.ContainerRegistry/registries@2023-07-01' existing = {
  name: containerRegistryName
}

resource runtimeVault 'Microsoft.KeyVault/vaults@2023-07-01' = {
  name: runtimeKeyVaultName
  location: location
  properties: {
    tenantId: subscription().tenantId
    sku: {
      family: 'A'
      name: 'standard'
    }
    accessPolicies: []
    enablePurgeProtection: true
    enableRbacAuthorization: true
    enableSoftDelete: true
    softDeleteRetentionInDays: 90
    publicNetworkAccess: 'Enabled'
  }
}

resource migrationVault 'Microsoft.KeyVault/vaults@2023-07-01' = {
  name: migrationKeyVaultName
  location: location
  properties: {
    tenantId: subscription().tenantId
    sku: {
      family: 'A'
      name: 'standard'
    }
    accessPolicies: []
    enablePurgeProtection: true
    enableRbacAuthorization: true
    enableSoftDelete: true
    softDeleteRetentionInDays: 90
    publicNetworkAccess: 'Enabled'
  }
}

resource legacyVault 'Microsoft.KeyVault/vaults@2023-07-01' existing = {
  name: legacyKeyVaultName
}

resource azureOpenAIAccount 'Microsoft.CognitiveServices/accounts@2024-10-01' existing = {
  name: azureOpenAIAccountName
}

resource translationRAIPolicy 'Microsoft.CognitiveServices/accounts/raiPolicies@2024-10-01' = if (translationConfigured) {
  parent: azureOpenAIAccount
  name: azureOpenAIRaiPolicyName
  properties: {
    basePolicyName: 'Microsoft.DefaultV2'
    mode: 'Blocking'
    contentFilters: any([
      { name: 'Hate', source: 'Prompt', action: 'NONE', enabled: true, blocking: true, severityThreshold: 'High' }
      { name: 'Hate', source: 'Completion', action: 'NONE', enabled: true, blocking: true, severityThreshold: 'High' }
      { name: 'Sexual', source: 'Prompt', action: 'NONE', enabled: true, blocking: true, severityThreshold: 'High' }
      { name: 'Sexual', source: 'Completion', action: 'NONE', enabled: true, blocking: true, severityThreshold: 'High' }
      { name: 'Violence', source: 'Prompt', action: 'NONE', enabled: true, blocking: true, severityThreshold: 'High' }
      { name: 'Violence', source: 'Completion', action: 'NONE', enabled: true, blocking: true, severityThreshold: 'High' }
      { name: 'Selfharm', source: 'Prompt', action: 'NONE', enabled: true, blocking: true, severityThreshold: 'High' }
      { name: 'Selfharm', source: 'Completion', action: 'NONE', enabled: true, blocking: true, severityThreshold: 'High' }
      { name: 'Jailbreak', source: 'Prompt', action: 'NONE', enabled: true, blocking: false }
    ])
  }
}

resource apiIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: 'hhc-web-api-identity'
  location: location
}

resource migrateIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: 'hhc-web-migrate-identity'
  location: location
}

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

resource migrationSecretAccess 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (provisionPermissions) {
  name: guid(migrationVault.id, migrateIdentity.id, 'key-vault-secrets-user')
  scope: migrationVault
  properties: {
    principalId: migrateIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: keyVaultSecretsUserRole
  }
}

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
        enabled: true
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
      ] : [], retainLegacyRuntimeSecret ? [
        {
          name: 'database-url'
          keyVaultUrl: '${legacyVault.properties.vaultUri}secrets/hhc-web-database-url'
          identity: apiIdentity.id
        }
      ] : [])
    }
    template: {
      containers: [
        {
          name: 'hhc-web-api'
          image: runtimeImage
          env: concat(commonEnvironment, translationEnvironment, [
            { name: 'DATABASE_URL', secretRef: 'database-url-v2' }
          ])
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
        maxReplicas: 3
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

resource operationsWorkloadAuth 'Microsoft.App/containerApps/authConfigs@2025-01-01' = if (deployRuntime && operationsWorkloadConfigured) {
  parent: api
  name: 'current'
  properties: {
    platform: { enabled: true }
    httpSettings: { requireHttps: true }
    globalValidation: { unauthenticatedClientAction: 'AllowAnonymous' }
    identityProviders: {
      azureActiveDirectory: {
        enabled: true
        isAutoProvisioned: false
        registration: {
          clientId: replace(operationsWorkloadAudience, 'api://', '')
          openIdIssuer: 'https://sts.windows.net/${subscription().tenantId}/'
        }
        validation: {
          allowedAudiences: [operationsWorkloadAudience]
          defaultAuthorizationPolicy: {
            allowedApplications: [operationsWorkloadClientId]
            allowedPrincipals: { identities: [operationsWorkloadObjectId] }
          }
        }
      }
    }
  }
}

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
          env: [
            { name: 'DATABASE_URL', secretRef: 'database-url' }
          ]
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

resource contentImport 'Microsoft.App/jobs@2024-03-01' = {
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
          keyVaultUrl: '${runtimeVault.properties.vaultUri}secrets/database-url'
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
          env: [
            { name: 'DATABASE_URL', secretRef: 'database-url' }
          ]
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

output apiName string = 'hhc-web-api'
output migrationJobName string = 'hhc-web-migrate'
