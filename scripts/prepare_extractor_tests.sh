#!/usr/bin/env bash
set -euo pipefail
mkdir -p artifacts/extractor-selection
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o artifacts/extractor-selection/parser.test ./internal/onlinebulletins
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o artifacts/extractor-selection/extractor.test ./cmd/extractor
python3 - <<'PY'
import hashlib,json
from pathlib import Path
root=Path('artifacts/extractor-selection')
(root/'binaries.json').write_text(json.dumps({p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in root.glob('*.test')}))
PY
test "$(docker inspect "$EXTRACTOR_IMAGE_REF" --format '{{.Config.User}}')" = '65532:65532'
docker run --rm --network none --read-only "$EXTRACTOR_IMAGE_REF" -version
docker run --rm --network none --read-only "$EXTRACTOR_IMAGE_REF" -verify-assets
docker run --rm --network none --read-only --entrypoint /usr/bin/pdftohtml "$EXTRACTOR_IMAGE_REF" -v
docker run --rm --network none --read-only --entrypoint /usr/bin/pdftocairo "$EXTRACTOR_IMAGE_REF" -v
docker run --rm --network none --read-only --memory 512m --cpus 1 \
  --tmpfs /tmp:rw,noexec,nosuid,size=128m \
  -v "$PWD/artifacts/extractor-selection/parser.test:/verification/parser.test:ro" \
  -v "$PWD/internal/onlinebulletins/testdata:/fixture-work/testdata:ro" \
  -w /fixture-work -e HHC_REQUIRE_POPPLER=1 \
  --entrypoint /verification/parser.test "$EXTRACTOR_IMAGE_REF" \
  -test.run TestParsePDFControlledFixtures -test.count=1 -test.v > artifacts/extractor-selection/parser-smoke.log
cat artifacts/extractor-selection/parser-smoke.log
python3 - <<'PY'
from pathlib import Path
s=Path('artifacts/extractor-selection/parser-smoke.log').read_text()
assert 'SKIP' not in s and 'FAIL' not in s
for name in ['1739-sanitized.pdf','1740-sanitized.pdf']:
 assert 'PASS: TestParsePDFControlledFixtures/'+name in s
PY
