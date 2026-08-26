#!/bin/sh
set -eu

workflow=.github/workflows/release.yml

test ! -e azure-pipelines.yml
grep -q 'workflow_dispatch:' "$workflow"
grep -q '^  push:' "$workflow"
grep -q 'branches: \[main\]' "$workflow"
expected_paths_ignore='docs/**
openapi.yaml
openapi_test.go
.github/workflows/ci.yml'
actual_paths_ignore="$(awk '
  $0 == "    paths-ignore:" { in_paths_ignore = 1; next }
  in_paths_ignore && /^      - / { sub(/^      - /, ""); print; next }
  in_paths_ignore { exit }
' "$workflow")"
if [ "$actual_paths_ignore" != "$expected_paths_ignore" ]; then
  echo 'release docs-only paths-ignore policy mismatch' >&2
  exit 1
fi
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
grep -q 'RUNTIME_CPU=' "$workflow"
grep -q 'RUNTIME_MEMORY=' "$workflow"
grep -Fq -- '--query properties.template.containers[0].resources.cpu -o tsv' "$workflow"
grep -Fq -- '--query properties.template.containers[0].resources.memory -o tsv' "$workflow"
test "$(grep -Fc 'runtimeCpu="$RUNTIME_CPU" runtimeMemory="$RUNTIME_MEMORY"' "$workflow")" -eq 3
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
grep -Fq 'param runtimeCpu string' infra/main.bicep
grep -Fq 'param runtimeMemory string' infra/main.bicep
grep -Fq "cpu: json(runtimeCpu)" infra/main.bicep
grep -Fq 'memory: runtimeMemory' infra/main.bicep
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
grep -Fq 'npx --yes @redocly/cli@2.47.0 lint openapi.yaml' .github/workflows/ci.yml
grep -Fq './scripts/test-release-policy.sh' .github/workflows/ci.yml

grep -q 'fail_openapi_before_pointer:' "$workflow"
grep -q '^  publish_openapi:' "$workflow"
grep -q 'commit: ${{ steps.release_outputs.outputs.commit }}' "$workflow"
grep -q 'image: ${{ steps.release_outputs.outputs.image }}' "$workflow"

publish_job="$(sed -n '/^  publish_openapi:/,$p' "$workflow")"
printf '%s\n' "$publish_job" | grep -q 'needs: deploy'
printf '%s\n' "$publish_job" | grep -q 'environment: production'
printf '%s\n' "$publish_job" | grep -q 'contents: read'
printf '%s\n' "$publish_job" | grep -q 'id-token: write'
printf '%s\n' "$publish_job" | grep -q 'API_DOCS_AZURE_CLIENT_ID'
printf '%s\n' "$publish_job" | grep -q 'api-docs-hhc-web-api'
printf '%s\n' "$publish_job" | grep -q 'hhcapidocsprod'
printf '%s\n' "$publish_job" | grep -q 'needs.deploy.outputs.commit'
printf '%s\n' "$publish_job" | grep -q 'needs.deploy.outputs.image'
printf '%s\n' "$publish_job" | grep -q 'specs/${GITHUB_SHA}/openapi.yaml'
printf '%s\n' "$publish_job" | grep -q 'inputs.fail_openapi_before_pointer && github.run_attempt == 1'
printf '%s\n' "$publish_job" | grep -q -- '--overwrite false'
printf '%s\n' "$publish_job" | grep -q -- '--name current.json'
printf '%s\n' "$publish_job" | grep -q -- '--overwrite true'

