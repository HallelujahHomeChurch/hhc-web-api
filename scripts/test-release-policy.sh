#!/bin/sh
set -eu

workflow=.github/workflows/release.yml

grep -q '^trigger: none$' azure-pipelines.yml
grep -q '^pr: none$' azure-pipelines.yml
grep -q 'workflow_dispatch:' "$workflow"
grep -q '^  push:' "$workflow"
grep -q 'branches: \[main\]' "$workflow"
grep -Fq "github.event_name == 'push' && 'deploy-hhc-web-api-production' || inputs.confirmation" "$workflow"
grep -q 'deploy-hhc-web-api-production' "$workflow"
grep -q 'environment: production' "$workflow"
grep -q 'Verify isolated runtime prerequisites' "$workflow"
grep -q "roleDefinitionName=='Key Vault Secrets User'" "$workflow"
grep -q 'properties.accessPolicies' "$workflow"
grep -q 'IMAGE_REF=.*@${digest}' "$workflow"
grep -q 'az deployment group what-if' "$workflow"
grep -q -- '--validation-level Provider' "$workflow"
grep -q -- '--no-pretty-print' "$workflow"
grep -q './scripts/check-what-if.sh what-if.json' "$workflow"
grep -q 'PREVIOUS_IMAGE_REF=' "$workflow"
grep -q 'az containerapp revision copy' "$workflow"
grep -q -- '--image "$PREVIOUS_IMAGE_REF"' "$workflow"
grep -q 'Verify rolled back runtime' "$workflow"
grep -q '/health/live' infra/main.bicep
grep -q '/health/ready' infra/main.bicep
grep -q "type: 'Startup'" infra/main.bicep
grep -q "name: 'hhc-web-migrate-identity'" infra/main.bicep
grep -q "runtimeKeyVaultName string = 'alive-hhw-runtime-kv'" infra/main.bicep
grep -q "migrationKeyVaultName string = 'alive-hhw-migrate-kv'" infra/main.bicep
grep -q 'retainLegacyRuntimeSecret bool = false' infra/main.bicep
grep -q "name: 'database-url-v2'" infra/main.bicep
grep -q 'enableRbacAuthorization: true' infra/main.bicep
grep -q "workloadProfileName: 'Consumption'" infra/main.bicep
grep -q 'cooldownPeriod: 300' infra/main.bicep
grep -q 'pollingInterval: 30' infra/main.bicep
grep -q 'test-migration-policy-test.sh' .github/workflows/ci.yml
grep -q 'test-what-if-policy.sh' .github/workflows/ci.yml
grep -Fq 'test-migration-policy.sh internal/migrations/sql/*.sql' .github/workflows/ci.yml
grep -Fq 'test-migration-policy.sh internal/migrations/sql/*.sql' "$workflow"

if grep -Eiq 'migrate[[:space:]_-]*down|migration[[:space:]_-]*rollback' "$workflow"; then
  echo 'release workflow must not roll back database migrations automatically' >&2
  exit 1
fi

if grep -Eq 'PREVIOUS_MIGRATION_IMAGE|Restore migration job|containerapp job update' "$workflow"; then
  echo 'release workflow must preserve the forward migration image' >&2
  exit 1
fi
