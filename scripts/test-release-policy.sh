#!/bin/sh
set -eu
eval "$(python3 scripts/test_no_cloud.py)"
python3 scripts/test-deployment-config.py
python3 scripts/test-public-release-evidence.py
grep -Fq "name: 'CMS_DAPR_ENABLED', value: cmsDaprEnabled ? 'true' : 'false'" infra/main.bicep
python3 scripts/test-release-runtime.py
api_scale="$(awk '/^resource api / { in_api = 1; next } in_api && /^resource / { exit } in_api { print }' infra/main.bicep)"
for expected in 'minReplicas: 1' 'maxReplicas: 12' "name: 'cpu-scaler'" "type: 'cpu'" "type: 'Utilization'" "value: '50'"; do
  printf '%s\n' "$api_scale" | grep -Fq "$expected"
done
! printf '%s\n' "$api_scale" | grep -Eq "http-scaler|concurrentRequests:"
for expected in "name: 'DB_MAX_OPEN_CONNS', value: '10'" "name: 'DB_MAX_IDLE_CONNS', value: '5'"; do
  grep -Fq "$expected" infra/main.bicep
done

python3 scripts/test-runtime-image-contract.py
python3 scripts/test-rollback-watermark-worker.py

python3 scripts/test-retire-cms-dapr.py

workflow=.github/workflows/release.yml
# Old queued releases retain production; only the replacement namespace can publish.
for restored in "$workflow" .github/workflows/content-migration.yml .github/workflows/home-v2-migration.yml; do
  ! grep -Eq '^    environment: production$' "$restored"
  grep -Fq '    environment: production-actions' "$restored"
  grep -Fq '  group: hhc-web-api-production-actions' "$restored"
done
python3 - <<'PYTEST'
from pathlib import Path
import fnmatch
import re

workflow = Path('.github/workflows/release.yml').read_text()
ignored = re.search(r'^    paths-ignore:\n(.*?)(?=^  \w|^\w)', workflow, re.M | re.S).group(1)
patterns = re.findall(r'^      - (.+)$', ignored, re.M)
def starts_release(paths):
    return any(not any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns) for path in paths)
ci_paths = ['.github/workflows/ci.yml', '.github/workflows/release.yml', 'scripts/test-release-policy.sh']
assert not starts_release(ci_paths), 'CI-only changes must not deploy the runtime'
for path in ['cmd/server/main.go', 'Dockerfile', 'Dockerfile.extractor', 'infra/main.bicep', 'go.mod', 'internal/migrations/sql/107_example.sql', 'scripts/bootstrap-migration-role.sh']:
    assert starts_release(ci_paths + [path]), f'mixed runtime changes must deploy: {path}'
assert 'workflow_dispatch:' in workflow, 'manual release remains available'
PYTEST
# Privileged image preparation is gated before login; deployment joins every gate.
release_job() {
  awk -v header="  $1:" '$0 == header { found=1; next } found && /^  [a-z_]+:/ { exit } found { print }' "$workflow"
}
configuration_job="$(release_job configuration)"
api_job="$(release_job prepare_api)"
extractor_job="$(release_job prepare_extractor)"
deploy_job="$(release_job deploy)"
load_job="$(release_job verify_extractor)"
scan_job="$(release_job scan_extractor)"
test -n "$scan_job" || { echo 'missing immutable extractor scan gate' >&2; exit 1; }
printf '%s\n' "$scan_job" | grep -Fq 'needs: prepare_extractor'
printf '%s\n' "$scan_job" | grep -Fq 'ghcr.io/aquasecurity/trivy@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969'
printf '%s\n' "$scan_job" | grep -Fq 'docker pull "$EXTRACTOR_IMAGE_REF"'
printf '%s\n' "$scan_job" | grep -Fq 'image --db-repository ghcr.io/aquasecurity/trivy-db:2 --db-repository public.ecr.aws/aquasecurity/trivy-db:2 --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 "$EXTRACTOR_IMAGE_REF"'
! printf '%s\n' "$scan_job" | grep -Fq 'continue-on-error:'
printf '%s\n' "$extractor_job" | grep -Fq -- 'docker buildx build --platform linux/amd64 --push --provenance=false -f Dockerfile.extractor'
! printf '%s\n' "$extractor_job" | grep -Eq -- '--load|docker run|docker push'
printf '%s\n' "$load_job" | grep -Fq 'needs: prepare_extractor'
printf '%s\n' "$load_job" | grep -Fq 'fail-fast: false'
printf '%s\n' "$load_job" | grep -Fq 'matrix: ${{ fromJSON(needs.prepare_extractor.outputs.matrix) }}'
printf '%s\n' "$load_job" | grep -Fq 'scripts/run_extractor_selection.py release'
printf '%s\n' "$extractor_job" | grep -Fq './scripts/prepare_extractor_tests.sh'
printf '%s\n' "$load_job" | grep -Fq 'docker pull "$EXTRACTOR_IMAGE_REF"'
printf '%s\n' "$load_job" | grep -Fq '[[ "$EXTRACTOR_BUILD_COMMIT" == "$GITHUB_SHA" ]] || exit 1'
! printf '%s\n' "$load_job" | grep -Fq 'continue-on-error:'

test -n "$configuration_job" && test -n "$api_job" && test -n "$extractor_job"
! printf '%s\n' "$configuration_job" | grep -Eq 'id-token: write|azure/login@'
printf '%s\n' "$deploy_job" | grep -Fq 'needs: [configuration, verify, prepare_api, prepare_extractor, scan_extractor, verify_extractor, verify_extractor_evidence]'
for preparation in "$api_job" "$extractor_job"; do
  printf '%s\n' "$preparation" | grep -Fq 'needs: configuration'
  printf '%s\n' "$preparation" | grep -Fq 'environment: production'
  printf '%s\n' "$preparation" | grep -Fq 'id-token: write'
  printf '%s\n' "$preparation" | grep -Fq 'docker buildx create --use --driver docker-container'
  printf '%s\n' "$preparation" | grep -Fq -- '--driver-opt image=moby/buildkit@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea'
  printf '%s\n' "$preparation" | grep -Fq -- '--platform linux/amd64'
  printf '%s\n' "$preparation" | grep -Fq -- '--cache-from "type=registry,ref=${cache_ref}"'
  printf '%s\n' "$preparation" | grep -Fq -- '--cache-to "type=registry,ref=${cache_ref},mode=max"'
  printf '%s\n' "$preparation" | grep -Fq 'commit: ${{ steps.image.outputs.commit }}'
  printf '%s\n' "$preparation" | grep -Fq 'image_digest: ${{ steps.image.outputs.image_digest }}'
done
printf '%s\n' "$deploy_job" | grep -Fq '[[ "$API_BUILD_COMMIT" == "$GITHUB_SHA" && "$EXTRACTOR_BUILD_COMMIT" == "$GITHUB_SHA" ]]'
printf '%s\n' "$deploy_job" | grep -Fq 'secrets.IMAGE_REPOSITORY, needs.prepare_api.outputs.image_digest'
printf '%s\n' "$deploy_job" | grep -Fq 'secrets.EXTRACTOR_IMAGE_REPOSITORY, needs.prepare_extractor.outputs.image_digest'
artifact_validation_line="$(printf '%s\n' "$deploy_job" | grep -n 'name: Validate prepared release artifacts' | cut -d: -f1)"
deploy_login_line="$(printf '%s\n' "$deploy_job" | grep -n 'uses: azure/login@' | cut -d: -f1)"
test "$artifact_validation_line" -lt "$deploy_login_line"
# Artifact provenance is checked before any Azure login or production mutation.
artifact_validation_body="$(printf '%s\n' "$deploy_job" | awk '
  /name: Validate prepared release artifacts/ { found=1; next }
  found && /^        run: \|$/ { capture=1; next }
  capture && (/^      - / || /^  [a-z_]+:/) { exit }
  capture { sub(/^          /, ""); print }
