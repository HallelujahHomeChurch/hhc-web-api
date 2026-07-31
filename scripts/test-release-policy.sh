#!/bin/sh
set -eu

workflow=.github/workflows/release.yml

grep -q 'workflow_dispatch:' "$workflow"
grep -q 'deploy-hhc-web-api-production' "$workflow"
if grep -q '^  push:' "$workflow"; then
  echo 'production release must not run automatically on push' >&2
  exit 1
fi
grep -q 'environment: production' "$workflow"
grep -q 'IMAGE_REF=.*@${digest}' "$workflow"
grep -q 'az deployment group what-if' "$workflow"
grep -q -- '--validation-level Provider' "$workflow"
grep -q 'changeType == "Unsupported"' "$workflow"
grep -q 'PREVIOUS_IMAGE_REF=' "$workflow"
grep -q 'az containerapp revision copy' "$workflow"
grep -q -- '--image "$PREVIOUS_IMAGE_REF"' "$workflow"
grep -q 'Verify rolled back runtime' "$workflow"
grep -q '/health/live' infra/main.bicep
grep -q '/health/ready' infra/main.bicep
grep -q "type: 'Startup'" infra/main.bicep

if grep -Eiq 'migrate[[:space:]_-]*down|migration[[:space:]_-]*rollback' "$workflow"; then
  echo 'release workflow must not roll back database migrations automatically' >&2
  exit 1
fi

if grep -Eq 'PREVIOUS_MIGRATION_IMAGE|Restore migration job|containerapp job update' "$workflow"; then
  echo 'release workflow must preserve the forward migration image' >&2
  exit 1
fi
