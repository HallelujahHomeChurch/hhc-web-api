#!/bin/sh
set -eu

workflow=.github/workflows/release.yml
import_workflow=.github/workflows/content-migration.yml

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

run_smoke_case() {
  mode="$1"
  location_status="$2"
  location_content_type="$3"
  location_body="$4"
  expected="$5"
  expected_location_request="$6"
  case_dir="$(mktemp -d)"
  mkdir "$case_dir/bin"
  cat >"$case_dir/bin/timeout" <<'SH'
#!/bin/sh
shift
exec "$@"
SH
  cat >"$case_dir/bin/script" <<'SH'
#!/bin/sh
printf '%s\n' READY_OK PUBLIC_OK 'HTTP/1.1 401'
SH
  cat >"$case_dir/bin/curl" <<'SH'
#!/bin/sh
headers=''
body=''
url=''
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dump-header|--output|--write-out|--max-time)
      case "$1" in
        --dump-header) headers="$2" ;;
        --output) body="$2" ;;
      esac
      shift 2
      ;;
    http*) url="$1"; shift ;;
    *) shift ;;
  esac
done
printf '%s\n' "$url" >>"$SMOKE_EVENTS"
case "$url" in
  */api/locations*)
    printf 'HTTP/1.1 %s\nContent-Type: %s\n\n' "$SMOKE_LOCATION_STATUS" "$SMOKE_LOCATION_CONTENT_TYPE" >"$headers"
    printf '%s\n' "$SMOKE_LOCATION_BODY" >"$body"
    printf '%s' "$SMOKE_LOCATION_STATUS"
    ;;
esac
SH
  chmod +x "$case_dir/bin/timeout" "$case_dir/bin/script" "$case_dir/bin/curl"
  set +e
  PATH="$case_dir/bin:$PATH" SMOKE_MODE="$mode" SMOKE_EVENTS="$case_dir/events" \
    SMOKE_LOCATION_STATUS="$location_status" SMOKE_LOCATION_CONTENT_TYPE="$location_content_type" \
    SMOKE_LOCATION_BODY="$location_body" ./scripts/smoke-release.sh >/dev/null 2>&1
  status=$?
  set -e
  case "$expected" in
    pass) test "$status" -eq 0 ;;
    fail) test "$status" -ne 0 ;;
  esac
  case "$expected_location_request" in
    requested) grep -Eq '/api/locations([?]|$)' "$case_dir/events" ;;
    skipped) ! grep -Eq '/api/locations([?]|$)' "$case_dir/events" 2>/dev/null ;;
  esac
  rm -rf "$case_dir"
}

run_smoke_case invalid 200 application/json '{"data":[],"meta":{},"error":null}' fail skipped
run_smoke_case '' 200 application/json '{"data":[],"meta":{},"error":null}' fail skipped
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' pass requested
run_smoke_case forward 200 'application/json; charset=utf-8' '{"data":[{"id":"taipei","name":"台北","address":"台北地址","mapHref":"https://maps.example.com/taipei","sortOrder":10,"resolvedLocale":"zh-Hant","availableLocales":["zh-Hant","en"]}],"meta":{},"error":null}' pass requested
run_smoke_case forward 200 application/json '{"data":[{"id":"taipei","name":"台北","address":"台北地址","mapHref":"https://maps.example.com/taipei","sortOrder":10,"resolvedLocale":"zh-Hant"}],"meta":{},"error":null}' fail requested
run_smoke_case forward 404 application/json '{"data":null,"meta":{},"error":{"code":"not_found"}}' fail requested
run_smoke_case forward 200 text/html '{"data":[],"meta":{},"error":null}' fail requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":{"code":"unexpected"}}' fail requested
run_smoke_case rollback 404 application/json '{"data":null,"meta":{},"error":{"code":"not_found"}}' pass skipped

