#!/usr/bin/env python3
"""Exercise the workflow's image handoff with real hashing/gzip and a Docker boundary fake."""
import gzip
import hashlib
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest


WORKFLOW = Path('.github/workflows/ci.yml')
COMMIT = 'a' * 40
IMAGE_ID = 'sha256:' + 'b' * 64


def step_body(name):
    match = re.search(r'      - name: ' + re.escape(name) +
                      r'\n.*?        run: \|\n(.*?)(?=^      - |^  \w+:|\Z)',
                      WORKFLOW.read_text(), re.M | re.S)
    if not match:
        raise AssertionError(f'Missing image handoff step: {name}')
    if '        shell: bash\n' not in match.group(0):
        raise AssertionError('Image handoff requires explicit bash with pipefail')
    return textwrap.dedent(match.group(1))


class ExtractorArtifactTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.archive = self.root / 'extractor-image.tar.gz'
        self.archive.write_bytes(gzip.compress(b'extractor fixture image'))
        self.sha = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        self.image_name = 'extractor-ci-123-1'
        self.selection_name = f'extractor-selection-{COMMIT}-1'
        self.downloaded_archive = self.root / 'extractor-inputs' / self.image_name / self.archive.name
        self.downloaded_archive.parent.mkdir(parents=True)
        self.downloaded_archive.write_bytes(self.archive.read_bytes())
        self.selection_dir = self.root / 'extractor-inputs' / self.selection_name
        self.selection_dir.mkdir()
        for name in ['ci.json', 'release-check.json']:
            (self.selection_dir / name).write_text('{}')
        docker = self.root / 'docker'
        docker.write_text('''#!/usr/bin/env python3
import os, sys
from pathlib import Path
with open(os.environ['DOCKER_CALLS'], 'a') as log:
    log.write(' '.join(sys.argv[1:]) + '\\n')
if sys.argv[1:] == ['image', 'inspect', 'bulletin-extractor:verify', '--format', '{{.Id}}']:
    print(os.environ['LOADED_IMAGE_ID'])
elif sys.argv[1:] == ['save', 'bulletin-extractor:verify']:
    if os.environ.get('SAVE_FAIL'): sys.exit(1)
    sys.stdout.buffer.write(b'extractor fixture image')
elif sys.argv[1:3] == ['load', '--input']:
    if os.environ.get('LOAD_FAIL'): sys.exit(1)
    if not Path(sys.argv[3]).is_file(): sys.exit(1)
else:
    sys.exit(2)
''')
        docker.chmod(0o755)
        git = self.root / 'git'
        git.write_text('#!/bin/sh\ntest "$1 $2" = "rev-parse HEAD" || exit 1\nprintf "%s\\n" "$CHECKOUT_SHA"\n')
        git.chmod(0o755)
        self.env = {
            **os.environ, 'PATH': str(self.root) + os.pathsep + os.environ['PATH'],
            'RUNNER_TEMP': str(self.root), 'GITHUB_OUTPUT': str(self.root / 'outputs'),
            'GITHUB_STEP_SUMMARY': str(self.root / 'summary'),
            'DOCKER_CALLS': str(self.root / 'docker-calls'), 'LOADED_IMAGE_ID': IMAGE_ID,
            'CHECKOUT_SHA': COMMIT, 'GITHUB_SHA': COMMIT, 'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '1',
            'PREPARED_COMMIT': COMMIT, 'PREPARED_RUN_ID': '123',
            'ARTIFACT_ID': '456', 'SELECTION_ID': '789', 'ARCHIVE_SHA256': self.sha, 'IMAGE_ID': IMAGE_ID,
            'IMAGE_ARTIFACT_NAME': self.image_name, 'SELECTION_ARTIFACT_NAME': self.selection_name,
        }

    def execute(self, names, **env):
        return subprocess.run(['bash', '-eo', 'pipefail', '-c', '\n'.join(map(step_body, names))],
                              env={**self.env, **env}, cwd=self.root, capture_output=True, text=True)

    def consume(self, **env):
        return self.execute(['Validate prepared extractor artifact', 'Load prepared extractor image'], **env)

    def calls(self):
        path = self.root / 'docker-calls'
        return path.read_text() if path.exists() else ''

    def test_producer_seals_actual_saved_image(self):
        result = self.execute(['Export extractor image for verification'])
        self.assertEqual(result.returncode, 0, result.stderr)
        outputs = dict(line.split('=', 1) for line in (self.root / 'outputs').read_text().splitlines())
        self.assertEqual(outputs['commit'], COMMIT)
        self.assertEqual(outputs['run_id'], '123')
        self.assertEqual(outputs['image_id'], IMAGE_ID)
        self.assertEqual(outputs['archive_sha256'], hashlib.sha256(self.archive.read_bytes()).hexdigest())
        self.assertEqual(gzip.decompress(self.archive.read_bytes()), b'extractor fixture image')

    def test_failed_save_cannot_publish_partial_archive(self):
        result = self.execute(['Export extractor image for verification'], SAVE_FAIL='1')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.root / 'outputs').exists())

    def test_invalid_producer_checkout_or_image_cannot_publish(self):
        for env in [{'CHECKOUT_SHA': 'c' * 40}, {'LOADED_IMAGE_ID': 'latest'}]:
            with self.subTest(env=env):
                self.assertNotEqual(self.execute(['Export extractor image for verification'], **env).returncode, 0)
                self.assertFalse((self.root / 'outputs').exists())

    def test_failed_load_stops_before_inspection(self):
        self.assertNotEqual(self.consume(LOAD_FAIL='1').returncode, 0)
        self.assertNotIn('image inspect', self.calls())

    def test_valid_artifact_is_loaded_and_identity_checked(self):
        result = self.consume()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.calls().splitlines(), [
            f'load --input {self.downloaded_archive}',
            'image inspect bulletin-extractor:verify --format {{.Id}}'])

    def test_failed_consumer_rerun_reuses_original_successful_producer(self):
        result = self.consume(GITHUB_RUN_ATTEMPT='2')
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_wrong_source_run_or_missing_identity_cannot_load(self):
        for key, value in [('CHECKOUT_SHA', 'c' * 40), ('PREPARED_COMMIT', 'c' * 40), ('PREPARED_RUN_ID', '124'),
                           ('ARTIFACT_ID', ''), ('ARTIFACT_ID', '456,789'), ('SELECTION_ID', ''),
                           ('SELECTION_ID', 'invalid'), ('IMAGE_ARTIFACT_NAME', '../other'),
                           ('SELECTION_ARTIFACT_NAME', 'extractor-selection-wrong-1'),
                           ('ARCHIVE_SHA256', ''), ('IMAGE_ID', 'latest')]:
            with self.subTest(key=key, value=value):
                result = self.consume(**{key: value})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.calls(), '')

    def test_corrupt_or_missing_archive_cannot_load(self):
        self.downloaded_archive.write_bytes(b'changed archive')
        self.assertNotEqual(self.consume().returncode, 0)
        self.assertEqual(self.calls(), '')
        self.downloaded_archive.unlink()
        self.assertNotEqual(self.consume().returncode, 0)
        self.assertEqual(self.calls(), '')

    def test_missing_selection_manifest_cannot_load(self):
        for name in ['ci.json', 'release-check.json']:
            with self.subTest(name=name):
                path = self.selection_dir / name
                path.unlink()
                self.assertNotEqual(self.consume().returncode, 0)
                self.assertEqual(self.calls(), '')
                path.write_text('{}')

    def test_valid_inputs_restore_selection_to_expected_path(self):
        self.assertEqual(self.consume().returncode, 0)
        self.assertTrue((self.root / 'artifacts/extractor-selection/ci.json').is_file())
        self.assertFalse(self.selection_dir.exists())

    def test_wrong_loaded_image_id_fails_after_load(self):
        self.assertNotEqual(self.consume(LOADED_IMAGE_ID='sha256:' + 'c' * 64).returncode, 0)
        self.assertIn('load --input', self.calls())


if __name__ == '__main__':
    unittest.main()
