#!/bin/sh
set -eu
# Keep manual runs unique; only PR updates may cancel an older verification.
grep -Fq "group: hhc-web-api-ci-\${{ github.event_name }}-\${{ github.event.pull_request.number || github.run_id }}" .github/workflows/ci.yml
grep -Fq "cancel-in-progress: \${{ github.event_name == 'pull_request' }}" .github/workflows/ci.yml