test -f "$import_workflow" || {
  echo 'missing manual content migration workflow' >&2
  exit 1
}
release_concurrency_group="$(awk '$0 == "concurrency:" { found = 1; next } found && /^  group:/ { print $2; exit }' "$workflow")"
import_concurrency_group="$(awk '$0 == "concurrency:" { found = 1; next } found && /^  group:/ { print $2; exit }' "$import_workflow")"
test -n "$release_concurrency_group"
test "$import_concurrency_group" = "$release_concurrency_group"

content_import_job="$(sed -n '/^resource contentImport /,/^}/p' infra/main.bicep)"
printf '%s\n' "$content_import_job" | grep -Fq "resource contentImport 'Microsoft.App/jobs@2024-03-01'"
printf '%s\n' "$content_import_job" | grep -Fq "name: 'hhc-web-content-import'"
printf '%s\n' "$content_import_job" | grep -Fq "'\${apiIdentity.id}': {}"
printf '%s\n' "$content_import_job" | grep -Fq 'environmentId: environment.id'
printf '%s\n' "$content_import_job" | grep -Fq "triggerType: 'Manual'"
printf '%s\n' "$content_import_job" | grep -Fq 'replicaTimeout: 900'
printf '%s\n' "$content_import_job" | grep -Fq 'replicaRetryLimit: 0'
printf '%s\n' "$content_import_job" | grep -Fq 'parallelism: 1'
printf '%s\n' "$content_import_job" | grep -Fq 'replicaCompletionCount: 1'
printf '%s\n' "$content_import_job" | grep -Fq 'identity: apiIdentity.id'
printf '%s\n' "$content_import_job" | grep -Fq "keyVaultUrl: '\${runtimeVault.properties.vaultUri}secrets/database-url'"
printf '%s\n' "$content_import_job" | grep -Fq "name: 'content-import'"
printf '%s\n' "$content_import_job" | grep -Fq 'image: runtimeImage'
printf '%s\n' "$content_import_job" | grep -Fq "command: ['/hhc-web-content-import']"
printf '%s\n' "$content_import_job" | grep -Fq "args: ['--mode=inventory']"
printf '%s\n' "$content_import_job" | grep -Fq "{ name: 'DATABASE_URL', secretRef: 'database-url' }"
if printf '%s\n' "$content_import_job" | grep -Eq 'migrateIdentity|migrationVault'; then
  echo 'content import job must use only the runtime identity and vault' >&2
  exit 1
fi

test "$(grep -Fc 'az containerapp job start' "$workflow")" -eq 1
grep -Fq 'az containerapp job start -g "$RESOURCE_GROUP" -n "$MIGRATION_JOB_NAME"' "$workflow"
grep -Fq 'Verify content import job image' "$workflow"
grep -Fq 'CONTENT_IMPORT_JOB_NAME: hhc-web-content-import' "$workflow"
grep -Fq 'az containerapp job show' "$workflow"
grep -Fq '[[ "$content_import_image" == "$IMAGE_REF" ]]' "$workflow"
release_update_line="$(grep -nF 'name: Update release jobs' "$workflow" | cut -d: -f1)"
release_migration_start_line="$(grep -nF 'az containerapp job start -g "$RESOURCE_GROUP" -n "$MIGRATION_JOB_NAME"' "$workflow" | cut -d: -f1)"
release_deploy_line="$(grep -nF 'name: Deploy API' "$workflow" | cut -d: -f1)"
release_import_verify_line="$(grep -nF 'name: Verify content import job image' "$workflow" | cut -d: -f1)"
release_rollback_line="$(grep -nF 'name: Roll back failed runtime' "$workflow" | cut -d: -f1)"
test "$release_update_line" -lt "$release_migration_start_line"
test "$release_migration_start_line" -lt "$release_deploy_line"
test "$release_deploy_line" -lt "$release_import_verify_line"
test "$release_import_verify_line" -lt "$release_rollback_line"

grep -Fq 'mode:' "$import_workflow"
for mode in inventory plan apply; do
  grep -Fq -- "- $mode" "$import_workflow"
