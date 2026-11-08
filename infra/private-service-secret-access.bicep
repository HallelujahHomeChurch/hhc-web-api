targetScope = 'resourceGroup'

param runtimeKeyVaultName string

@secure()
param accountCredential string

@secure()
param operationsCredential string

@secure()
param engagementCredential string

@secure()
param lineCredential string

@secure()
param gatewayCredential string

@secure()
param gatewayLiveCredential string

var credentials = [accountCredential, operationsCredential, engagementCredential, lineCredential, gatewayCredential, gatewayLiveCredential]
var secretNames = [
  'cms-account-private-token'
  'cms-operations-private-token'
  'cms-engagement-private-token'
  'cms-line-private-token'
  'cms-gateway-private-token'
  'cms-gateway-live-private-token'
]
var callerNames = [
  'account-api-identity'
  'operations-api-runtime-identity'
  'engagement-api-identity'
]

resource vault 'Microsoft.KeyVault/vaults@2026-02-01' existing = {
  name: runtimeKeyVaultName
}

resource callers 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = [
  for name in callerNames: { name: name }
]

resource secrets 'Microsoft.KeyVault/vaults/secrets@2026-02-01' = [
  for (name, i) in secretNames: {
    parent: vault
    name: name
    properties: {
      value: credentials[i]
    }
  }
]

var readerRole = subscriptionResourceId('Microsoft.Authorization/roleDefinitions', '4633458b-17de-408a-b874-0445c86b69e6')

resource callerReaders 'Microsoft.Authorization/roleAssignments@2022-04-01' = [
  for i in range(0, length(callerNames)): {
    name: guid(secrets[i].id, callers[i].id, 'read')
    scope: secrets[i]
    properties: {
      principalId: callers[i].properties.principalId
      principalType: 'ServicePrincipal'
      roleDefinitionId: readerRole
    }
  }
]


resource line 'Microsoft.App/containerApps@2025-01-01' existing = {
  name: 'hhc-line-function-bot'
}

resource lineReader 'Microsoft.Authorization/roleAssignments@2022-04-01' = {
  name: guid(secrets[3].id, line.id, 'read')
  scope: secrets[3]
  properties: {
    principalId: line.identity.principalId
    principalType: 'ServicePrincipal'
    roleDefinitionId: readerRole
  }
}


resource gateway 'Microsoft.App/containerApps@2025-01-01' existing = {
  name: 'api-gateway'
}

resource gatewayReaders 'Microsoft.Authorization/roleAssignments@2022-04-01' = [
  for i in [4, 5]: {
    name: guid(secrets[i].id, gateway.id, 'read')
    scope: secrets[i]
    properties: {
      principalId: gateway.identity.principalId
      principalType: 'ServicePrincipal'
      roleDefinitionId: readerRole
    }
  }
]
