#!/bin/sh
set -eu
python3 - <<'PY'
from pathlib import Path
import fnmatch
import re

cache_action = 'actions/cache@caa296126883cff596d87d8935842f9db880ef25'
workflows = {name: dict(re.findall(r'^  (\w+):\n(.*?)(?=^  \w+:\n|\Z)',
             Path(f'.github/workflows/{name}.yml').read_text(), re.M | re.S))
             for name in ['ci', 'release']}
expected_prefixes = {}
for workflow, go_job in [('ci', 'verify_go'), ('release', 'verify')]:
    for job, role in [(go_job, 'race-core')]:
        body = workflows[workflow][job]
        steps = re.split(r'(?=^      - )', body, flags=re.M)
        setup = next(step for step in steps if 'uses: actions/setup-go@' in step)
        assert 'id: go' in setup and 'cache: false' in setup, (workflow, job, 'setup-go must not restore the build cache twice')
        caches = [step for step in steps if cache_action in step]
        assert len(caches) == 2, (workflow, job, 'separate module and source-aware build caches')
        modules = next(step for step in caches if 'path: ~/go/pkg/mod\n' in step)
        module_key = "hhc-go-mod-v1-${{ runner.os }}-${{ runner.arch }}-${{ steps.go.outputs.go-version }}-${{ hashFiles('go.mod', 'go.sum') }}"
        assert 'key: ' + module_key in modules
        assert 'restore-keys:' not in modules and 'go-build' not in modules, 'module cache must be lockfile exact and contain no build cache'
        cache = next(step for step in caches if 'path: ~/.cache/go-build\n' in step)
        assert 'path: ~/.cache/go-build\n' in cache, (workflow, job, 'cache only Go build artifacts')
        key = re.search(r'^          key: (.+)$', cache, re.M).group(1)
        prefix = re.search(r'^          restore-keys: (.+)$', cache, re.M).group(1)
        for component in ['${{ runner.os }}', '${{ runner.arch }}', '${{ steps.go.outputs.go-version }}',
                          "${{ hashFiles('go.mod', 'go.sum') }}", role]:
            assert component in prefix, (workflow, job, 'unsafe cache fallback', component)
        source_hash = re.findall(r'\$\{\{ hashFiles\((.+?)\) \}\}', key)[-1]
        assert key == prefix + '${{ hashFiles(' + source_hash + ') }}', (workflow, job, 'source key/fallback mismatch')
        patterns = re.findall(r"'([^']+)'", source_hash)
        assert '**/*.go' in patterns, (workflow, job, 'all owned Go source must affect cache identity')
        # Embedded SQL/JSON changes also compile new bytes, even when Go source is unchanged.
        for source in Path('.').rglob('*.go'):
            if '.git' in source.parts or '.renderer-source' in source.parts:
                continue
            for declaration in re.findall(r'^//go:embed (.+)$', source.read_text(), re.M):
                for pattern in declaration.split():
                    for embedded in source.parent.glob(pattern):
                        if not source.name.endswith('_test.go'):
                            assert not embedded.name.endswith('_test.go'), ('production embed excluded by Docker context', embedded)
                        assert any(fnmatch.fnmatch(embedded.as_posix(), p) for p in patterns), (workflow, job, 'uncached embedded input', embedded)
        assert 'continue-on-error:' not in cache
        if role in expected_prefixes:
            assert prefix == expected_prefixes[role], 'CI and release cache toolchain/roles must align'
        expected_prefixes[role] = prefix
for workflow in ['ci', 'release']:
    consumer = workflows[workflow]['verify_extractor']
    assert 'cache: false' in consumer
    assert 'Cache Go modules' not in consumer and 'Cache incremental Go builds' not in consumer
    assert 'actions/cache@' not in consumer, 'prepared-binary consumers must not restore unused compilation caches'
    assert 'scripts/run_extractor_selection.py' in consumer
    assert 'artifacts/extractor-selection/*.test' in consumer
assert 'go test' not in Path('scripts/run_extractor_selection.py').read_text()
assert "'go', 'tool', 'test2json'" in Path('scripts/run_extractor_selection.py').read_text()

image = workflows['ci']['verify_image']
assert 'docker/setup-buildx-action@f87e5991a6d7451dcb8d9637bfbc97413f497069' in image
assert 'version: v0.38.0' in image
assert 'image=moby/buildkit@sha256:cec9f139f45e93c5c69c60f8b07cfad9f43f4ef6b6a6cd917527fea5ff2e3dea' in image
build = next(step for step in re.split(r'(?=^      - )', image, flags=re.M)
             if 'docker/build-push-action@c3c9e263c25d99ce0380d002d59b67737d91b0dc' in step)
