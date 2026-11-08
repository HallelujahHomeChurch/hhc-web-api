#!/bin/sh
set -eu
python3 - <<'PY'
from pathlib import Path
import sys
sys.path.insert(0, "scripts")
import os
import re
import subprocess
import tempfile

workflow = Path('.github/workflows/ci.yml').read_text()
jobs = dict(re.findall(r'^  (\w+):\n(.*?)(?=^  \w+:\n|\Z)', workflow, re.M | re.S))
release = dict(re.findall(r'^  (\w+):\n(.*?)(?=^  \w+:\n|\Z)', Path('.github/workflows/release.yml').read_text(), re.M | re.S))
assert 'verify_extractor' in jobs, 'extractor must run independently of Go tests'
assert 'prepare_extractor' in jobs, 'native image must be prepared once before the matrix'
assert 'needs: [changes, prepare_extractor]' in jobs['verify_extractor']
for job in ['verify_go', 'prepare_extractor', 'verify_extractor', 'verify_image']:
    if job != 'verify_extractor':
        assert 'needs: changes' in jobs[job]
    assert "if: needs.changes.outputs.docs_only != 'true'" in jobs[job], 'only proven documentation may skip application checks'
assert 'needs: changes' in jobs['verify_policy']
assert "if: needs.changes.outputs.docs_only != 'true'" in jobs['verify_policy'], 'policy checks remain mandatory for source/configuration and unknown diffs'
assert 'timeout-minutes: 15' in jobs['verify_extractor']
assert 'fail-fast: false' in jobs['verify_extractor'], 'all renderer shards must complete'
assert 'continue-on-error:' not in jobs['verify_extractor']
assert 'matrix: ${{ fromJSON(needs.prepare_extractor.outputs.matrix) }}' in jobs['verify_extractor']
assert 'scripts/run_extractor_selection.py ci' in jobs['verify_extractor']
assert 'release_args=(artifacts/extractor-selection/release-check.json artifacts/extractor-results-release)' in jobs['verify_extractor']
assert '"${release_args[@]}"' in jobs['verify_extractor']
from extractor_selection import GROUPS, VERSIONS
assert len(GROUPS) == 4 and sorted(v for group in GROUPS for v in group) == sorted(VERSIONS)
assert 'HHC_EXTRACTOR_LOAD_IMAGE=' not in jobs['verify_go']
for go_job, bootstrap_step in [(jobs['verify_go'], 'Bootstrap migration role'), (release['verify'], 'Test migration and deployment policies')]:
    assert 'strategy:' not in go_job, 'Go groups share one runner'
    assert 'continue-on-error:' not in go_job
    assert 'run: go vet ./...' in go_job
    assert './scripts/bootstrap-migration-role.test.sh' in go_job
    assert 'HHW_TEST_STORAGE_DATABASE_URL:' in go_job
    assert "--command='CREATE DATABASE hhc_web_core_test'" in go_job
    assert 'python3 scripts/test-go-lanes_test.py' in go_job
    assert 'python3 scripts/test-go-lanes.py' in go_job
    assert 'matrix.group' not in go_job
    for step_name in ['Install and test deterministic bulletin converter', bootstrap_step, 'Vet Go']:
        assert go_job.count('name: ' + step_name + '\n') == 1, f'{step_name} must execute once'
# Exercise discovery, exact coverage, separate DSNs and actual subprocess failures.
subprocess.run([sys.executable, 'scripts/test-go-lanes_test.py'], check=True)

extractor = jobs['prepare_extractor']
consumer = jobs['verify_extractor']
assert '    strategy:' not in extractor and 'services:' not in extractor
assert 'docker/build-push-action@' not in consumer
assert 'Build exact shared renderer for integration' in extractor
assert 'continue-on-error:' not in extractor
assert 'id-token: write' not in extractor + consumer and 'docker login' not in extractor + consumer
upload = next(step for step in re.split(r'(?=^      - )', extractor, flags=re.M)
              if 'name: Store extractor verification image' in step)
