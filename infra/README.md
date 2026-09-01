# Production release

`hhc-web-api` is private to the Container Apps environment and is invoked through Dapr by `api-gateway`.

The internal ingress additionally accepts the dedicated Asset scan-warmer managed identity through Container Apps authentication. Only `GET /priv/meeting-sync-windows` accepts that verified workload principal; all other private routes retain Dapr caller and app-channel-token authentication. The warmer requests a token for `OPERATIONS_WORKLOAD_AUDIENCE`; no Dapr app-channel token is copied or persisted.

Azure Container Apps injects the revision-scoped `APP_API_TOKEN` used to verify inbound Dapr requests. Do not persist it in Key Vault, declare it in Bicep, or reuse it as an outbound `DAPR_API_TOKEN`.

Initial bootstrap:

```sh
# 1. Create the isolated vaults and identities without changing either workload.
az deployment group create \
  --resource-group alive \
  --template-file infra/main.bicep \
  --parameters migrationImage=<current-image> runtimeImage=<current-image> \
    runtimeCpu=<current-cpu> runtimeMemory=<current-memory> \
    deployRuntime=false deployMigrationJob=false provisionPermissions=true

# 2. Temporarily grant the signed-in operator access, then migrate the existing
# runtime DSN and create the migration-only role/DSN.
operator_id="$(az ad signed-in-user show --query id -o tsv)"
for vault in alive-hhw-runtime-kv alive-hhw-migrate-kv; do
  scope="$(az keyvault show --name "$vault" --query id -o tsv)"
  az role assignment create --assignee-object-id "$operator_id" \
    --assignee-principal-type User --role 'Key Vault Secrets Officer' --scope "$scope"
done
./scripts/bootstrap-migration-role.sh

# 3. Remove the temporary roles and cut both workloads over using the currently
# deployed image.
for vault in alive-hhw-runtime-kv alive-hhw-migrate-kv; do
  scope="$(az keyvault show --name "$vault" --query id -o tsv)"
  az role assignment delete --assignee-object-id "$operator_id" \
    --role 'Key Vault Secrets Officer' --scope "$scope"
done
az deployment group create \
  --resource-group alive \
  --template-file infra/main.bicep \
  --parameters migrationImage=<current-image> runtimeImage=<current-image> \
    runtimeCpu=<current-cpu> runtimeMemory=<current-memory> \
    deployRuntime=true deployMigrationJob=true provisionPermissions=false \
    retainLegacyRuntimeSecret=true

# 4. After the new runtime revision is ready and the rollback window closes,
# remove the old app-level secret reference, then remove legacy vault access.
az deployment group create \
  --resource-group alive \
  --template-file infra/main.bicep \
  --parameters migrationImage=<current-image> runtimeImage=<current-image> \
    runtimeCpu=<current-cpu> runtimeMemory=<current-memory> \
    deployRuntime=true deployMigrationJob=true provisionPermissions=false \
    retainLegacyRuntimeSecret=false
runtime_principal="$(az identity show --resource-group alive --name hhc-web-api-identity --query principalId -o tsv)"
az keyvault delete-policy --name alive-vault --object-id "$runtime_principal"
test "$(az keyvault show --name alive-vault --query "properties.accessPolicies[?objectId=='${runtime_principal}'] | length(@)" -o tsv)" = 0
```

`hhc_web` keeps DML only. `hhc_web_migrate` owns the database, schema, tables, sequences, and migration history. The two credentials and managed identities use separate Key Vaults.

Keep the legacy secret and policy through the first rollback window. After cleanup, confirm the normal production `what-if` has no delete before starting a code release.

Every release uses one immutable image for the migration job and runtime. The release rejects destructive resource/property changes and destructive migration SQL. A failed migration stops before the runtime revision changes. Runtime rollback never rolls back the forward-only schema.