')"
artifact_sha=0123456789abcdef0123456789abcdef01234567
artifact_digest=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
check_prepared_artifacts() {
  expected="$1" api_commit="$2" extractor_commit="$3" image="$4" extractor="$5" ref="$6"
  if GITHUB_REF="$ref" GITHUB_SHA="$artifact_sha" GITHUB_ENV=/dev/null \
    API_BUILD_COMMIT="$api_commit" EXTRACTOR_BUILD_COMMIT="$extractor_commit" \
    IMAGE_REF="$image" WATERMARK_IMAGE_REF="${watermark_override-example.azurecr.io/example/hhc-web-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}" EXTRACTOR_IMAGE_REF="$extractor" \
    ACR_NAME=example ACR_LOGIN_SERVER=example.azurecr.io IMAGE_REPOSITORY=example/hhc-web-api \
    EXTRACTOR_IMAGE_REPOSITORY=example/hhc-web-bulletin-extractor \
    bash -euo pipefail -c "$artifact_validation_body" >/dev/null 2>&1; then status=pass; else status=fail; fi
  test "$status" = "$expected"
}
artifact_api="example.azurecr.io/example/hhc-web-api@$artifact_digest"
artifact_extractor="example.azurecr.io/example/hhc-web-bulletin-extractor@$artifact_digest"
check_prepared_artifacts pass "$artifact_sha" "$artifact_sha" "$artifact_api" "$artifact_extractor" refs/heads/main
watermark_override="$artifact_api" check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" "$artifact_api" "$artifact_extractor" refs/heads/main
watermark_override="" check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" "$artifact_api" "$artifact_extractor" refs/heads/main
check_prepared_artifacts fail other "$artifact_sha" "$artifact_api" "$artifact_extractor" refs/heads/main
check_prepared_artifacts fail "$artifact_sha" other "$artifact_api" "$artifact_extractor" refs/heads/main
check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" "$artifact_api" "$artifact_extractor" refs/heads/feature
check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" example.azurecr.io/example/hhc-web-api:latest "$artifact_extractor" refs/heads/main
check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" "$artifact_api" example.azurecr.io/example/hhc-web-bulletin-extractor@sha256:bad refs/heads/main
check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" "exampleXazurecr.io/example/hhc-web-api@$artifact_digest" "$artifact_extractor" refs/heads/main
# Reuse deployment provenance fixtures for both immutable extractor consumers.
for consumer in "$scan_job" "$load_job"; do
  validation_line="$(printf '%s\n' "$consumer" | grep -n 'name: Validate prepared extractor artifact' | cut -d: -f1)"
  login_line="$(printf '%s\n' "$consumer" | grep -n 'uses: azure/login@' | cut -d: -f1)"
  test "$validation_line" -lt "$login_line"
  artifact_validation_body="$(printf '%s\n' "$consumer" | awk '
    /name: Validate prepared extractor artifact/ { found=1; next }
    found && /^        run: \|$/ { capture=1; next }
    capture && /^      - / { exit }
    capture { sub(/^          /, ""); print }
  ')"
  test -n "$artifact_validation_body"
  check_prepared_artifacts pass "$artifact_sha" "$artifact_sha" "$artifact_api" "$artifact_extractor" refs/heads/main
  check_prepared_artifacts fail "$artifact_sha" other "$artifact_api" "$artifact_extractor" refs/heads/main
  check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" "$artifact_api" "$artifact_extractor" refs/heads/feature
  check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" "$artifact_api" example.azurecr.io/example/hhc-web-bulletin-extractor:latest refs/heads/main
  check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" "$artifact_api" example.azurecr.io/example/hhc-web-bulletin-extractor@sha256:bad refs/heads/main
  check_prepared_artifacts fail "$artifact_sha" "$artifact_sha" "$artifact_api" "exampleXazurecr.io/example/hhc-web-bulletin-extractor@$artifact_digest" refs/heads/main
done
for preparation in "$api_job" "$extractor_job"; do
  printf '%s\n' "$preparation" | grep -Fq -- '--build-arg APT_CACHE_DATE="$(date -u +%F)"'
  ! printf '%s\n' "$preparation" | grep -Fq 'continue-on-error:'
done
! printf '%s\n' "$deploy_job" | grep -Fq 'if: ${{ always() }}'
test "$(grep -Fc 'ARG APT_CACHE_DATE' Dockerfile)" -eq 1
test "$(grep -Fc 'ARG APT_CACHE_DATE' Dockerfile.extractor)" -eq 2
extractor_runtime="$(sed -n '/^FROM .* AS extractor$/,$p' Dockerfile.extractor)"
printf '%s\n' "$extractor_runtime" | grep -Fq 'ARG APT_CACHE_DATE'
printf '%s\n' "$extractor_runtime" | grep -Fq 'apt-get -o Acquire::Retries=0 update'
extractor_apt_patch="$(printf '%s\n' "$extractor_runtime" | grep -nF 'apt-get -o Acquire::Retries=0 install -y --no-install-recommends apt=3.0.4 libapt-pkg7.0=3.0.4' | cut -d: -f1)"
extractor_dependency_install="$(printf '%s\n' "$extractor_runtime" | grep -nF 'apt-get update && apt-get install' | cut -d: -f1)"
test "$extractor_apt_patch" -lt "$extractor_dependency_install"
extractor_runtime_install="$(printf '%s\n' "$extractor_runtime" | grep -F 'apt-get update && apt-get install')"
for package in libexpat1=2.9.0-1~deb13u1 poppler-utils=25.03.0-5+deb13u4 python3=3.13.5-1 libbrotli1=1.1.0-2+deb13u1; do
  printf '%s\n' "$extractor_runtime_install" | grep -Fwq -- "$package" || {
    echo "extractor runtime must explicitly install $package" >&2
    exit 1
  }
done
# Production must build the same reviewed renderer artifact as the CI image.
ci_renderer_digest="$(awk -F 'BULLETIN_RENDERER_ARTIFACT_SHA256=' 'NF > 1 { sub(/[[:space:]].*$/, "", $2); print $2 }' .github/workflows/ci.yml)"
release_renderer_digest="$(awk '$1 == "BULLETIN_RENDERER_ARTIFACT_SHA256:" { print $2 }' "$workflow")"
if [ "${#ci_renderer_digest}" -ne 64 ] || [ "$release_renderer_digest" != "$ci_renderer_digest" ]; then
  echo 'release renderer artifact must match the CI extractor image' >&2
  exit 1
fi
verify_job="$(sed -n '/^  verify:/,/^  prepare_api:/p' "$workflow")"
printf '%s\n' "$verify_job" | grep -Fq 'fetch-depth: 0'
printf '%s\n' "$verify_job" | grep -Fq 'working-directory: tools/bulletin-conversion'
printf '%s\n' "$verify_job" | grep -Fq 'npm ci --ignore-scripts --no-audit --no-fund && npm test'
# Registry authentication precedes image builds and immutable-image load tests.
extractor_build="$extractor_job"
registry_login_line="$(printf '%s\n' "$extractor_build" | grep -n 'az acr login' | cut -d: -f1)"
image_build_line="$(printf '%s\n' "$extractor_build" | grep -n 'docker buildx build --platform linux/amd64 --push --provenance=false -f Dockerfile.extractor' | cut -d: -f1)"
load_login_line="$(printf '%s\n' "$load_job" | grep -n 'az acr login' | cut -d: -f1)"
load_pull_line="$(printf '%s\n' "$load_job" | grep -n 'docker pull' | cut -d: -f1)"
load_gate_line="$(printf '%s\n' "$load_job" | grep -n 'scripts/run_extractor_selection.py release' | cut -d: -f1)"
test -n "$registry_login_line" && test -n "$image_build_line" && test -n "$load_gate_line"
test "$registry_login_line" -lt "$image_build_line"
test "$load_login_line" -lt "$load_pull_line"
test "$load_pull_line" -lt "$load_gate_line"
import_workflow=.github/workflows/content-migration.yml
home_v2_workflow=.github/workflows/home-v2-migration.yml

test ! -e azure-pipelines.yml
test -f "$home_v2_workflow"
grep -q 'workflow_dispatch:' "$home_v2_workflow"
grep -q 'options: \[plan, apply\]' "$home_v2_workflow"
grep -q 'group: hhc-web-api-production' "$home_v2_workflow"
grep -Fq '[[ "$GITHUB_REF" == refs/heads/main ]]' "$home_v2_workflow"
grep -Fq '[[ "$RUN_ATTEMPT" == 1 ]]' "$home_v2_workflow"
test "$(grep -Fc 'environment: production' "$home_v2_workflow")" -eq 2
test "$(grep -Fc 'actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1' "$home_v2_workflow")" -eq 2
test "$(grep -Fc 'azure/login@532459ea530d8321f2fb9bb10d1e0bcf23869a43' "$home_v2_workflow")" -eq 2
grep -Fq '/hhc-web-home-v2-migrate' "$home_v2_workflow"
grep -Fq '[[ "$runtime_image" == "$image_ref" ]]' "$home_v2_workflow"
grep -Fq '[[ "$image_ref" == "$REVIEWED_IMAGE_REF" && "$runtime_image" == "$REVIEWED_IMAGE_REF" ]]' "$home_v2_workflow"
grep -Fq -- '--expected-source-sha=\($source)' "$home_v2_workflow"
grep -Fq -- '--expected-plan-sha=\($plan)' "$home_v2_workflow"
grep -Fq '.updates==1 and .inserts==0 and .deletes==0 and .warnings==0 and .conflicts==0' "$home_v2_workflow"
grep -Fq 'go build -trimpath -ldflags="-s -w" -o /hhc-web-home-v2-migrate ./cmd/home-v2-migrate' Dockerfile
grep -Fq 'COPY --from=api-build /hhc-web-home-v2-migrate /hhc-web-home-v2-migrate' Dockerfile
grep -q 'workflow_dispatch:' "$workflow"
grep -q '^  push:' "$workflow"
grep -q 'branches: \[main\]' "$workflow"
expected_paths_ignore='docs/**
openapi.yaml
openapi_test.go
.github/workflows/ci.yml
.github/workflows/release.yml
.github/workflows/locations-alias.yml
scripts/deploy-cms-locations-alias.py
scripts/cms-locations-release.sh
scripts/test-cms-locations-release.sh
scripts/retire_cms_dapr.py
scripts/test-retire-cms-dapr.py
scripts/test-release-policy.sh
README.md
**/*_test.go
tools/bulletin_v4/**
infra/bulletin-v4.bicep
.github/workflows/bulletin-v4.yml
**/*.md'
actual_paths_ignore="$(awk '
  $0 == "    paths-ignore:" { in_paths_ignore = 1; next }
  in_paths_ignore && /^      - / { sub(/^      - /, ""); gsub(/\047/, ""); print; next }
  in_paths_ignore { exit }
