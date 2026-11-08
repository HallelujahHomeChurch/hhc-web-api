#!/bin/sh
set -eu
image=${1:?image required}
cache=${RUNNER_TEMP:-/tmp}/trivy-cache
report=$(mktemp)
trap 'rm -f "$report"' EXIT
mkdir -p "$cache"
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v "$PWD/.vex:/vex:ro" -v "$cache:/root/.cache/trivy" \
  ghcr.io/aquasecurity/trivy@sha256:62b1e65e8869bc4b4c6aa4fa2b21595256c7c2f6018a9d9ad61caf87187c1969 \
  image --db-repository ghcr.io/aquasecurity/trivy-db:2 --db-repository public.ecr.aws/aquasecurity/trivy-db:2 --scanners vuln,secret --severity HIGH,CRITICAL --format json --vex /vex/lightning-2.6.6.openvex.json --show-suppressed "$image" > "$report"
python3 scripts/trivy-report.py "$report" "$image" >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"
