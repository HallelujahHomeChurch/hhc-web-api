targetScope = 'resourceGroup'

param location string = resourceGroup().location
param containerAppEnvironmentName string = 'alive-env'
param containerRegistryName string = 'alive'
param keyVaultName string = 'alive-vault'
@minLength(1)
param migrationImage string
@minLength(1)
param runtimeImage string
param deployRuntime bool = true
param provisionPermissions bool = true

var acrPullRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '7f951dda-4ed3-4680-a7ca-43fe172d538d')
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
  { name: 'PUBLIC_BASE_URL', value: 'https://www.alive.org.tw/api' }
  { name: 'OUTBOX_MAX_ATTEMPTS', value: '20' }
]

resource environment 'Microsoft.App/managedEnvironments@2024-03-01' existing = {
  name: containerAppEnvironmentName
}

resource registry 'Microsoft.ContainerRegistry/registries@2023-07-01' existing = {
  name: containerRegistryName
}

resource vault 'Microsoft.KeyVault/vaults@2023-07-01' existing = {
  name: keyVaultName
}

resource identity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: 'hhc-web-api-identity'
  location: location
}

resource acrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = if (provisionPermissions) {
  name: guid(registry.id, identity.id, 'acr-pull')
  scope: registry
  properties: {
    principalId: identity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: acrPullRole
  }
}

resource secretAccess 'Microsoft.KeyVault/vaults/accessPolicies@2023-07-01' = if (provisionPermissions) {
  parent: vault
  name: 'add'
  properties: {
    accessPolicies: [
      {
        tenantId: subscription().tenantId
        objectId: identity.properties.principalId
        permissions: {
          secrets: ['get']
        }
      }
    ]
  }
}

resource api 'Microsoft.App/containerApps@2025-01-01' = if (deployRuntime) {
  name: 'hhc-web-api'
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${identity.id}': {}
    }
  }
  properties: {
    managedEnvironmentId: environment.id
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
          identity: identity.id
        }
      ]
      secrets: [
        {
          name: 'database-url'
          keyVaultUrl: '${vault.properties.vaultUri}secrets/hhc-web-database-url'
          identity: identity.id
        }
      ]
    }
    template: {
      containers: [
        {
          name: 'hhc-web-api'
          image: runtimeImage
          env: concat(commonEnvironment, [
            { name: 'DATABASE_URL', secretRef: 'database-url' }
          ])
          resources: {
            cpu: json('0.5')
            memory: '1Gi'
          }
          probes: [
            {
              type: 'Liveness'
              httpGet: { path: '/health', port: 8082 }
              initialDelaySeconds: 10
              periodSeconds: 30
            }
            {
              type: 'Readiness'
              httpGet: { path: '/ready', port: 8082 }
              initialDelaySeconds: 10
              periodSeconds: 10
            }
          ]
        }
      ]
      scale: {
        minReplicas: 1
        maxReplicas: 3
      }
    }
  }
  dependsOn: [
    acrPull
    secretAccess
  ]
}

resource migrate 'Microsoft.App/jobs@2024-03-01' = {
  name: 'hhc-web-migrate'
  location: location
  identity: {
    type: 'UserAssigned'
    userAssignedIdentities: {
      '${identity.id}': {}
    }
  }
  properties: {
    environmentId: environment.id
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
          identity: identity.id
        }
      ]
      secrets: [
        {
          name: 'database-url'
          keyVaultUrl: '${vault.properties.vaultUri}secrets/hhc-web-database-url'
          identity: identity.id
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
    acrPull
    secretAccess
  ]
}

output apiName string = 'hhc-web-api'
output migrationJobName string = migrate.name