' "$workflow")"
if [ "$actual_paths_ignore" != "$expected_paths_ignore" ]; then
  echo 'release non-runtime paths-ignore policy mismatch' >&2
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
grep -Fq 'RUNTIME_CPU: "0.25"' "$workflow"
grep -Fq 'RUNTIME_MEMORY: 0.5Gi' "$workflow"
test "$(grep -Fc 'runtimeCpu="$RUNTIME_CPU" runtimeMemory="$RUNTIME_MEMORY"' "$workflow")" -eq 5
# Exercise the deployed-revision predicate, including Azure's decimal CPU format.
revision_predicate="$(grep -F '[[ "$image" == "$IMAGE_REF"' "$workflow" | sed 's/^[[:space:]]*if //; s/; then$//')"
check_revision() {
  image="$1" cpu="$2" memory="$3" auto_enabled="${4:-true}" \
    IMAGE_REF=expected RUNTIME_CPU=0.25 RUNTIME_MEMORY=0.5Gi BULLETIN_AUTO_EXTRACTION_ENABLED=true \
    bash -c "$revision_predicate" 2>/dev/null
}
check_revision expected 0.25 0.5Gi
check_revision expected 0.250 0.5Gi
! check_revision expected 0.5 0.5Gi
! check_revision expected invalid 0.5Gi
! check_revision wrong 0.25 0.5Gi
! check_revision expected 0.25 1Gi
if check_revision expected 0.25 0.5Gi false; then
  echo 'ready revision accepted a mismatched automatic extraction flag' >&2
  exit 1
fi
grep -q 'python3 scripts/restore-runtime-template.py' "$workflow"
grep -Fq '"$PREVIOUS_IMAGE_REF"' "$workflow"
grep -q 'Verify rolled back runtime' "$workflow"
grep -q '/health/live' infra/main.bicep
grep -q '/health/ready' infra/main.bicep
grep -q "type: 'Startup'" infra/main.bicep
grep -q "name: 'hhc-web-migrate-identity'" infra/main.bicep
grep -q "param runtimeKeyVaultName string$" infra/main.bicep
grep -q "param migrationKeyVaultName string$" infra/main.bicep
grep -q 'retainLegacyRuntimeSecret bool = false' infra/main.bicep
grep -q "name: 'database-url-v2'" infra/main.bicep
grep -Fq -- '--query properties.enableRbacAuthorization' "$workflow"
grep -q "workloadProfileName: 'Consumption'" infra/main.bicep
test "$(grep -Fc 'cooldownPeriod: 300' infra/main.bicep)" -eq 2
test "$(grep -Fc "name: 'AZURE_CLIENT_ID', value: apiIdentity.properties.clientId" infra/main.bicep)" -eq 3
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
grep -Fq 'AZURE_OPENAI_RAI_POLICY: "hhc-cms-translation-v1"' "$workflow"
grep -Fq 'az rest --method get' "$workflow"
grep -Fq 'raiPolicies/$AZURE_OPENAI_RAI_POLICY?api-version=2024-10-01' "$workflow"
grep -Fq '. as $policy' "$workflow"
grep -Fq '$policy.properties.contentFilters[]' "$workflow"
grep -Fq 'var translationConfigured = !empty(azureOpenAIEndpoint) && !empty(azureOpenAIDeployment)' infra/main.bicep
test "$(grep -Fc 'translationConfigured ? [' infra/main.bicep)" -eq 3
test "$(grep -Fc 'cmsTranslationEnabled ? [' infra/main.bicep)" -eq 0
grep -Fq "{ name: 'CMS_TRANSLATION_ENABLED', value: cmsTranslationEnabled ? 'true' : 'false' }" infra/main.bicep
grep -Fq "keyVaultUrl: '\${runtimeVault.properties.vaultUri}secrets/hhc-web-azure-openai-api-key'" infra/main.bicep
grep -Fq 'translation_configured=false' "$workflow"
grep -Fq 'if [[ -n "$AZURE_OPENAI_ENDPOINT" || -n "$AZURE_OPENAI_DEPLOYMENT" ]]; then' "$workflow"
grep -Fq 'test -n "$AZURE_OPENAI_ENDPOINT" && test -n "$AZURE_OPENAI_DEPLOYMENT"' "$workflow"
grep -Fq 'if [[ "$CMS_TRANSLATION_ENABLED" == "true" && "$translation_configured" != "true" ]]; then' "$workflow"
grep -Fq 'if [[ -n "$AZURE_OPENAI_ENDPOINT" && -n "$AZURE_OPENAI_DEPLOYMENT" ]]; then' "$workflow"
grep -Fq 'az keyvault secret show --vault-name "$RUNTIME_KEY_VAULT_NAME"' "$workflow"
grep -Fq -- '--name hhc-web-azure-openai-api-key --query name --output tsv --only-show-errors' "$workflow"
grep -Fq '[[ "$translation_secret_name" == "hhc-web-azure-openai-api-key" ]]' "$workflow"
grep -Fq "param videoMediaBaseURL string = ''" infra/main.bicep
grep -Fq "name: 'VIDEO_MEDIA_BASE_URL', value: videoMediaBaseURL" infra/main.bicep
grep -Fq "name: 'VIDEO_WATERMARK_KEY_HEX', secretRef: 'video-watermark-key'" infra/main.bicep
grep -Fq "keyVaultUrl: '\${runtimeVault.properties.vaultUri}secrets/member-video-watermark-key-hex'" infra/main.bicep
grep -Fq 'VIDEO_MEDIA_BASE_URL: ${{ secrets.VIDEO_MEDIA_BASE_URL }}' "$workflow"
grep -Fq '[[ "$VIDEO_MEDIA_BASE_URL" == "https://media.alive.org.tw" ]]' "$workflow"
grep -Fq -- '--name member-video-watermark-key-hex --query name --output tsv --only-show-errors' "$workflow"
test "$(grep -Fc 'videoMediaBaseURL="$VIDEO_MEDIA_BASE_URL"' "$workflow")" -eq 4
test "$(grep -Fc 'cmsTranslationEnabled="$CMS_TRANSLATION_ENABLED" azureOpenAIEndpoint="$AZURE_OPENAI_ENDPOINT" azureOpenAIDeployment="$AZURE_OPENAI_DEPLOYMENT"' "$workflow")" -eq 4
grep -q 'test-migration-policy-test.sh' .github/workflows/ci.yml
grep -q 'test-what-if-policy.sh' .github/workflows/ci.yml
grep -Fq 'test-migration-policy.sh internal/migrations/sql/*.sql' .github/workflows/ci.yml
grep -Fq 'test-migration-policy.sh internal/migrations/sql/*.sql' "$workflow"
grep -Fq 'npx --yes @redocly/cli@2.47.0 lint openapi.yaml' .github/workflows/ci.yml
grep -Fq './scripts/test-release-policy.sh' .github/workflows/ci.yml
for job in changes verify_policy verify_go prepare_extractor verify_extractor verify_image; do
  grep -Fq "  $job:" .github/workflows/ci.yml