workflow_body="$(sed -n '/^          spec_blob="specs\//,$p' "$workflow" | sed 's/^          //')"
run_openapi_publication_case() {
  pointer_json="$1"
  candidate_run_id="$2"
  expected="$3"
  failure_injection="${4:-false}"
  spec_fixture="${5:-missing}"
  case_dir="$(mktemp -d)"
  mkdir "$case_dir/pointer"
  ln -s "$PWD/openapi.yaml" "$case_dir/openapi.yaml"
  if [ "$pointer_json" != missing ]; then
    printf '%s\n' "$pointer_json" > "$case_dir/pointer/current.json"
    cp "$case_dir/pointer/current.json" "$case_dir/expected-current.json"
  fi
  case "$spec_fixture" in
    identical)
      mkdir -p "$case_dir/blobs/specs/0123456789abcdef0123456789abcdef01234567"
      cp openapi.yaml "$case_dir/blobs/specs/0123456789abcdef0123456789abcdef01234567/openapi.yaml"
      ;;
    different)
      mkdir -p "$case_dir/blobs/specs/0123456789abcdef0123456789abcdef01234567"
      printf 'different spec\n' > "$case_dir/blobs/specs/0123456789abcdef0123456789abcdef01234567/openapi.yaml"
      ;;
  esac

  if output="$(POINTER_CASE_DIR="$case_dir" WORKFLOW_BODY="$workflow_body" GITHUB_RUN_ID="$candidate_run_id" GITHUB_SHA=0123456789abcdef0123456789abcdef01234567 GITHUB_REPOSITORY=HallelujahHomeChurch/hhc-web-api RELEASE_COMMIT=0123456789abcdef0123456789abcdef01234567 RELEASE_IMAGE=alive.azurecr.io/alive/hhc-web-api@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef FAIL_OPENAPI_BEFORE_POINTER="$failure_injection" bash -e -c '
    az() {
      command="$1 $2 $3"
      name=""
      file=""
      overwrite=""
      while [ "$#" -gt 0 ]; do
        case "$1" in
          --name) name="$2"; shift 2 ;;
          --file) file="$2"; shift 2 ;;
          --overwrite) overwrite="$2"; shift 2 ;;
          *) shift ;;
        esac
      done
      blob="$POINTER_CASE_DIR/pointer/current.json"
      if [ "$name" != current.json ]; then
        blob="$POINTER_CASE_DIR/blobs/$name"
      fi
      case "$command" in
        "storage blob exists")
          if [ -f "$blob" ]; then printf true; else printf false; fi
          ;;
        "storage blob download")
          cp "$blob" "$file"
          if [ "$name" != current.json ]; then printf spec-download\\n >> "$POINTER_CASE_DIR/events"; fi
          ;;
        "storage blob upload")
          if [ -e "$blob" ] && [ "$overwrite" = false ]; then return 1; fi
          mkdir -p "$(dirname "$blob")"
          cp "$file" "$blob"
          if [ "$name" = current.json ]; then
            printf pointer-upload\\n >> "$POINTER_CASE_DIR/uploads"
          else
            printf spec-upload\\n >> "$POINTER_CASE_DIR/uploads"
          fi
          ;;
      esac
    }
    cd "$POINTER_CASE_DIR"
    eval "$WORKFLOW_BODY"
  ' 2>&1)"; then
    status=0
  else
    status=$?
  fi
  pointer_uploaded=false
  if [ -e "$case_dir/uploads" ] && grep -Fxq pointer-upload "$case_dir/uploads"; then
    pointer_uploaded=true
  fi

  case "$expected" in
    upload)
      test "$status" -eq 0
      test "$pointer_uploaded" = true
      grep -Fq "/runs/$candidate_run_id\"" "$case_dir/pointer/current.json"
      ;;
    noop)
      test "$status" -eq 0
      test "$pointer_uploaded" = false
      cmp "$case_dir/expected-current.json" "$case_dir/pointer/current.json"
      ;;
    invalid-pointer)
      test "$status" -ne 0
      test "$pointer_uploaded" = false
      cmp "$case_dir/expected-current.json" "$case_dir/pointer/current.json"
      printf '%s\n' "$output" | grep -Fq 'Invalid existing API docs pointer: expected canonical GitHub workflow run ID'
      ;;
    invalid-candidate)
      test "$status" -ne 0
      test "$pointer_uploaded" = false
      printf '%s\n' "$output" | grep -Fq 'Invalid GITHUB_RUN_ID: expected canonical positive decimal'
      ;;
    pre-pointer-failure)
      test "$status" -ne 0
      test "$pointer_uploaded" = false
      test ! -e "$case_dir/pointer/current.json"
      printf '%s\n' "$output" | grep -Fq 'Requested failure before API docs pointer upload'
      ;;
    spec-idempotent)
      test "$status" -eq 0
      grep -Fxq spec-download "$case_dir/events"
      if [ -e "$case_dir/uploads" ] && grep -Fxq spec-upload "$case_dir/uploads"; then
        exit 1
      fi
      grep -Fxq pointer-upload "$case_dir/uploads"
      grep -Fq "/runs/$candidate_run_id\"" "$case_dir/pointer/current.json"
      ;;
    spec-mismatch)
      test "$status" -ne 0
      test "$pointer_uploaded" = false
      cmp "$case_dir/expected-current.json" "$case_dir/pointer/current.json"
      ;;
    pre-pointer-preserve)
      test "$status" -ne 0
      test "$pointer_uploaded" = false
      cmp "$case_dir/expected-current.json" "$case_dir/pointer/current.json"
      printf '%s\n' "$output" | grep -Fq 'Requested failure before API docs pointer upload'
      ;;
  esac
  rm -rf "$case_dir"
}