done
grep -Fq 'seed_version:' "$import_workflow"
grep -Fq 'manifest_sha:' "$import_workflow"
grep -Fq 'preflight:' "$import_workflow"
grep -Fq 'approved_apply:' "$import_workflow"
preflight_job="$(sed -n '/^  preflight:/,/^  approved_apply:/p' "$import_workflow")"
apply_job="$(sed -n '/^  approved_apply:/,$p' "$import_workflow")"
input_validation_body="$(awk '
  /^      - name: Validate workflow inputs$/ { found = 1; next }
  found && /^        run: \|$/ { capture = 1; next }
  capture && /^      - / { exit }
  capture { sub(/^          /, ""); print }
' "$import_workflow")"
test -n "$input_validation_body"
run_input_validation_case() {
  mode="$1"
  run_attempt="$2"
  github_ref="$3"
  expected="$4"
  event_file="$(mktemp)"
  rm -f "$event_file"
  if AZURE_CLIENT_ID=test AZURE_TENANT_ID=test AZURE_SUBSCRIPTION_ID=test \
    GITHUB_REF="$github_ref" REQUESTED_MODE="$mode" RUN_ATTEMPT="$run_attempt" \
    REVIEWED_SEED_VERSION=reviewed-v1 REVIEWED_MANIFEST_SHA=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef \
    VALIDATION_BODY="$input_validation_body" EVENT_FILE="$event_file" bash -euo pipefail -c '
      eval "$VALIDATION_BODY"
      printf "login\nstart\n" > "$EVENT_FILE"
    ' >/dev/null 2>&1; then
    status=0
  else
    status=$?
  fi
  case "$expected" in
    pass)
      test "$status" -eq 0
      grep -Fxq login "$event_file"
      grep -Fxq start "$event_file"
      ;;
    fail)
      test "$status" -ne 0
      test ! -e "$event_file"
      ;;
  esac
  rm -f "$event_file"
}
run_input_validation_case apply 1 refs/heads/main pass
run_input_validation_case apply 2 refs/heads/main fail
run_input_validation_case inventory 2 refs/heads/main pass
run_input_validation_case plan 2 refs/heads/main pass
run_input_validation_case apply 1 refs/heads/feature fail
run_input_validation_case inventory 1 refs/heads/feature fail
run_input_validation_case plan 1 refs/tags/v1 fail
input_validation_line="$(printf '%s\n' "$preflight_job" | grep -nF 'name: Validate workflow inputs' | cut -d: -f1)"
azure_login_line="$(printf '%s\n' "$preflight_job" | grep -nF 'name: Sign in to Azure with OIDC' | cut -d: -f1)"
preflight_start_line="$(printf '%s\n' "$preflight_job" | grep -nF 'az containerapp job start' | cut -d: -f1)"
test "$input_validation_line" -lt "$azure_login_line"
test "$azure_login_line" -lt "$preflight_start_line"
if printf '%s\n' "$preflight_job" | grep -q 'environment: production'; then
  echo 'read-only content migration preflight must not require production approval' >&2
  exit 1
fi
printf '%s\n' "$preflight_job" | grep -Fq 'MODE=plan'
printf '%s\n' "$preflight_job" | grep -Fq 'inputs.seed_version'
printf '%s\n' "$preflight_job" | grep -Fq 'inputs.manifest_sha'
printf '%s\n' "$apply_job" | grep -Fq 'needs: preflight'
printf '%s\n' "$apply_job" | grep -Fq "if: \${{ inputs.mode == 'apply' && github.run_attempt == 1 }}"
printf '%s\n' "$apply_job" | grep -Fq 'environment: production'
test "$(grep -Fc 'environment: production' "$import_workflow")" -eq 1
printf '%s\n' "$apply_job" | grep -Fq 'needs.preflight.outputs.image_ref'
printf '%s\n' "$apply_job" | grep -Fq '"--confirmation=\($confirmation)"'
printf '%s\n' "$apply_job" | grep -Fq '"--expected-manifest-sha=\($sha)"'
test "$(grep -Fc 'uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' "$import_workflow")" -eq 2
test "$(grep -Fc 'uses: azure/login@532459ea530d8321f2fb9bb10d1e0bcf23869a43' "$import_workflow")" -eq 2
test "$(grep -Fc 'az containerapp job start' "$import_workflow")" -eq 2
test "$(grep -Fc -- '--yaml "$execution_template"' "$import_workflow")" -eq 2
if grep -Fq -- '--args "--mode=' "$import_workflow"; then
  echo 'content migration must pass dash-leading importer arguments through an execution template' >&2
  exit 1