done
grep -Fq 'needs: [changes, verify_policy, verify_go, prepare_extractor, verify_extractor, verify_image]' .github/workflows/ci.yml
grep -Fq 'test "${{ needs.changes.result }}" = success' .github/workflows/ci.yml
grep -Fq 'test "${{ needs.verify_policy.result }}" = success' .github/workflows/ci.yml
grep -Fq 'test "${{ needs.verify_go.result }}" = success' .github/workflows/ci.yml
grep -Fq 'test "${{ needs.prepare_extractor.result }}" = success' .github/workflows/ci.yml
grep -Fq 'test "${{ needs.prepare_extractor.result }}" = skipped' .github/workflows/ci.yml
grep -Fq 'test "${{ needs.verify_extractor.result }}" = success' .github/workflows/ci.yml
grep -Fq 'test "${{ needs.verify_image.result }}" = success' .github/workflows/ci.yml
scanner='ghcr.io/aquasecurity/trivy@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969'
test "$(grep -Fc "$scanner" .github/workflows/ci.yml)" -eq 2
test "$(grep -Fc "$scanner" "$workflow")" -eq 3
python3 scripts/test-trivy-report.py
grep -Fq 'fs --db-repository ghcr.io/aquasecurity/trivy-db:2 --db-repository public.ecr.aws/aquasecurity/trivy-db:2 --scanners vuln --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1' .github/workflows/ci.yml
grep -Fq 'fs --db-repository ghcr.io/aquasecurity/trivy-db:2 --db-repository public.ecr.aws/aquasecurity/trivy-db:2 --scanners vuln --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1' "$workflow"
grep -Fq 'image --db-repository ghcr.io/aquasecurity/trivy-db:2 --db-repository public.ecr.aws/aquasecurity/trivy-db:2 --severity HIGH,CRITICAL --ignore-unfixed --exit-code 1 --vex /vex/lightning-2.6.6.openvex.json --show-suppressed "$image"' .github/workflows/ci.yml
grep -Fq 'docker pull "$IMAGE_REF"' "$workflow"
grep -Fq 'sh scripts/scan-image.sh "$verified_image"' "$workflow"
# The reviewed VEX must remain exact and visible in every image scan; source scans stay unfiltered.
test "$(grep -Fc -- '-v "$PWD/.vex:/vex:ro"' .github/workflows/ci.yml)" -eq 1
test "$(grep -Fc -- '-v "$PWD/.vex:/vex:ro"' scripts/scan-image.sh)" -eq 1
test "$(grep -Fc -- '--vex /vex/lightning-2.6.6.openvex.json --show-suppressed' .github/workflows/ci.yml)" -eq 1
test "$(grep -Fc -- '--vex /vex/lightning-2.6.6.openvex.json --show-suppressed' scripts/scan-image.sh)" -eq 1
grep -Fq -- '--format json --vex /vex/lightning-2.6.6.openvex.json --show-suppressed' scripts/scan-image.sh
release_pull_line="$(grep -nF 'docker pull "$IMAGE_REF"' "$workflow" | cut -d: -f1)"
release_guard_line="$(grep -nF -- '-m unittest discover -s tools/bulletin_watermark -p '\''test_*.py'\''' "$workflow" | cut -d: -f1)"
release_gate_line="$(grep -nF 'sh scripts/scan-image.sh "$verified_image"' "$workflow" | cut -d: -f1)"
grep -Fq -- '--entrypoint /opt/watermark/bin/python' "$workflow"
grep -Fq -- '-v "$PWD:/workspace:ro" -w /workspace "$WATERMARK_IMAGE_REF"' "$workflow"
grep -Fq 'HHC_WATERMARK_PDF_TESTS=1 -e HHC_WATERMARK_PDFTOPPM=/usr/bin/pdftoppm' "$workflow"
release_verify="$(awk '/^  verify:/{found=1; next} found && /^  prepare_api:/{exit} found{print}' "$workflow")"
! printf '%s\n' "$release_verify" | grep -Fq 'docker build -t hhc-web-api:verify .'
! printf '%s\n' "$release_verify" | grep -Fq 'hhc-web-api:verify'
ci_policy_job="$(awk '/^  verify_policy:/{found=1; next} found && /^  verify_go:/{exit} found{print}' .github/workflows/ci.yml)"
ci_go_job="$(awk '/^  verify_go:/{found=1; next} found && /^  verify_image:/{exit} found{print}' .github/workflows/ci.yml)"
! printf '%s\n' "$ci_policy_job" | grep -Fq './scripts/bootstrap-migration-role.test.sh'
printf '%s\n' "$ci_go_job" | grep -Fq './scripts/bootstrap-migration-role.test.sh'
test "$release_pull_line" -lt "$release_guard_line"
test "$release_guard_line" -lt "$release_gate_line"
release_scan_line="$(grep -nF 'name: Test and scan immutable image' "$workflow" | cut -d: -f1)"
release_what_if_line="$(grep -nF 'name: Reject destructive infrastructure changes' "$workflow" | cut -d: -f1)"
test "$release_scan_line" -lt "$release_what_if_line"
grep -Fq 'SITE_LAYOUT_SMOKE_URL: https://www.alive.org.tw/api/site-layout?locale=zh-Hant' "$workflow"
grep -Fq 'PAGE_SMOKE_BASE_URL: https://www.alive.org.tw/api/pages' "$workflow"
grep -Fq 'for locale in zh-Hant zh-Hans en ja ko; do' scripts/smoke-release.sh
grep -Fq 'Home Video order differs for $locale' scripts/smoke-release.sh
grep -Fq 'Home Video order changed across repeated zh-Hant reads' scripts/smoke-release.sh

run_smoke_case() {
  mode="$1"
  location_status="$2"
  location_content_type="$3"
  location_body="$4"
  expected="$5"
  expected_location_request="$6"
  site_layout_status="${7:-404}"
  site_layout_content_type="${8:-application/json}"
  site_layout_body='{"data":null,"meta":{},"error":{"code":"not_found","message":"The site settings were not found."}}'
  [ "$#" -lt 9 ] || site_layout_body="$9"
  expected_site_layout_request="${10:-unchecked}"
  site_layout_etag="${11:-}"
  page_status="${12:-404}"
  page_content_type="${13:-application/json}"
  page_body='{"data":null,"meta":{},"error":{"code":"not_found","message":"The content was not found."}}'
  [ "$#" -lt 14 ] || page_body="${14}"
  expected_page_request="${15:-unchecked}"
  page_etag='"page-3"'
  [ "$#" -lt 16 ] || page_etag="${16}"
  page_cache_control='public, max-age=30, must-revalidate'
  [ "$#" -lt 17 ] || page_cache_control="${17}"
  home_contract_mode="${18:-dual}"
  home_template="${19:-home.v1}"
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
  */api/admin/bulletins) printf 401 ;;
  */api/home*)
    printf '%s\n' '{"data":{"news":[],"videos":[{"id":"video-a"},{"id":"video-b"}]},"meta":{},"error":null}'
    ;;
  */api/locations*)
    printf 'HTTP/1.1 %s\nContent-Type: %s\n\n' "$SMOKE_LOCATION_STATUS" "$SMOKE_LOCATION_CONTENT_TYPE" >"$headers"
    printf '%s\n' "$SMOKE_LOCATION_BODY" >"$body"
    printf '%s' "$SMOKE_LOCATION_STATUS"
    ;;
  */api/site-layout*)
    printf 'HTTP/1.1 %s\nContent-Type: %s\n' "$SMOKE_SITE_LAYOUT_STATUS" "$SMOKE_SITE_LAYOUT_CONTENT_TYPE" >"$headers"
    [ -z "$SMOKE_SITE_LAYOUT_ETAG" ] || printf 'ETag: %s\n' "$SMOKE_SITE_LAYOUT_ETAG" >>"$headers"
    printf '\n' >>"$headers"
    printf '%s\n' "$SMOKE_SITE_LAYOUT_BODY" >"$body"
    printf '%s' "$SMOKE_SITE_LAYOUT_STATUS"
    ;;
  */api/pages/*)
    printf 'HTTP/1.1 %s\nContent-Type: %s\n' "$SMOKE_PAGE_STATUS" "$SMOKE_PAGE_CONTENT_TYPE" >"$headers"
    [ -z "$SMOKE_PAGE_ETAG" ] || printf 'ETag: %s\n' "$SMOKE_PAGE_ETAG" >>"$headers"
    [ -z "$SMOKE_PAGE_CACHE_CONTROL" ] || printf 'Cache-Control: %s\n' "$SMOKE_PAGE_CACHE_CONTROL" >>"$headers"
    printf '\n' >>"$headers"
    page_key="${url%%\?*}"
    page_key="${page_key##*/}"
    case "$page_key" in
      home)
        route=/
        if [ "$SMOKE_HOME_TEMPLATE" = home.v2 ]; then
          template=home.v2; schema_version=2
          page_data='{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Joy","aboutDescription":"About us","bannerImageUrl":"/api/assets/banner","links":{"churchYoutube":"https://youtube.com/@hhc33","churchFacebook":"https://facebook.com/hhc","musicYoutube":"https://youtube.com/@music"},"locations":[{"key":"taipei","name":"Taipei","address":"Taipei address","mapHref":"https://maps.example.com/taipei","sortOrder":10}]}'
        else
          template=home.v1; schema_version=1
          page_data='{"heroTitle":"Home","heroSubtitle":"Welcome","newsTitle":"News","moreNews":"More","weeklyTitle":"Weekly","downloadWeekly":"Download","videosTitle":"Videos","videosSubtitle":"Music","watchMore":"Watch","aboutTitle":"About","aboutBody":"About us","aboutCta":"Meet us","locationsTitle":"Locations","mapLink":"Map"}'
        fi
        ;;
      about)
        template=about.v1; schema_version=1; route=/about
        page_data='{"heroTitle":"About","heroSubtitle":"Mission","vision":{"intro":"Intro","imageAlt":"Image","actionsImageAlt":"Actions","sections":[{"eyebrow":"One","title":"Vision","body":"Body"},{"eyebrow":"Two","title":"Goals","body":"Body"},{"eyebrow":"Three","title":"Actions","cards":[{"title":"Share","body":"Body"}]},{"eyebrow":"Four","title":"Convictions","cards":[{"title":"Mission","body":"Body"}]}]},"history":{"scripture":[{"lines":["Verse"],"cite":"Isaiah"}],"imageAlt":"Image","intro":"History","title":"Church History"}}'
        ;;
      privacy-policy) template=legal.v1; schema_version=1; route=/privacy-policy; page_data='{"heroTitle":"Privacy","heroSubtitle":"","updatedAtLabel":"Updated","updatedAt":"August 10, 2026","intro":"Intro","sections":[{"title":"Scope","body":["Paragraph"]}]}' ;;
      terms-of-use) template=legal.v1; schema_version=1; route=/terms-of-use; page_data='{"heroTitle":"Terms","heroSubtitle":"","updatedAtLabel":"Updated","updatedAt":"August 10, 2026","intro":"Intro","sections":[{"title":"Scope","body":["Paragraph"]}]}' ;;
    esac
    if [ "$SMOKE_PAGE_BODY" = __VALID_PAGE__ ]; then
      printf '{"data":{"pageKey":"%s","template":"%s","routePath":"%s","indexable":true,"content":{"schemaVersion":%s,"template":"%s","data":%s},"resolvedLocale":"zh-Hant","availableLocales":["zh-Hant","zh-Hans","en","ja","ko"],"version":3,"publishedAt":"2026-08-29T00:00:00Z"},"meta":{},"error":null}\n' "$page_key" "$template" "$route" "$schema_version" "$template" "$page_data" >"$body"
    else
      printf '%s\n' "$SMOKE_PAGE_BODY" | sed "s|__PAGE_KEY__|$page_key|g; s|__TEMPLATE__|$template|g; s|__ROUTE__|$route|g" >"$body"
    fi
    printf '%s' "$SMOKE_PAGE_STATUS"
    ;;
