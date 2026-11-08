// Dedicated identity for the bulletin watermark worker (PLAN-v2 W3).
//
// Privileged bootstrap, run by the platform operator from merged main after a
// reviewed what-if (release identities only hold Contributor and cannot write
// role assignments). main.bicep references the identity as `existing`, so
// releases never recreate or broaden it. See infra/README.md.
//
// Exact grants (least privilege, each scoped to one resource):
//   - Storage Queue Data Contributor  on queue bulletin-watermark
//       dequeue, delete (ack) and update visibility (W1 retry backoff); KEDA
//       queue-length polling.
//   - Storage Queue Data Message Sender on queue bulletin-watermark-poison
//       quarantine of exhausted or malformed messages only.
//   - Key Vault Secrets User          on the configured runtime vault's jobs-database-url
//       the Job-login DSN shared with the extractor (login cap), nothing else.
//   - AcrPull                         on the configured registry
// The Asset API `Asset.Invoke` app role is assigned separately by the
// asset-api release (`approve_watermark_app_role`).

param location string = resourceGroup().location
param containerRegistryName string
param runtimeKeyVaultName string
param queueStorageAccountName string

var acrPullRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '7f951dda-4ed3-4680-a7ca-43fe172d538d')
var keyVaultSecretsUserRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '4633458b-17de-408a-b874-0445c86b69e6')
var storageQueueDataContributorRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '974c5e8b-45b9-4653-ba55-5f855dd0fb88')
var storageQueueDataMessageSenderRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', 'c6a89b2d-59bc-44d0-9896-0f6e12d7b80a')

resource workerIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' = {
  name: 'hhc-web-bulletin-worker-identity'
  location: location
}

resource registry 'Microsoft.ContainerRegistry/registries@2023-07-01' existing = {
  name: containerRegistryName
}

resource runtimeVault 'Microsoft.KeyVault/vaults@2026-02-01' existing = {
  name: runtimeKeyVaultName
}

resource jobsDatabaseSecret 'Microsoft.KeyVault/vaults/secrets@2026-02-01' existing = {
  parent: runtimeVault
  name: 'jobs-database-url'
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
  name: 'bulletin-watermark'
}

resource watermarkPoisonQueue 'Microsoft.Storage/storageAccounts/queueServices/queues@2023-05-01' existing = {
  parent: queueService
  name: 'bulletin-watermark-poison'
}

resource queueAccess 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(watermarkQueue.id, workerIdentity.id, 'queue-data-contributor')
  scope: watermarkQueue
  properties: {
    principalId: workerIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: storageQueueDataContributorRole
  }
}

resource poisonQueueAccess 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(watermarkPoisonQueue.id, workerIdentity.id, 'queue-data-message-sender')
  scope: watermarkPoisonQueue
  properties: {
    principalId: workerIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: storageQueueDataMessageSenderRole
  }
}

resource jobsDatabaseSecretAccess 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(jobsDatabaseSecret.id, workerIdentity.id, 'key-vault-secrets-user')
  scope: jobsDatabaseSecret
  properties: {
    principalId: workerIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: keyVaultSecretsUserRole
  }
}

resource acrPull 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(registry.id, workerIdentity.id, 'acr-pull')
  scope: registry
  properties: {
    principalId: workerIdentity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: acrPullRole
  }
}

output clientId string = workerIdentity.properties.clientId
output principalId string = workerIdentity.properties.principalId