fi
execution_filters="$(grep -F '.containers[0].args = [' "$import_workflow")"
test "$(printf '%s\n' "$execution_filters" | awk 'NF { count++ } END { print count + 0 }')" -eq 2
test "$(printf '%s\n' "$execution_filters" | sort -u | awk 'NF { count++ } END { print count + 0 }')" -eq 1
execution_filter="$(printf '%s\n' "$execution_filters" | head -1 | sed -e "s/^[[:space:]]*'//" -e "s/' \\\\$//")"
sample_template='{"containers":[{"name":"content-import","image":"registry/app@sha256:abc","command":["/hhc-web-content-import"],"args":["--mode=inventory"],"env":[{"name":"DATABASE_URL","secretRef":"database-url"}],"resources":{"cpu":0.25,"memory":"0.5Gi"}}],"initContainers":[{"name":"init"}]}'
rendered_template="$(printf '%s\n' "$sample_template" | jq -c \
  --arg mode plan --arg confirmation reviewed-v1 --arg sha 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef \
  "$execution_filter")"
expected_template='{"containers":[{"name":"content-import","image":"registry/app@sha256:abc","command":["/hhc-web-content-import"],"args":["--mode=plan","--confirmation=reviewed-v1","--expected-manifest-sha=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"],"env":[{"name":"DATABASE_URL","secretRef":"database-url"}],"resources":{"cpu":0.25,"memory":"0.5Gi"}}],"initContainers":[{"name":"init"}]}'
test "$rendered_template" = "$expected_template"
test "$(grep -Fc -- '--job-execution-name "$execution_name"' "$import_workflow")" -eq 2
test "$(grep -Fc 'az containerapp env show' "$import_workflow")" -eq 2
test "$(grep -Fc 'az monitor log-analytics query' "$import_workflow")" -eq 2
test "$(grep -Fc 'ContainerGroupName_s startswith '\''${execution_name}-'\''' "$import_workflow")" -eq 2
test "$(grep -Fc '[[ "$execution_name" =~ ^[a-z0-9-]+$ ]]' "$import_workflow")" -eq 2
test "$(grep -Fc 'for attempt in {1..30}; do' "$import_workflow")" -eq 2
if grep -Fq 'az containerapp job logs show' "$import_workflow"; then
  echo 'content migration evidence must use durable execution-scoped Log Analytics logs' >&2
  exit 1
fi
test "$(grep -Fc 'properties.latestReadyRevisionName' "$import_workflow")" -eq 2
test "$(grep -Fc 'az containerapp revision show' "$import_workflow")" -eq 2
test "$(grep -Fc 'az acr repository show' "$import_workflow")" -eq 2
test "$(grep -Fc 'runtime_image_ref="$(resolve_image_ref "$runtime_image")"' "$import_workflow")" -eq 2