esac
SH
  chmod +x "$case_dir/bin/timeout" "$case_dir/bin/script" "$case_dir/bin/curl"
  set +e
  PATH="$case_dir/bin:$PATH" SMOKE_MODE="$mode" SMOKE_EVENTS="$case_dir/events" \
    SMOKE_LOCATION_STATUS="$location_status" SMOKE_LOCATION_CONTENT_TYPE="$location_content_type" \
    SMOKE_LOCATION_BODY="$location_body" SMOKE_SITE_LAYOUT_STATUS="$site_layout_status" \
    SMOKE_SITE_LAYOUT_CONTENT_TYPE="$site_layout_content_type" SMOKE_SITE_LAYOUT_BODY="$site_layout_body" \
    SMOKE_SITE_LAYOUT_ETAG="$site_layout_etag" \
    SMOKE_PAGE_STATUS="$page_status" SMOKE_PAGE_CONTENT_TYPE="$page_content_type" SMOKE_PAGE_BODY="$page_body" \
    SMOKE_PAGE_ETAG="$page_etag" SMOKE_PAGE_CACHE_CONTROL="$page_cache_control" \
    HOME_PAGE_CONTRACT_MODE="$home_contract_mode" SMOKE_HOME_TEMPLATE="$home_template" \
    ./scripts/smoke-release.sh >/dev/null 2>&1
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
  case "$expected_site_layout_request" in
    requested) grep -Eq '/api/site-layout([?]|$)' "$case_dir/events" ;;
    skipped) ! grep -Eq '/api/site-layout([?]|$)' "$case_dir/events" 2>/dev/null ;;
  esac
  case "$expected_page_request" in
    requested)
      for page_key in home about privacy-policy terms-of-use; do
        grep -Eq "/api/pages/$page_key[?]locale=zh-Hant$" "$case_dir/events"
      done
      test "$(grep -Ec '/api/pages/(home|about|privacy-policy|terms-of-use)[?]locale=zh-Hant$' "$case_dir/events")" -eq 4
      ;;
    started) grep -Eq '/api/pages/home[?]locale=zh-Hant$' "$case_dir/events" ;;
    skipped) ! grep -Eq '/api/pages/' "$case_dir/events" 2>/dev/null ;;
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
run_smoke_case forward 200 application/jsonp '{"data":[],"meta":{},"error":null}' fail requested
run_smoke_case forward 200 application/json-seq '{"data":[],"meta":{},"error":null}' fail requested
run_smoke_case forward 200 ' Application/JSON ; charset=utf-8 ' '{"data":[],"meta":{},"error":null}' pass requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":{"code":"unexpected"}}' fail requested
run_smoke_case rollback 404 application/json '{"data":null,"meta":{},"error":{"code":"not_found"}}' pass skipped

valid_site_layout='{"data":{"locale":"zh-Hant","siteName":"哈利路亞家教會","englishName":"Hallelujah Home Church","copyrightHolder":"Hallelujah Home Church","allRightsReserved":"版權所有","seoTitleSuffix":"HHC","seoDescriptionFallback":"教會網站","header":[{"key":"about","label":"關於我們","href":"/zh-Hant/about","visible":true},{"key":"news","label":"最新消息","href":"/zh-Hant/news","visible":true},{"key":"literature-ministry","label":"文字事工","href":"/zh-Hant/literature-ministry","visible":true}],"legal":[{"key":"privacy-policy","label":"隱私權政策","href":"/zh-Hant/privacy-policy","visible":true},{"key":"terms-of-use","label":"使用條款","href":"/zh-Hant/terms-of-use","visible":true}],"links":{"churchYoutube":"https://youtube.com/@hhc33","churchFacebook":"https://facebook.com/hhc","musicYoutube":"https://youtube.com/@music"},"version":1,"publishedAt":"2026-08-29T00:00:00Z"},"meta":{},"error":null}'
backend_site_layout_not_found='{"data":null,"meta":{},"error":{"code":"not_found","message":"The site settings were not found."}}'
hidden_site_layout="$(printf '%s\n' "$valid_site_layout" | jq -c '.data.header[0].visible = false | .data.header[0].label = "" | .data.legal[0].visible = false | .data.legal[0].label = ""')"
reordered_site_layout="$(printf '%s\n' "$valid_site_layout" | jq -c '.data.header |= reverse | .data.legal |= reverse')"
missing_site_layout="$(printf '%s\n' "$valid_site_layout" | jq -c 'del(.data.legal[1])')"
duplicate_site_layout="$(printf '%s\n' "$valid_site_layout" | jq -c '.data.header[2] = .data.header[0]')"
visible_empty_site_layout="$(printf '%s\n' "$valid_site_layout" | jq -c '.data.legal[0].label = ""')"
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' pass requested 200 application/json "$valid_site_layout" requested '"site-layout-1"'
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' pass requested 200 application/json "$hidden_site_layout" requested '"site-layout-1"'
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' pass requested 200 application/json "$reordered_site_layout" requested '"site-layout-1"'
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-2"'
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' pass requested 404 application/json "$backend_site_layout_not_found" requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 404 application/json '{"error":"Not Found","message":"The requested resource was not found.","service":"api-gateway"}' requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 404 text/plain '' requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json '{"data":{},"meta":{},"error":null}' requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$missing_site_layout" requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$duplicate_site_layout" requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$visible_empty_site_layout" requested
run_smoke_case rollback 404 application/json '{"data":null,"meta":{},"error":{"code":"not_found"}}' pass skipped 404 application/json "$backend_site_layout_not_found" skipped

valid_page=__VALID_PAGE__
backend_page_not_found='{"data":null,"meta":{},"error":{"code":"not_found","message":"The content was not found."}}'
gateway_page_not_found='{"error":"Not Found","message":"The requested resource was not found.","service":"api-gateway"}'
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' pass requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 200 application/json "$valid_page" requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' pass requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 200 application/json "$valid_page" requested '"page-3"' 'public, max-age=30, must-revalidate' v2-only home.v2
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 200 application/json "$valid_page" started '"page-3"' 'public, max-age=30, must-revalidate' v2-only home.v1
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' pass requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 404 application/json "$backend_page_not_found" requested
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 404 application/json "$gateway_page_not_found" started
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 404 text/plain '' started
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 200 application/json '{"data":{},"meta":{},"error":null}' started
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 200 application/json '' started
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 200 application/json "$valid_page" started '' 'public, max-age=30, must-revalidate'
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 200 application/json "$valid_page" started 'W/"page-3"' 'public, max-age=30, must-revalidate'
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 200 application/json "$valid_page" started '"page-3"' ''
run_smoke_case forward 200 application/json '{"data":[],"meta":{},"error":null}' fail requested 200 application/json "$valid_site_layout" requested '"site-layout-1"' 200 application/json "$valid_page" started '"page-3"' 'public, max-age=30'
run_smoke_case rollback 404 application/json '{"data":null,"meta":{},"error":{"code":"not_found"}}' pass skipped 404 application/json "$backend_site_layout_not_found" skipped '' 404 application/json "$backend_page_not_found" skipped