valid_pointer='{"releaseUrl":"https://github.com/HallelujahHomeChurch/hhc-web-api/actions/runs/20"}'
run_openapi_publication_case missing 20 upload
run_openapi_publication_case missing 20 pre-pointer-failure true
run_openapi_publication_case "$valid_pointer" 21 spec-idempotent false identical
run_openapi_publication_case "$valid_pointer" 21 spec-mismatch false different
run_openapi_publication_case "$valid_pointer" 21 pre-pointer-preserve true
run_openapi_publication_case "$valid_pointer" 19 noop
run_openapi_publication_case "$valid_pointer" 20 noop
run_openapi_publication_case "$valid_pointer" 21 upload
run_openapi_publication_case '{' 22 invalid-pointer
run_openapi_publication_case '{}' 22 invalid-pointer
run_openapi_publication_case '{"releaseUrl":null}' 22 invalid-pointer
run_openapi_publication_case '{"releaseUrl":"https://github.com/HallelujahHomeChurch/hhc-web-api/actions/runs/09"}' 22 invalid-pointer
run_openapi_publication_case '{"releaseUrl":"https://github.com/HallelujahHomeChurch/hhc-web-api/actions/runs/0"}' 22 invalid-pointer
run_openapi_publication_case '{"releaseUrl":"https://github.com/HallelujahHomeChurch/hhc-web-api/actions/runs/99999999999999999999"}' 100000000000000000000 upload
run_openapi_publication_case missing 0 invalid-candidate
run_openapi_publication_case missing 01 invalid-candidate

printf '%s\n' "$publish_job" | grep -q 'pointer_exists="$(az storage blob exists'
printf '%s\n' "$publish_job" | grep -q 'current_pointer="$(mktemp)"'
printf '%s\n' "$publish_job" | grep -Fq 'Invalid GITHUB_RUN_ID: expected canonical positive decimal'
printf '%s\n' "$publish_job" | grep -Fq 'Invalid existing API docs pointer: expected canonical GitHub workflow run ID'
printf '%s\n' "$publish_job" | grep -Fq 'exit 0'
deploy_line="$(grep -n '^  deploy:' "$workflow" | cut -d: -f1)"
publish_line="$(grep -n '^  publish_openapi:' "$workflow" | cut -d: -f1)"
smoke_line="$(grep -n 'run: ./scripts/smoke-release.sh' "$workflow" | head -1 | cut -d: -f1)"
outputs_line="$(grep -n 'id: release_outputs' "$workflow" | cut -d: -f1)"
rollback_line="$(grep -n 'name: Roll back failed runtime' "$workflow" | cut -d: -f1)"
guard_line="$(grep -nF 'pointer_exists="$(az storage blob exists' "$workflow" | cut -d: -f1)"
guard_exit_line="$(awk '/skipping stale or rerun publication/ { getline; if ($0 ~ /^[[:space:]]*exit 0$/) print NR }' "$workflow")"
pointer_upload_line="$(awk '/az storage blob upload/ { upload = 1 } upload && /--file current.json/ { print NR; exit }' "$workflow")"
test "$smoke_line" -lt "$outputs_line"
test "$outputs_line" -lt "$rollback_line"
test "$deploy_line" -lt "$publish_line"
test "$guard_line" -lt "$guard_exit_line"
test "$guard_exit_line" -lt "$pointer_upload_line"

if grep -Eiq 'migrate[[:space:]_-]*down|migration[[:space:]_-]*rollback' "$workflow"; then
  echo 'release workflow must not roll back database migrations automatically' >&2
  exit 1
fi

if grep -Eq 'PREVIOUS_MIGRATION_IMAGE|Restore migration job|containerapp job update' "$workflow"; then
  echo 'release workflow must preserve the forward migration image' >&2
  exit 1
fi

echo 'release policy verified'
