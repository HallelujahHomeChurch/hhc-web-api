#!/usr/bin/env python3
import test_no_cloud
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class RollbackWorker(unittest.TestCase):
    def run_rollback(self, *, wrong_image=False, wrong_revision=False, invalid=False):
        with tempfile.TemporaryDirectory() as directory:
            events = Path(directory) / 'events'
            env = {**os.environ, 'RESOURCE_GROUP': 'example-group', 'WATERMARK_WORKER_APP_NAME': 'hhc-web-bulletin-worker',
                   'PREVIOUS_WORKER_REVISION': 'hhc-web-bulletin-worker--previous',
                   'PREVIOUS_WORKER_IMAGE_REF': 'example.azurecr.io/example/hhc-web-api@sha256:' + 'a' * 64,
                   'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '2', 'EVENTS': str(events),
                   'WRONG_IMAGE': str(wrong_image), 'WRONG_REVISION': str(wrong_revision)}
            if invalid:
                env['PREVIOUS_WORKER_IMAGE_REF'] = 'untrusted/image:latest'
            result = subprocess.run(['bash', '-c', '''
                az() {
                  printf '%s\\n' "$*" >> "$EVENTS"
                  if [[ "$*" == *'revision copy'* ]]; then return 0; fi
                  if [[ "$*" == *'latestReadyRevisionName'* ]]; then
                    if [[ "$WRONG_REVISION" == True ]]; then echo hhc-web-bulletin-worker--other; else echo hhc-web-bulletin-worker--rollback-123-2; fi
                  elif [[ "$WRONG_IMAGE" == True ]]; then echo wrong; else echo "$PREVIOUS_WORKER_IMAGE_REF"; fi
                }
                sleep() { :; }
                export -f az sleep
                bash scripts/rollback-watermark-worker.sh
            '''], env=env, capture_output=True, text=True, timeout=10)
            return result.returncode, events.read_text() if events.exists() else ''

    def test_restores_exact_template_and_digest(self):
        code, events = self.run_rollback()
        self.assertEqual(code, 0)
        self.assertIn('--from-revision hhc-web-bulletin-worker--previous', events)
        self.assertIn('--revision-suffix rollback-123-2', events)
        self.assertNotIn('set-env', events)

    def test_wrong_readback_fails(self):
        for kwargs in ({'wrong_image': True}, {'wrong_revision': True}):
            with self.subTest(kwargs=kwargs):
                self.assertNotEqual(self.run_rollback(**kwargs)[0], 0)

    def test_invalid_target_does_not_mutate(self):
        code, events = self.run_rollback(invalid=True)
        self.assertNotEqual(code, 0)
        self.assertEqual(events, '')


if __name__ == '__main__':
    unittest.main()
