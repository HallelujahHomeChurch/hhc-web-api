// Privileged bootstrap from merged main, after a reviewed Provider what-if.
// Release identities are Contributor only; regular service releases reference
// these queues and never create role assignments.
param queueStorageAccountName string
resource storage 'Microsoft.Storage/storageAccounts@2023-05-01' existing = { name: queueStorageAccountName }
resource service 'Microsoft.Storage/storageAccounts/queueServices@2023-05-01' existing = {
  parent: storage
  name: 'default'
}
resource identity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = {
  name: 'hhc-web-api-identity'
}
resource queue 'Microsoft.Storage/storageAccounts/queueServices/queues@2023-05-01' = {
  parent: service
  name: 'bulletin-extraction'
  properties: {}
}
resource poison 'Microsoft.Storage/storageAccounts/queueServices/queues@2023-05-01' = {
  parent: service
  name: 'bulletin-extraction-poison'
  properties: {}
}
resource processor 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(queue.id, identity.id, 'queue-data-contributor')
  scope: queue
  properties: {
    principalId: identity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId(
      'Microsoft.Authorization/roleDefinitions',
      '974c5e8b-45b9-4653-ba55-5f855dd0fb88'
    )
  }
}
resource quarantine 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(poison.id, identity.id, 'queue-data-message-sender')
  scope: poison
  properties: {
    principalId: identity.properties.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: subscriptionResourceId(
      'Microsoft.Authorization/roleDefinitions',
      'c6a89b2d-59bc-44d0-9896-0f6e12d7b80a'
    )
  }
}