apply_verify_line="$(printf '%s\n' "$apply_job" | grep -nF '[[ "$current_image" == "$REVIEWED_IMAGE_REF" ]]' | cut -d: -f1)"
apply_start_line="$(printf '%s\n' "$apply_job" | grep -nF 'az containerapp job start' | cut -d: -f1)"
test "$apply_verify_line" -lt "$apply_start_line"
for job in "$preflight_job" "$apply_job"; do
  start_line="$(printf '%s\n' "$job" | grep -nF 'az containerapp job start' | cut -d: -f1)"
  status_line="$(printf '%s\n' "$job" | grep -nF 'az containerapp job execution show' | cut -d: -f1)"
  logs_line="$(printf '%s\n' "$job" | grep -nF 'az monitor log-analytics query' | cut -d: -f1)"
  report_line="$(printf '%s\n' "$job" | grep -nF 'report="$(validate_report' | cut -d: -f1)"
  test "$start_line" -lt "$status_line"
  test "$status_line" -lt "$logs_line"
  test "$logs_line" -lt "$report_line"
done

extract_workflow_function() {
  function_name="$1"
  job_body="$2"
  printf '%s\n' "$job_body" | awk -v function_name="$function_name" '
    $0 == "          " function_name "() {" { capture = 1 }
    capture {
      line = $0
      sub(/^          /, "")
      print
      if (line == "          }") exit
    }
  '
}

run_image_match_case() {
  image_guard="$1"
  job_image="$2"
  runtime_image="$3"
  expected="$4"
  if IMAGE_GUARD="$image_guard" JOB_IMAGE="$job_image" RUNTIME_IMAGE="$runtime_image" bash -euo pipefail -c '
    eval "$IMAGE_GUARD"
    require_same_image "$JOB_IMAGE" "$RUNTIME_IMAGE"
  ' >/dev/null 2>&1; then
    status=0
  else
    status=$?
  fi
  case "$expected" in
    pass) test "$status" -eq 0 ;;
    fail) test "$status" -ne 0 ;;
  esac
}

image_a=alive.azurecr.io/alive/hhc-web-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
image_b=alive.azurecr.io/alive/hhc-web-api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
for job in "$preflight_job" "$apply_job"; do
  image_guard="$(extract_workflow_function require_same_image "$job")"
  test -n "$image_guard"
  run_image_match_case "$image_guard" "$image_a" "$image_a" pass
  run_image_match_case "$image_guard" "$image_a" "$image_b" fail
  run_image_match_case "$image_guard" alive.azurecr.io/alive/hhc-web-api:main "$image_a" fail

  job_image_line="$(printf '%s\n' "$job" | grep -nF 'az containerapp job show' | head -1 | cut -d: -f1)"
  runtime_line="$(printf '%s\n' "$job" | grep -nF 'properties.latestReadyRevisionName' | cut -d: -f1)"
  compare_line="$(printf '%s\n' "$job" | grep -nF 'require_same_image ' | tail -1 | cut -d: -f1)"
  start_line="$(printf '%s\n' "$job" | grep -nF 'az containerapp job start' | cut -d: -f1)"
  attempt_guard_line="$(printf '%s\n' "$job" | grep -nF '[[ "$RUN_ATTEMPT" == 1 ]]' | tail -1 | cut -d: -f1)"
  test "$attempt_guard_line" -lt "$start_line"
  test "$job_image_line" -lt "$runtime_line"
  test "$runtime_line" -lt "$compare_line"
  test "$compare_line" -lt "$start_line"
  test "$(printf '%s\n' "$job" | sed -n "${compare_line},${start_line}p" | grep -Fc 'az ')" -eq 1
done

preflight_validation_function="$(extract_workflow_function validate_report "$preflight_job")"
apply_validation_function="$(extract_workflow_function validate_report "$apply_job")"
test -n "$preflight_validation_function"
test -n "$apply_validation_function"
test "$preflight_validation_function" = "$apply_validation_function"

run_report_validation_case() {
  logs="$1"
  mode="$2"
  seed_version="$3"
  manifest_sha="$4"
  expected="$5"
  if output="$(LOGS="$logs" MODE="$mode" SEED_VERSION="$seed_version" MANIFEST_SHA="$manifest_sha" VALIDATION_FUNCTION="$validation_function" bash -euo pipefail -c '
    eval "$VALIDATION_FUNCTION"
    validate_report "$LOGS" "$MODE" "$SEED_VERSION" "$MANIFEST_SHA"
  ' 2>&1)"; then
    status=0
  else
    status=$?
  fi
  case "$expected" in
    pass)
      test "$status" -eq 0
      printf '%s\n' "$output" | jq -e --arg mode "$mode" --arg seed "$seed_version" --arg sha "$manifest_sha" \
        '.mode == $mode and .seedVersion == $seed and .manifestSHA256 == $sha' >/dev/null
      ;;
    fail) test "$status" -ne 0 ;;
  esac
}