for field in ['actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a',
              'name: extractor-ci-${{ github.run_id }}-${{ github.run_attempt }}',
              'retention-days: 1', 'compression-level: 0', 'if-no-files-found: error']:
    assert field in upload, ('unsafe image artifact producer', field)
download = next(step for step in re.split(r'(?=^      - )', consumer, flags=re.M)
                if 'name: Download prepared extractor inputs' in step)
for field in ['actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c',
              'artifact-ids: ${{ needs.prepare_extractor.outputs.artifact_id }},${{ needs.prepare_extractor.outputs.selection_id }}',
              'merge-multiple: false', 'digest-mismatch: error']:
    assert field in download, ('unsafe image artifact consumer', field)
assert 'needs.prepare_extractor.outputs.selection_id' in consumer, 'use the successful producer output on retries'
assert 'run-id:' not in download and 'github-token:' not in download, 'download only from this run'
assert consumer.index('name: Validate prepared extractor artifact') < consumer.index('name: Download prepared extractor inputs')
assert consumer.index('name: Load prepared extractor image') < consumer.index('name: Verify isolated bulletin extractor runtime')
assert consumer.count('uses: actions/download-artifact@') == 1, 'one action process downloads both exact-ID inputs'
for field in ['SELECTION_ID:', 'IMAGE_ARTIFACT_NAME: ${{ needs.prepare_extractor.outputs.artifact_name }}',
              'SELECTION_ARTIFACT_NAME: ${{ needs.prepare_extractor.outputs.selection_name }}']:
    assert field in consumer, field
steps = re.split(r'(?=^      - )', extractor, flags=re.M)
renderer_build = next((step for step in steps if 'name: Build cached renderer image' in step), None)
assert renderer_build is not None, 'build the renderer through the cached OCI action'
extractor_build = next((step for step in steps if 'name: Build cached extractor image' in step), None)
assert extractor_build is not None, 'build the extractor from the reviewed OCI context'
assert 'outputs: type=oci,dest=${{ runner.temp }}/renderer.tar' in renderer_build
assert 'provenance: false' in renderer_build
assert 'build-contexts: bulletin-renderer=${{ steps.renderer.outputs.context }}' in extractor_build
assert 'BULLETIN_RENDERER_IMAGE=bulletin-renderer' in extractor_build
assert 'BULLETIN_RENDERER_ARTIFACT_SHA256=50cb71b21129cb5296f8fa849da743d3553048d9ebacee27d013ad1552d087a1' in extractor_build
assert 'load: true' in extractor_build and 'push: false' in extractor_build
assert 'target: extractor' in extractor_build and 'tags: bulletin-extractor:verify' in extractor_build
assert 'tar -xf "$RUNNER_TEMP/renderer.tar" -C "$RUNNER_TEMP/renderer-layout"' in extractor
assert 'docker build ' not in extractor, 'native images must use the reviewed OCI/cache handoff'
runtime = Path('scripts/prepare_extractor_tests.sh').read_text()
assert 'postgres:' in consumer and 'HHW_TEST_DATABASE_URL:' in consumer
for check in ['--entrypoint /usr/bin/pdftohtml', '--entrypoint /usr/bin/pdftocairo', '--memory 512m --cpus 1', '-test.run TestParsePDFControlledFixtures']:
    assert check in runtime, f'missing shared extractor preflight: {check}'
assert './scripts/prepare_extractor_tests.sh' in extractor
assert 'scripts/extractor_selection.py verify ci' in jobs['verify']
assert 'scripts/extractor_selection.py verify release' in jobs['verify']
assert 'id-token: write' not in workflow, 'PR verification has no deployment authority'