forward_smoke_line="$(grep -nF 'SMOKE_MODE=forward ./scripts/smoke-release.sh' "$workflow" | cut -d: -f1)"
pointer_upload_line="$(grep -nF -- '--name current.json' "$workflow" | tail -1 | cut -d: -f1)"
test -n "$forward_smoke_line"
test -n "$pointer_upload_line"
for line in $forward_smoke_line; do test "$line" -lt "$pointer_upload_line"; done
grep -A2 '^  publish_openapi:' "$workflow" | grep -Fq 'needs: public_acceptance'

test -f "$import_workflow" || {
  echo 'missing manual content migration workflow' >&2
  exit 1
}
release_concurrency_group="$(awk '$0 == "concurrency:" { found = 1; next } found && /^  group:/ { print $2; exit }' "$workflow")"
import_concurrency_group="$(awk '$0 == "concurrency:" { found = 1; next } found && /^  group:/ { print $2; exit }' "$import_workflow")"
test -n "$release_concurrency_group"
test "$import_concurrency_group" = "$release_concurrency_group"

content_import_env="$(sed -n '/^var hhcWebContentImportEnvironment =/,/^$/p' infra/main.bicep)"
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
printf '%s\n' "$content_import_job" | grep -Fq "keyVaultUrl: '\${runtimeVault.properties.vaultUri}secrets/jobs-database-url'"
printf '%s\n' "$content_import_job" | grep -Fq "name: 'content-import'"
printf '%s\n' "$content_import_job" | grep -Fq 'image: runtimeImage'
printf '%s\n' "$content_import_job" | grep -Fq "command: ['/hhc-web-content-import']"
printf '%s\n' "$content_import_job" | grep -Fq "args: ['--mode=inventory']"
printf '%s\n' "$content_import_job" | grep -Fq 'filter(hhcWebContentImportEnvironment,'
printf '%s\n' "$content_import_env" | grep -Fq "{ name: 'DATABASE_URL', secretRef: 'database-url' }"
if printf '%s\n%s\n' "$content_import_job" "$content_import_env" | grep -Eq 'migrateIdentity|migrationVault'; then
  echo 'content import job must use only the runtime identity and vault' >&2
  exit 1
fi

test "$(grep -Fc 'az containerapp job start' "$workflow")" -eq 2
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
  if AZURE_CLIENT_ID=test AZURE_TENANT_ID=test AZURE_SUBSCRIPTION_ID=11111111-1111-4111-8111-111111111111 \
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
# Private target settings and exact environment OIDC also fence read-only plans.
printf '%s\n' "$preflight_job" | grep -Fq 'environment: production-actions'
printf '%s\n' "$preflight_job" | grep -Fq 'MODE=plan'
printf '%s\n' "$preflight_job" | grep -Fq 'inputs.seed_version'
printf '%s\n' "$preflight_job" | grep -Fq 'inputs.manifest_sha'
printf '%s\n' "$apply_job" | grep -Fq 'needs: preflight'
printf '%s\n' "$apply_job" | grep -Fq "if: \${{ inputs.mode == 'apply' && github.run_attempt == 1 }}"
printf '%s\n' "$apply_job" | grep -Fq 'environment: production'
test "$(grep -Fc 'environment: production' "$import_workflow")" -eq 2
printf '%s\n' "$apply_job" | grep -Fq 'needs.preflight.outputs.image_digest'
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

image_a=example.azurecr.io/example/hhc-web-api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
image_b=example.azurecr.io/example/hhc-web-api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
for job in "$preflight_job" "$apply_job"; do
  image_guard="$(extract_workflow_function require_same_image "$job")"
  test -n "$image_guard"
  run_image_match_case "$image_guard" "$image_a" "$image_a" pass
  foreign="foreign.example/other@${image_a##*@}"
  run_image_match_case "$image_guard" "$foreign" "$foreign" fail
  foreign="example.azurecr.io/example/other@${image_a##*@}"
  run_image_match_case "$image_guard" "$foreign" "$foreign" fail
  run_image_match_case "$image_guard" "$image_a" "$image_b" fail
  run_image_match_case "$image_guard" example.azurecr.io/example/hhc-web-api:main "$image_a" fail

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
grep -q 'image_digest: ${{ steps.release_outputs.outputs.image_digest }}' "$workflow"

publish_job="$(release_job publish_openapi)"
printf '%s\n' "$publish_job" | grep -q 'needs: public_acceptance'
printf '%s\n' "$publish_job" | grep -q 'environment: production'
printf '%s\n' "$publish_job" | grep -q 'contents: read'
printf '%s\n' "$publish_job" | grep -q 'id-token: write'
printf '%s\n' "$publish_job" | grep -q 'API_DOCS_AZURE_CLIENT_ID'
printf '%s\n' "$publish_job" | grep -q 'api-docs-hhc-web-api'
printf '%s\n' "$publish_job" | grep -Fq 'STORAGE_ACCOUNT: ${{ secrets.STORAGE_ACCOUNT }}'
printf '%s\n' "$publish_job" | grep -q 'needs.public_acceptance.outputs.commit'
printf '%s\n' "$publish_job" | grep -q 'needs.public_acceptance.outputs.image_digest'
printf '%s\n' "$publish_job" | grep -q 'specs/${RELEASE_COMMIT}/openapi.yaml'
printf '%s\n' "$publish_job" | grep -q 'inputs.fail_openapi_before_pointer && github.run_attempt == 1'
printf '%s\n' "$publish_job" | grep -q -- '--overwrite false'
printf '%s\n' "$publish_job" | grep -q -- '--name current.json'
printf '%s\n' "$publish_job" | grep -q -- '--overwrite true'

workflow_body="$(printf '%s\n' "$publish_job" | sed -n '/^          spec_blob="specs\//,$p' | sed 's/^          //')"
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

  if output="$(POINTER_CASE_DIR="$case_dir" WORKFLOW_BODY="$workflow_body" GITHUB_RUN_ID="$candidate_run_id" GITHUB_SHA=0123456789abcdef0123456789abcdef01234567 GITHUB_REPOSITORY=HallelujahHomeChurch/hhc-web-api RELEASE_COMMIT=0123456789abcdef0123456789abcdef01234567 RELEASE_IMAGE_DIGEST=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef RELEASE_IMAGE=example.azurecr.io/example/hhc-web-api@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef FAIL_OPENAPI_BEFORE_POINTER="$failure_injection" bash -e -c '
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

  if [ "$pointer_uploaded" = true ]; then
    python3 - "$case_dir/pointer/current.json" <<'PYPOINTER'
import json,sys
record=json.load(open(sys.argv[1]))
assert set(record)=={'schemaVersion','service','commit','image','specBlob','specSha256','releaseUrl','publishedAt'}
assert record['schemaVersion']==1 and record['service']=='hhc-web-api'
assert record['image']=='example.azurecr.io/example/hhc-web-api@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'
PYPOINTER
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
smoke_line="$(grep -nF 'SMOKE_MODE=forward ./scripts/smoke-release.sh' "$workflow" | tail -1 | cut -d: -f1)"
rollback_smoke_line="$(grep -nF 'SMOKE_MODE=rollback ./scripts/smoke-release.sh' "$workflow" | tail -1 | cut -d: -f1)"
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


# A reviewed policy version is the single activation control; the model needs
# at least one CPU and 2 GiB, while larger existing allocations stay intact.
grep -Fq 'BULLETIN_WATERMARK_PRIVACY_VERSION: ${{ vars.BULLETIN_WATERMARK_PRIVACY_VERSION }}' "$workflow"
test "$(grep -Fc 'bulletinWatermarkPrivacyVersion="$BULLETIN_WATERMARK_PRIVACY_VERSION"' "$workflow")" -eq 5
! grep -Fq '0.25|0.5|0.50|0.75) cpu=1; memory=2Gi' "$workflow"
grep -Fq "param bulletinWatermarkPrivacyVersion string = ''" infra/main.bicep
direct_decode_enabled_count="$(grep -Fc "{ name: 'BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED', value: 'true' }" infra/main.bicep)"
test "$direct_decode_enabled_count" -eq 1
watermark_worker_block="$(sed -n '/resource watermarkWorker /,/resource migrate /p' infra/main.bicep)"
printf '%s\n' "$watermark_worker_block" | grep -Fq 'minReplicas: 0'
printf '%s\n' "$watermark_worker_block" | grep -Fq 'maxReplicas: 1'
printf '%s\n' "$watermark_worker_block" | grep -Fq 'cooldownPeriod: 300'