report_sha=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
valid_report="{\"mode\":\"plan\",\"seedVersion\":\"reviewed-v1\",\"manifestSHA256\":\"$report_sha\",\"inserts\":0,\"skips\":0,\"updates\":0,\"deletes\":0,\"warnings\":0,\"conflicts\":0}"
duplicate_reports="$(printf '%s\n%s\n' "$valid_report" "$valid_report")"
valid_and_malformed="$(printf '%s\n%s\n' "$valid_report" '{"mode":"plan"')"
unsafe_seed_report="$(printf '%s\n' "$valid_report" | jq -c '.seedVersion = "unsafe\\noutput"')"
for validation_function in "$preflight_validation_function" "$apply_validation_function"; do
  run_report_validation_case "$valid_report" plan reviewed-v1 "$report_sha" pass
  run_report_validation_case "$duplicate_reports" plan reviewed-v1 "$report_sha" fail
  run_report_validation_case 'no importer report' plan reviewed-v1 "$report_sha" fail
  run_report_validation_case '{"mode":"plan"' plan reviewed-v1 "$report_sha" fail
  run_report_validation_case "$valid_and_malformed" plan reviewed-v1 "$report_sha" fail
  run_report_validation_case "$unsafe_seed_report" plan '' "$report_sha" fail
  run_report_validation_case "$valid_report" apply reviewed-v1 "$report_sha" fail
  run_report_validation_case "$valid_report" plan other-v1 "$report_sha" fail
  run_report_validation_case "$valid_report" plan reviewed-v1 ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff fail
  for field in updates deletes warnings conflicts; do
    nonzero_report="$(printf '%s\n' "$valid_report" | jq -c --arg field "$field" '.[$field] = 1')"
    run_report_validation_case "$nonzero_report" plan reviewed-v1 "$report_sha" fail
  done
  for field in inserts skips; do
    missing_report="$(printf '%s\n' "$valid_report" | jq -c --arg field "$field" 'del(.[$field])')"
    negative_report="$(printf '%s\n' "$valid_report" | jq -c --arg field "$field" '.[$field] = -1')"
    fractional_report="$(printf '%s\n' "$valid_report" | jq -c --arg field "$field" '.[$field] = 0.5')"
    string_report="$(printf '%s\n' "$valid_report" | jq -c --arg field "$field" '.[$field] = "0"')"
    run_report_validation_case "$missing_report" plan reviewed-v1 "$report_sha" fail
    run_report_validation_case "$negative_report" plan reviewed-v1 "$report_sha" fail
    run_report_validation_case "$fractional_report" plan reviewed-v1 "$report_sha" fail
    run_report_validation_case "$string_report" plan reviewed-v1 "$report_sha" fail
  done
done

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
smoke_line="$(grep -n 'run: SMOKE_MODE=forward ./scripts/smoke-release.sh' "$workflow" | cut -d: -f1)"
rollback_smoke_line="$(grep -n 'run: SMOKE_MODE=rollback ./scripts/smoke-release.sh' "$workflow" | cut -d: -f1)"
outputs_line="$(grep -n 'id: release_outputs' "$workflow" | cut -d: -f1)"
rollback_line="$(grep -n 'name: Roll back failed runtime' "$workflow" | cut -d: -f1)"
guard_line="$(grep -nF 'pointer_exists="$(az storage blob exists' "$workflow" | cut -d: -f1)"
guard_exit_line="$(awk '/skipping stale or rerun publication/ { getline; if ($0 ~ /^[[:space:]]*exit 0$/) print NR }' "$workflow")"
pointer_upload_line="$(awk '/az storage blob upload/ { upload = 1 } upload && /--file current.json/ { print NR; exit }' "$workflow")"
test "$smoke_line" -lt "$outputs_line"
test "$outputs_line" -lt "$rollback_line"
test "$rollback_line" -lt "$rollback_smoke_line"
test "$deploy_line" -lt "$publish_line"
test "$smoke_line" -lt "$pointer_upload_line"
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
