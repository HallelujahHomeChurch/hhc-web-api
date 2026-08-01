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
param deployRuntime bool = true
param deployMigrationJob bool = true
param provisionPermissions bool = true
param retainLegacyRuntimeSecret bool = false

var acrPullRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '7f951dda-4ed3-4680-a7ca-43fe172d538d')
var keyVaultSecretsUserRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '4633458b-17de-408a-b874-0445c86b69e6')
var commonEnvironment = [
  { name: 'PORT', value: '8082' }
  { name: 'ENVIRONMENT', value: 'production' }
  { name: 'DB_MAX_OPEN_CONNS', value: '10' }
  { name: 'DB_MAX_IDLE_CONNS', value: '5' }
  { name: 'DB_CONN_MAX_LIFETIME', value: '30m' }
  { name: 'ASSET_API_BASE_URL', value: 'http://localhost:3500/v1.0/invoke/asset-api/method' }
  { name: 'INTERNAL_CALLER_APP_ID', value: 'hhc-web-api' }
  { name: 'ADMIN_ALLOWED_CALLER_APP_ID', value: 'api-gateway' }
  { name: 'ALLOW_DEV_CALLER_HEADER', value: 'false' }
  { name: 'PUBLIC_BASE_URL', value: 'https://www.alive.org.tw/assets' }
  { name: 'OUTBOX_MAX_ATTEMPTS', value: '20' }
]

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
      ], retainLegacyRuntimeSecret ? [
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
          env: concat(commonEnvironment, [
            { name: 'DATABASE_URL', secretRef: 'database-url-v2' }
          ])
          resources: {
            cpu: json('0.5')
            memory: '1Gi'
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
  ]
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

output apiName string = 'hhc-web-api'
output migrationJobName string = 'hhc-web-migrate'