# Every downloaded model retains its exact integrity gate, including cached builds.
! grep -Eq "^ADD --checksum" Dockerfile
grep -Fq '43f37103f92efa8bd6b1c5902bb537cc12a981dc699fca19d9bb7de8c62d03d9  /opt/watermark/models/trustmark_Q.yaml' Dockerfile
grep -Fq 'dc382c3f6b4fd568b27d6fbb763d6ffc2d2587126d84afe9d5ee95b4c5d99826  /opt/watermark/models/encoder_Q.ckpt' Dockerfile
grep -Fq 'e3d9cea5406a26590735719f8f15cb10802b11852ae69047eaf4cf17214df781  /opt/watermark/models/decoder_Q.ckpt' Dockerfile
grep -Fq 'sha256sum --check --strict -' Dockerfile

release_validation_body="$(awk '
  /^      - name: Validate release configuration$/ { found = 1; next }
  found && /^        run: \|$/ { capture = 1; next }
  capture && (/^      - / || /^  [a-z_]+:/) { exit }
  capture { sub(/^          /, ""); print }
' "$workflow")"
check_extractor_release_input() {
  renderer="$1" enabled="$2" audience="$3" expected="$4" auto_enabled="${5-false}"
  if VALIDATION_BODY="$release_validation_body" \
    GITHUB_REF=refs/heads/main RELEASE_CONFIRMATION=deploy-hhc-web-api-production RELEASE_PUBLIC_EXPECTATION=normal \
    AZURE_CLIENT_ID=test AZURE_TENANT_ID=test AZURE_SUBSCRIPTION_ID=11111111-1111-4111-8111-111111111111 \
    CMS_TRANSLATION_ENABLED=false AZURE_OPENAI_ENDPOINT='' AZURE_OPENAI_DEPLOYMENT='' BROADCAST_WRITERS_ENABLED="${6:-false}" STUDIO_WRITERS_ENABLED="${8:-false}" VIDEO_INTERACTIONS_ENABLED=false VIDEO_ANALYTICS_ENABLED=false VIDEO_MEDIA_BASE_URL="${7:-}" \
    BULLETIN_RENDERER_IMAGE="$renderer" BULLETIN_EXTRACTION_ENABLED="$enabled" BULLETIN_EXTRACTOR_ASSET_AUDIENCE="$audience" BULLETIN_AUTO_EXTRACTION_ENABLED="$auto_enabled" BULLETIN_LLM_EXTRACTION_ENABLED=false \
    bash -euo pipefail -c 'eval "$VALIDATION_BODY"' >/dev/null 2>&1; then status=0; else status=$?; fi
  case "$expected" in pass) test "$status" -eq 0;; fail) test "$status" -ne 0;; esac
}
renderer_ref=ghcr.io/hallelujahhomechurch/hhc-bulletin-renderer@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
check_extractor_release_input "$renderer_ref" false api://asset-api pass
check_extractor_release_input "$renderer_ref" true api://asset-api pass
check_extractor_release_input '' false api://asset-api fail
check_extractor_release_input ghcr.io/hallelujahhomechurch/hhc-bulletin-renderer:latest false api://asset-api fail
check_extractor_release_input "$renderer_ref" yes api://asset-api fail
check_extractor_release_input "$renderer_ref" false '' fail
check_extractor_release_input "$renderer_ref" true api://asset-api pass true
check_extractor_release_input "$renderer_ref" false api://asset-api fail true
check_extractor_release_input "$renderer_ref" true api://asset-api fail yes
check_extractor_release_input "$renderer_ref" true api://asset-api fail TRUE
check_extractor_release_input "$renderer_ref" true api://asset-api fail ''

# New B1 writers are opt-in through the same reviewed release configuration.
grep -Fq 'param broadcastWritersEnabled bool = false' infra/main.bicep
# ARM diffs env arrays by position; append B1 after existing optional media refs.
awk '
  /], videoMediaEnvironment/ { media = NR }
  /BROADCAST_WRITERS_ENABLED/ { broadcast = NR }
  END { exit !(media > 0 && broadcast > media) }
' infra/main.bicep
grep -Fq "{ name: 'BROADCAST_WRITERS_ENABLED', value: broadcastWritersEnabled ? 'true' : 'false' }" infra/main.bicep
grep -Fq 'BROADCAST_WRITERS_ENABLED: ${{ vars.BROADCAST_WRITERS_ENABLED || '\''false'\'' }}' "$workflow"
grep -Fq 'case "$BROADCAST_WRITERS_ENABLED" in' "$workflow"
grep -Fq 'BROADCAST_WRITERS_ENABLED must be true or false' "$workflow"
test "$(grep -Fc 'broadcastWritersEnabled="$BROADCAST_WRITERS_ENABLED"' "$workflow")" -eq 4

check_extractor_release_input "$renderer_ref" false api://asset-api pass false false
check_extractor_release_input "$renderer_ref" false api://asset-api fail false invalid
check_extractor_release_input "$renderer_ref" false api://asset-api fail false true
check_extractor_release_input "$renderer_ref" false api://asset-api pass false true https://media.alive.org.tw
check_extractor_release_input "$renderer_ref" false api://asset-api fail false true https://other.invalid

check_extractor_release_input "$renderer_ref" true api://asset-api pass true true https://media.alive.org.tw

python3 - <<'PYEXTRACTOR'
from pathlib import Path
s=Path('.github/workflows/release.yml').read_text()
block=s.split('      - name: Deploy isolated bulletin extractor\n',1)[1].split('      - name: Capture deployed release identity',1)[0]
start=block.index('echo "EXTRACTOR_RECOVERY_REQUIRED=true"')
deploy=block.index('az deployment group create')
smoke=block.index('SMOKE_MODE=forward ./scripts/smoke-release.sh')
clear=block.rindex('echo "EXTRACTOR_RECOVERY_REQUIRED=false"')
drain=block.index('python3 scripts/wait-bulletin-extractor-rollout.py')
compatible=block.index('EXTRACTOR_COMPATIBLE_DEPLOYED=true')
producer=block.index('      - name: Deploy API')
assert start < deploy < drain < compatible < producer < smoke < clear, 'Extractor must precede producer and retain legacy recovery through smoke'
assert 'if [[ "$BULLETIN_LLM_EXTRACTION_ENABLED" == true ]]; then' in block[drain:compatible]
assert "env.EXTRACTOR_COMPATIBLE_DEPLOYED != 'true'" in s, 'Queued LLM work must retain its compatible consumer during API rollback'
PYEXTRACTOR
echo 'release policy verified'

# Studio rollout remains opt-in and reaches every deployment path.
grep -Fq "param studioWritersEnabled bool = false" infra/main.bicep
test "$(grep -Fc 'studioWritersEnabled="$STUDIO_WRITERS_ENABLED"' "$workflow")" -eq 4

python3 scripts/test-maintenance-acceptance.py

python3 scripts/test-seal-extractor-attempt.py

# Shared platform resources are references, never routine release writes.
python3 - <<'PYOWNER'
from pathlib import Path
import re
source=Path("infra/main.bicep").read_text()
for name in ['apiIdentity', 'migrateIdentity', 'runtimeVault', 'migrationVault', 'watermarkQueue', 'watermarkPoisonQueue', 'translationRAIPolicy']:
    assert re.search(r"resource "+name+r" '[^']+' existing =", source), name
PYOWNER