release_load = release['verify_extractor']
assert 'matrix: ${{ fromJSON(needs.prepare_extractor.outputs.matrix) }}' in release_load
assert 'fail-fast: false' in release_load and 'continue-on-error:' not in release_load
assert 'scripts/run_extractor_selection.py release' in release_load
assert './scripts/prepare_extractor_tests.sh' in release['prepare_extractor']
assert 'scripts/extractor_selection.py verify release' in release['verify_extractor_evidence']
assert 'needs: [prepare_extractor, verify_extractor_evidence, public_acceptance]' in release['seal_extractor_evidence']
assert 'retention-days: 90' in release['seal_extractor_evidence']
assert 'needs: prepare_extractor' in release_load
assert 'needs: [configuration, verify, prepare_api, prepare_extractor, scan_extractor, verify_extractor, verify_extractor_evidence]' in release['deploy']
gate = release['deploy'].split('    if: >-\n', 1)[1].split('    runs-on:', 1)[0]
assert "!cancelled() && needs.configuration.result == 'success'" in gate
for required in ['verify', 'prepare_api', 'prepare_extractor', 'scan_extractor', 'verify_extractor', 'verify_extractor_evidence']:
    assert f"needs.{required}.result == 'success'" in gate
assert 'recovery_run' not in gate and "'skipped'" not in gate
assert 'continue-on-error:' not in release['scan_extractor']
assert not re.search(r'^    if:|^        if:', release['scan_extractor'], re.M), 'the scanner gate must execute unconditionally'
scan_step = re.search(r'      - name: Scan immutable extractor image\n.*?        run: \|\n(.*?)(?=^      - |\Z)', release['scan_extractor'], re.M | re.S).group(1)
scan_body = '\n'.join(line[10:] for line in scan_step.splitlines())
# Exercise the real scanner shell: a scanner failure must fail its deploy dependency.
with tempfile.TemporaryDirectory() as directory:
    directory = Path(directory)
    for command, body in [('az', 'exit 0'), ('docker', 'if [ "$1" = run ]; then exit "$SCANNER_STATUS"; fi')]:
        stub = directory / command
        stub.write_text('#!/bin/sh\n' + body + '\n')
        stub.chmod(0o755)
    for status in ['0', '1']:
        result = subprocess.run(['bash', '-euo', 'pipefail', '-c', scan_body], capture_output=True, env={
            **os.environ, 'PATH': str(directory) + os.pathsep + os.environ['PATH'],
            'SCANNER_STATUS': status, 'ACR_NAME': 'test', 'EXTRACTOR_IMAGE_REF': 'test@sha256:digest'})
        assert (result.returncode == 0) == (status == '0'), 'scanner failure must block deployment'


required = ['changes', 'verify_policy', 'verify_go', 'prepare_extractor', 'verify_extractor', 'verify_image']
verify = jobs['verify']
assert "if: ${{ always() && needs.changes.outputs.config_only != 'true' }}" in verify
assert f'needs: [{", ".join(required)}]' in verify
body = verify.split('        run: |\n', 1)[1].split('      - ', 1)[0]
body = '\n'.join(line[10:] for line in body.splitlines())
# Execute the actual aggregator for source and proven-document diffs.
for docs in ['true', 'false', '']:
    expected = {job: ('skipped' if docs == 'true' and job in required[1:] else 'success') for job in required}
    for failing in [None, *required]:
        for status in (['success'] if failing is None else ['failure', 'cancelled', 'skipped', 'success']):
            results = {**expected, **({failing: status} if failing else {})}
            command = body.replace('${{ needs.changes.outputs.docs_only }}', docs).replace('${{ needs.changes.outputs.extractor_required }}', 'true')
            for job in required:
                command = command.replace('${{ needs.' + job + '.result }}', results[job])
            result = subprocess.run(['sh', '-e', '-c', command], capture_output=True)
            assert (result.returncode == 0) == (results == expected), (docs, failing, status, result.stderr)
print('CI extractor isolation and fail-closed aggregation passed')
PY
