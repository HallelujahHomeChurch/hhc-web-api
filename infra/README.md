# Production release

`hhc-web-api` is private to the Container Apps environment and is invoked through Dapr by `api-gateway`.

Initial bootstrap:

```sh
./scripts/bootstrap-database.sh
az deployment group create \
  --resource-group alive \
  --template-file infra/main.bicep \
  --parameters migrationImage=<immutable-image> runtimeImage=<immutable-image> deployRuntime=false provisionPermissions=true
```

Every release uses one immutable image for the migration job and runtime. A failed migration stops the release before the runtime revision changes. Rollback redeploys a previously verified image only when it remains compatible with the forward-only schema.