# W3: dedicated watermark worker identity stays out of the release and least-privilege.
identity_bicep=infra/bulletin-worker-identity.bicep
grep -Fq "name: 'hhc-web-bulletin-worker-identity'" "$identity_bicep"
[ "$(grep -c "resource [A-Za-z]* 'Microsoft.Authorization/roleAssignments@" "$identity_bicep")" = 4 ]
for grant in 'scope: watermarkQueue' 'scope: watermarkPoisonQueue' 'scope: jobsDatabaseSecret' 'scope: registry'; do
  grep -Fq "  $grant" "$identity_bicep"
done
if grep -Eq 'scope: (runtimeVault|queueStorage|queueService|resourceGroup)' "$identity_bicep"; then
  echo 'watermark worker identity grant is broader than one resource' >&2; exit 1
fi
grep -Fq "resource bulletinWorkerIdentity 'Microsoft.ManagedIdentity/userAssignedIdentities@2023-01-31' existing = if (bulletinWorkerIdentityEnabled || deployWatermarkJob)" infra/main.bicep
if grep -Eq "^resource [A-Za-z]+ 'Microsoft.ManagedIdentity/userAssignedIdentities@[^']+' = " infra/main.bicep; then
  echo 'main.bicep must not create managed identities' >&2; exit 1
fi

# W5: releases deploy the Event Job (what-if and deploy) and never the App;
# the App is recreated only by the rollback script.
[ "$(grep -c 'deployWatermarkWorker=false deployWatermarkJob=true' .github/workflows/release.yml)" = 2 ]
! grep -Fq 'deployWatermarkWorker=true' .github/workflows/release.yml
grep -Fq 'deployWatermarkWorker=true deployWatermarkJob=false' scripts/rollback-watermark-job.sh
grep -Fq 'param deployWatermarkJob bool = false' infra/main.bicep
grep -Fq 'param watermarkJobMaxExecutions int = 20' infra/main.bicep
grep -B1 'param watermarkJobMaxExecutions int = 20' infra/main.bicep | grep -Fq '@maxValue(20)'
watermark_job_env="$(sed -n '/^var hhcWebBulletinWatermarkEnvironment =/,/^$/p' infra/main.bicep)"
watermark_job_block="$(sed -n '/^resource watermarkJob /,/^}/p' infra/main.bicep)"
for expected in "triggerType: 'Event'" 'replicaTimeout: 300' 'replicaRetryLimit: 0' 'maxExecutions: watermarkJobMaxExecutions' "queueLengthStrategy: 'visibleonly'" "args: ['--once']" "secrets/jobs-database-url"; do
  printf '%s\n' "$watermark_job_block" | grep -Fq "$expected"
done
printf '%s\n' "$watermark_job_block" | grep -Fq 'filter(hhcWebBulletinWatermarkEnvironment,'
for expected in "{ name: 'DB_MAX_OPEN_CONNS', value: '1' }" "{ name: 'DB_MAX_IDLE_CONNS', value: '1' }" "{ name: 'BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED', value: 'false' }" "{ name: 'BULLETIN_WORKER_ASSET_AUTH', value: 'workload' }"; do
  printf '%s\n' "$watermark_job_env" | grep -Fq "$expected"
done
if printf '%s\n%s\n' "$watermark_job_block" "$watermark_job_env" | grep -Eq 'apiIdentity|dapr:|ingress:'; then
  echo 'watermark Job must use only the dedicated identity, without Dapr or ingress' >&2; exit 1
fi
python3 scripts/test-rollback-watermark-job.py
python3 scripts/test-cutover-watermark-job.py

# Every release job is bounded (D1 applied to hhc-web-api, PLAN-v2 section D).
awk '/^jobs:$/{j=1;next} j&&/^  [a-z_]+:$/{if(name!=""&&!t){print name; bad=1} name=$1; t=0} j&&/^    timeout-minutes: [0-9]+$/{t=1} END{if(name!=""&&!t){print name; bad=1} exit bad}' .github/workflows/release.yml

python3 - <<'PYTEST'
from contextlib import redirect_stdout
import importlib.util
import io
import json
import os
from pathlib import Path
import sys
import tempfile
from unittest.mock import patch

sys.path.insert(0, "scripts")
spec = importlib.util.spec_from_file_location("locations_release", "scripts/deploy-cms-locations-alias.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
normal = Path(".github/workflows/release.yml").read_text()
dedicated = Path(".github/workflows/locations-alias.yml").read_text()
assert "run: python3 scripts/extractor_evidence.py" in normal
assert "locations_alias_transition:" not in normal
assert "group: hhc-web-api-production-actions" in dedicated
assert "prepare_api:" not in dedicated and "az acr build" not in dedicated
assert "verify-rollback" in dedicated
assert "binding=(--remove-env-vars CMS_GATEWAY_LOCATIONS_TOKEN)" in dedicated
assert "binding=(--set-env-vars CMS_GATEWAY_LOCATIONS_TOKEN=secretref:cms-gateway-locations-token)" in dedicated
assert '"${binding[@]}"' in dedicated
image = "example.azurecr.io/example/hhc-web-api@sha256:" + "b" * 64
commit = "c" * 40
version = "a" * 32
flags = {module.FLAGS[key]: "false" for key in module.REQUIRED}
flags.update(RELEASE=commit, BULLETIN_WATERMARK_PRIVACY_VERSION="v1")
live = dict(image=image, cpu=0.5, memory="1Gi", env=[dict(name=n, value=v) for n, v in flags.items()])
all_proofs = {key: True for key in ("nameMatches", "pinnedUriMatches", "identityMatches", "noInlineSecret", "bindingMatches")}

def simulate(transition, current_revision, *, bad_image=False, extra_resource=False, common_drift=False):
    before_enabled = transition == "disable"
    before_version = version if before_enabled else ""
    state = dict(CMS_LOCATIONS_ENABLED=str(not before_enabled).lower(), CMS_LOCATIONS_VERSION=version,
                 CMS_LOCATIONS_PREVIOUS_ENABLED=str(before_enabled).lower(), CMS_LOCATIONS_PREVIOUS_VERSION=before_version)
    calls = []
    def fake_az(repo, *args):
        if args[:2] == ("identity", "show"):
            return json.dumps("00000000-0000-4000-8000-000000000001")
        if args[:2] == ("containerapp", "revision"):
            return json.dumps(dict(live, image=image + "bad") if bad_image else live)
        return "[]"
    def fake_preview(repo, shared, enabled, credential, intent, suffix, client_id):
        assert "deployRuntimeCompanions=false" in shared and "deployMigrationJob=false" in shared
        assert "deployWatermarkJob=false" in shared and "deployBulletinExtractor=false" in shared
        assert f"release={commit}" in shared and f"runtimeImage={image}" in shared
        paths = []
        if intent == "enable":
            paths = [("properties.configuration.secrets.5", "Create"), ("properties.template.containers.0.env.38", "Create")]
        elif intent == "disable":
            paths = [("properties.template.containers.0.env.38", "Delete")]
        changes = [dict(resource="hhc-web-api", type="Modify", paths=paths)]
        if extra_resource:
            changes += [dict(resource="hhc-web-content-import", type="Modify", paths=[])]
        return dict(policyPassed=True, normalizationsPassed=not common_drift, aliasReferenceProof=all_proofs, changes=changes)
    with tempfile.TemporaryDirectory() as tmp:
        env = dict(GITHUB_ACTIONS="true", GITHUB_EVENT_NAME="workflow_dispatch", GITHUB_RUN_ID="7", GITHUB_RUN_ATTEMPT="1",
                   GITHUB_WORKFLOW_REF=f"{module.REPO}/.github/workflows/locations-alias.yml@refs/heads/main",
                   GITHUB_ENV=str(Path(tmp) / "env"), RUNNER_TEMP=tmp, CMS_LOCATIONS_EXPECTED_REVISION=current_revision,
                   CMS_LOCATIONS_TRANSITION=transition, CMS_SOURCE_RELEASE_RUN="5")
        with (patch.dict(os.environ, env), patch.object(module, "api", return_value={"id": 5}),
              patch.object(module, "evidence", return_value=dict(head=commit, apiDigest=image.rsplit("@",1)[1])),
              patch.object(module, "capture", return_value=state), patch.object(module, "az", side_effect=fake_az),
              patch.object(module, "preview", side_effect=fake_preview), patch.object(module, "require_gateway_dapr") as gateway,
              patch.object(module, "command", side_effect=lambda args, **kw: calls.append(args)),
              redirect_stdout(io.StringIO())):
            if bad_image or extra_resource or common_drift:
                try:
                    module.main("apply")
                except ValueError:
                    pass
                else:
                    raise AssertionError("unsafe config-only apply accepted")
                assert not any(args[:4] == ["az", "deployment", "group", "create"] for args in calls)
                return
            module.main("apply")
            creates = [args for args in calls if args[:4] == ["az", "deployment", "group", "create"]]
            assert len(creates) == 1 and f"runtimeImage={image}" in creates[0]
            assert f"gatewayLocationsSecretVersion={version}" in creates[0]
            assert f"enableGatewayLocationsAlias={str(not before_enabled).lower()}" in creates[0]
            assert f"PREVIOUS_READY_REVISION={current_revision}" in Path(env["GITHUB_ENV"]).read_text()
            assert gateway.call_count == (2 if before_enabled else 0)

simulate("enable", "hhc-web-api--0000299")
simulate("disable", "hhc-web-api--locations-6-1")
simulate("enable", "hhc-web-api--0000299", bad_image=True)
simulate("enable", "hhc-web-api--0000299", extra_resource=True)
simulate("enable", "hhc-web-api--0000299", common_drift=True)
shape = dict(properties=dict(template=dict(containers=[dict(name="hhc-web-api")])))
change = dict(before=shape, after=shape,
              delta=[dict(path="properties.template.scale.minReplicas", propertyChangeType="Modify", before=1, after=0)])
assert not module.runtime_normalizations(change, "/subscriptions/test/resourceGroups/example-group", "", "preserve",
                                         "locations-7-1", "00000000-0000-4000-8000-000000000001")
with patch.object(module, "az", side_effect=[json.dumps(["api-gateway--0000270"]), json.dumps([["CMS_GATEWAY_LOCATIONS_TOKEN"]])]):
    try:
        module.require_gateway_dapr(Path.cwd())
    except ValueError:
        pass
    else:
        raise AssertionError("disable accepted an active Gateway alias consumer")
print("CMS alias configuration release contract passed")
PYTEST
