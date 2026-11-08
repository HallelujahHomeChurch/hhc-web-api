param runId string
param image string
param containerAppEnvironmentName string
param containerRegistryLoginServer string
@secure()
param password string
@secure()
param labDSN string

var dbName = 'hhc-v4-db-${runId}'
var jobName = 'hhc-v4-job-${runId}'
var tags = { owner: 'hhc-web-api', task: 'V4-405', temporary: 'true', run: runId }
resource environment 'Microsoft.App/managedEnvironments@2025-07-01' existing = { name: containerAppEnvironmentName }
resource pullIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = { name: 'asset-api-acr-pull' }

resource database 'Microsoft.App/containerApps@2025-07-01' = {
  name: dbName
  location: resourceGroup().location
  tags: tags
  properties: {
    managedEnvironmentId: environment.id
    workloadProfileName: 'Consumption'
    configuration: {
      activeRevisionsMode: 'Single'
      ingress: { external: false, targetPort: 5432, exposedPort: 5432, transport: 'tcp' }
      secrets: [{ name: 'lab-password', value: password }]
    }
    template: {
      containers: [{
        name: 'postgres'
        image: 'docker.io/library/postgres:17-alpine@sha256:18cfe3ef5e6815560c98237d6216d1e5119702fb0f3894c8785dd58b8bbe5d73'
        resources: { cpu: json('0.5'), memory: '1Gi' }
        env: [
          { name: 'POSTGRES_DB', value: 'v4_lab' }
          { name: 'POSTGRES_USER', value: 'v4_lab' }
          { name: 'POSTGRES_PASSWORD', secretRef: 'lab-password' }
        ]
        probes: [{ type: 'Readiness', tcpSocket: { port: 5432 }, initialDelaySeconds: 5, periodSeconds: 5 }]
      }]
      scale: { minReplicas: 1, maxReplicas: 1 }
    }
  }
}
resource lab 'Microsoft.App/jobs@2025-07-01' = {
  name: jobName
  location: resourceGroup().location
  tags: tags
  identity: { type: 'UserAssigned', userAssignedIdentities: { '${pullIdentity.id}': {} } }
  properties: {
    environmentId: environment.id
    workloadProfileName: 'Consumption'
    configuration: {
      triggerType: 'Manual'
      replicaTimeout: 300
      replicaRetryLimit: 0
      manualTriggerConfig: { parallelism: 20, replicaCompletionCount: 20 }
      registries: [{ server: containerRegistryLoginServer, identity: pullIdentity.id }]
      secrets: [{ name: 'lab-dsn', value: labDSN }]
    }
    template: {
      containers: [{
        name: 'v4'
        image: image
        resources: { cpu: 2, memory: '4Gi' }
        env: [{ name: 'V4_LAB_DSN', secretRef: 'lab-dsn' }]
      }]
    }
  }
}
