#!/usr/bin/env bash
set -euo pipefail
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
if [[ $# -eq 0 ]]; then
  exec "${PYTHON:-python3}" "$repo_root/tools/bulletin_geometry/fixtures.py" "$repo_root/internal/onlinebulletins/testdata"
fi
if [[ $# -ne 4 ]]; then
  echo 'Usage: verify-bulletin-pdf-geometry.sh ISSUE_A ORIGINAL_A.pdf ISSUE_B ORIGINAL_B.pdf' >&2
  exit 2
fi
"${PYTHON:-python3}" "$repo_root/tools/bulletin_geometry/probe.py" "$1" "$2"
"${PYTHON:-python3}" "$repo_root/tools/bulletin_geometry/probe.py" "$3" "$4"
