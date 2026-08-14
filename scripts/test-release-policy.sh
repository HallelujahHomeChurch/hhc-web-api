#!/bin/sh
set -eu

workflow=.github/workflows/release.yml

test ! -e azure-pipelines.yml
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
grep -Fq 'param cmsTranslationEnabled bool = false' infra/main.bicep
grep -Fq 'CMS_TRANSLATION_ENABLED: "true"' "$workflow"
grep -Fq "var azureOpenAIRaiPolicyName = 'hhc-cms-translation-v1'" infra/main.bicep
grep -Fq "name: 'AZURE_OPENAI_RAI_POLICY', value: azureOpenAIRaiPolicyName" infra/main.bicep
grep -Fq "resource translationRAIPolicy 'Microsoft.CognitiveServices/accounts/raiPolicies@2024-10-01'" infra/main.bicep
test "$(grep -Fc "severityThreshold: 'High'" infra/main.bicep)" -eq 8
test "$(grep -Fc "action: 'NONE'" infra/main.bicep)" -eq 9
grep -Fq "name: 'Jailbreak'" infra/main.bicep
grep -Fq 'blocking: false' infra/main.bicep
grep -Fq 'AZURE_OPENAI_RAI_POLICY: "hhc-cms-translation-v1"' "$workflow"
grep -Fq 'az rest --method get' "$workflow"
grep -Fq 'raiPolicies/$AZURE_OPENAI_RAI_POLICY?api-version=2024-10-01' "$workflow"
grep -Fq '. as $policy' "$workflow"
grep -Fq '$policy.properties.contentFilters[]' "$workflow"
grep -Fq 'var translationConfigured = !empty(azureOpenAIEndpoint) && !empty(azureOpenAIDeployment)' infra/main.bicep
test "$(grep -Fc 'translationConfigured ? [' infra/main.bicep)" -eq 2
test "$(grep -Fc 'cmsTranslationEnabled ? [' infra/main.bicep)" -eq 0
grep -Fq "{ name: 'CMS_TRANSLATION_ENABLED', value: cmsTranslationEnabled ? 'true' : 'false' }" infra/main.bicep
grep -Fq "keyVaultUrl: '\${runtimeVault.properties.vaultUri}secrets/hhc-web-azure-openai-api-key'" infra/main.bicep
grep -Fq 'translation_configured=false' "$workflow"
grep -Fq 'if [[ -n "$AZURE_OPENAI_ENDPOINT" || -n "$AZURE_OPENAI_DEPLOYMENT" ]]; then' "$workflow"
grep -Fq 'test -n "$AZURE_OPENAI_ENDPOINT" && test -n "$AZURE_OPENAI_DEPLOYMENT"' "$workflow"
grep -Fq 'if [[ "$CMS_TRANSLATION_ENABLED" == "true" && "$translation_configured" != "true" ]]; then' "$workflow"
grep -Fq 'if [[ -n "$AZURE_OPENAI_ENDPOINT" && -n "$AZURE_OPENAI_DEPLOYMENT" ]]; then' "$workflow"
grep -Fq 'az keyvault secret show --vault-name alive-hhw-runtime-kv' "$workflow"
grep -Fq -- '--name hhc-web-azure-openai-api-key --query name --output tsv --only-show-errors' "$workflow"
grep -Fq '[[ "$translation_secret_name" == "hhc-web-azure-openai-api-key" ]]' "$workflow"
test "$(grep -Fc 'cmsTranslationEnabled="$CMS_TRANSLATION_ENABLED" azureOpenAIEndpoint="$AZURE_OPENAI_ENDPOINT" azureOpenAIDeployment="$AZURE_OPENAI_DEPLOYMENT"' "$workflow")" -eq 3
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

echo 'release policy verified'