for setting in ['context: .', 'file: Dockerfile', 'platforms: linux/amd64', 'load: true', 'push: false',
                'target: watermark', 'tags: hhc-web-watermark:verify', 'APT_CACHE_DATE=${{ steps.build_date.outputs.date }}']:
    assert setting in build, ('missing immutable local image cache guard', setting)
scope = "hhc-web-api-ci-${{ github.event.pull_request.number || github.ref_name }}"
assert f'cache-from: type=gha,scope={scope}' in build
assert f'cache-to: type=gha,mode=max,scope={scope}' in build
api_build = next(step for step in re.split(r'(?=^      - )', image, flags=re.M) if 'target: api' in step)
for setting in ['context: .', 'file: Dockerfile', 'platforms: linux/amd64', 'load: true', 'push: false', 'tags: hhc-web-api:verify', 'DOCKER_BUILD_RECORD_UPLOAD: false']:
    assert setting in api_build, ('missing lean API build guard', setting)
api_scope = "hhc-web-api-lean-ci-${{ github.event.pull_request.number || github.ref_name }}"
assert f'cache-from: type=gha,scope={api_scope}' in api_build
assert f'cache-to: type=gha,mode=max,scope={api_scope}' in api_build
assert 'scripts/test-runtime-images.py hhc-web-api:verify hhc-web-watermark:verify' in image
assert 'id: build_date' in image and 'date -u +%F' in image
assert 'id-token: write' not in image and 'login' not in image, 'PR image builds need no registry credentials'
assert 'hhc-web-api-ci-' not in Path('.github/workflows/release.yml').read_text(), 'release must not import PR image caches'
native = workflows['ci']['prepare_extractor']
steps = re.split(r'(?=^      - )', native, flags=re.M)
renderer = next(step for step in steps if 'name: Build cached renderer image' in step)
extractor = next(step for step in steps if 'name: Build cached extractor image' in step)
for role, build in [('renderer', renderer), ('extractor', extractor)]:
    assert 'docker/build-push-action@c3c9e263c25d99ce0380d002d59b67737d91b0dc' in build
    assert 'push: false' in build and 'platforms: linux/amd64' in build
    assert 'DOCKER_BUILD_RECORD_UPLOAD: false' in build
    # The renderer producer has no apt date build argument: never restore another day's layers.
    scope = 'hhc-' + role + '-ci-${{ github.event.pull_request.number || github.ref_name }}-${{ steps.build_date.outputs.date }}'
    assert 'cache-from: type=gha,scope=' + scope in build, (role, 'native cache must be PR/branch and UTC-day scoped')
    assert 'cache-to: type=gha,mode=max,scope=' + scope in build, (role, 'single producer exports shared image layers')
    assert build.count('cache-from:') == build.count('cache-to:') == 1
assert 'provenance: false' in renderer, 'OCI producer must have one runtime manifest'
assert 'outputs: type=oci,dest=${{ runner.temp }}/renderer.tar' in renderer
assert 'context: .renderer-source/artifacts/renderer' in renderer
assert 'file: .renderer-source/tools/bulletin-renderer/Dockerfile' in renderer
assert 'load: true' in extractor and 'tags: bulletin-extractor:verify' in extractor
assert 'provenance: false' in extractor
assert 'target: extractor' in extractor and 'file: Dockerfile.extractor' in extractor
assert 'build-contexts: bulletin-renderer=${{ steps.renderer.outputs.context }}' in extractor
assert "if: steps.renderer.outputs.reuse != 'true'" in renderer
assert 'BULLETIN_RENDERER_IMAGE=bulletin-renderer' in extractor
assert 'APT_CACHE_DATE=${{ steps.build_date.outputs.date }}' in extractor
assert 'tar -xf "$RUNNER_TEMP/renderer.tar" -C "$RUNNER_TEMP/renderer-layout"' in native
assert 'id: build_date' in native and 'date -u +%F' in native
assert 'id-token: write' not in native and 'docker login' not in native
release = Path('.github/workflows/release.yml').read_text()
assert 'hhc-renderer-ci-' not in release and 'hhc-extractor-ci-' not in release, 'production cannot import PR native image caches'
assert 'hhc-web-api-lean-ci-' not in release, 'production cannot import the PR API cache'
assert 'api_cache_ref="${ACR_LOGIN_SERVER}/${IMAGE_REPOSITORY}:buildcache-api"' in release
api_release = release.split('docker buildx build --platform linux/amd64 --target api', 1)[1].split('--file Dockerfile --push .', 1)[0]
assert '--cache-from "type=registry,ref=${api_cache_ref}"' in api_release
assert '--cache-to "type=registry,ref=${api_cache_ref},mode=max"' in api_release
print('Source-aware Go and isolated CI image cache policy verified')
PY
