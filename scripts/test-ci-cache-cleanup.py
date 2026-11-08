#!/usr/bin/env python3
"""Exercise the trusted workflow with only the external GitHub API replaced."""
import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import textwrap
import unittest
from unittest.mock import patch


WORKFLOW = Path('.github/workflows/cache-cleanup.yml')
REF = 'refs/pull/259/merge'
REPO = 'HallelujahHomeChurch/hhc-web-api'


class CacheCleanupTests(unittest.TestCase):
    def run_cleanup(self, pages=None, states=None, number='259', failure=None, pr_number=259):
        self.assertTrue(WORKFLOW.exists(), 'closed-PR cache cleanup workflow is missing')
        source = WORKFLOW.read_text()
        script = textwrap.dedent(source.split("          python3 - <<'PY'\n", 1)[1].split('\n          PY', 1)[0])
        if pages is None:
            pages = [{'actions_caches': [{'id': 101, 'ref': REF}]}]
        states = iter(states or ['closed'] * 10)
        self.calls = []
        self.deleted = []

        def api(args, **kwargs):
            self.calls.append(args)
            self.assertIsInstance(args, list)
            self.assertNotIn('shell', kwargs)
            self.assertEqual(args[:4], ['gh', 'api', '--method', args[3]])
            path = args[4]
            if failure == args[3] or (failure == 'LIST' and path.endswith('/actions/caches')):
                raise subprocess.CalledProcessError(1, args)
            if args[3] == 'DELETE':
                self.assertRegex(path, rf'^repos/{REPO}/actions/caches/[1-9][0-9]*$')
                self.deleted.append(int(path.rsplit('/', 1)[1]))
                return b''
            self.assertEqual(args[3], 'GET')
            if path == f'repos/{REPO}/pulls/259':
                return json.dumps({'number': pr_number, 'state': next(states)}).encode()
            self.assertEqual(path, f'repos/{REPO}/actions/caches')
            self.assertEqual(args[5:], ['-f', 'ref=' + REF, '-f', 'per_page=100', '--paginate', '--slurp'])
            return json.dumps(pages).encode()

        with patch.dict(os.environ, {'GH_REPO': REPO, 'PR_NUMBER': number}, clear=True), \
                patch('subprocess.check_output', side_effect=api), \
                contextlib.redirect_stdout(io.StringIO()):
            exec(compile(script, str(WORKFLOW), 'exec'), {})

    def test_deletes_all_paginated_ids_for_exact_closed_pr(self):
        self.run_cleanup(pages=[{'actions_caches': [{'id': 101, 'ref': REF}]},
                                {'actions_caches': [{'id': 202, 'ref': REF}]}])
        self.assertEqual(self.deleted, [101, 202])

    def test_empty_cache_is_noop(self):
        self.run_cleanup(pages=[{'actions_caches': []}])
        self.assertEqual(self.deleted, [])

    def test_reopened_pr_is_preserved(self):
        self.run_cleanup(states=['open'])
        self.assertEqual(self.deleted, [])

    def test_reopen_during_cleanup_stops_remaining_deletes(self):
        self.run_cleanup(pages=[{'actions_caches': [{'id': 101, 'ref': REF}, {'id': 202, 'ref': REF}]}],
                         states=['closed', 'closed', 'open'])
        self.assertEqual(self.deleted, [101])

    def test_all_pages_validated_before_delete(self):
        for ref in ['refs/heads/main', 'refs/pull/260/merge', 'refs/pull/259/head']:
            with self.subTest(ref=ref), self.assertRaises(ValueError):
                self.run_cleanup(pages=[{'actions_caches': [{'id': 101, 'ref': REF}]},
                                        {'actions_caches': [{'id': 202, 'ref': ref}]}])
            self.assertEqual(self.deleted, [])

    def test_invalid_ids_fail_before_any_delete(self):
        for cache_id in [True, 0, -1, '101', '101?ref=refs/heads/main', None]:
            with self.subTest(cache_id=cache_id), self.assertRaises(ValueError):
                self.run_cleanup(pages=[{'actions_caches': [{'id': cache_id, 'ref': REF}]}])
            self.assertEqual(self.deleted, [])

    def test_pr_number_cannot_change_api_target_or_run_shell(self):
        for number in ['', '0', '-1', '0259', '259/../../actions', '259; echo unsafe', '２５９']:
            with self.subTest(number=number), self.assertRaises(ValueError):
                self.run_cleanup(number=number)
            self.assertEqual(self.calls, [])

    def test_api_errors_fail_visibly(self):
        for method in ['GET', 'LIST', 'DELETE']:
            with self.subTest(method=method), self.assertRaises(subprocess.CalledProcessError):
                self.run_cleanup(failure=method)
            self.assertEqual(self.deleted, [])

    def test_unexpected_pr_response_fails_before_delete(self):
        for state in ['unknown', None]:
            with self.subTest(state=state), self.assertRaises(ValueError):
                self.run_cleanup(states=[state])
            self.assertEqual(self.deleted, [])
        with self.assertRaises(ValueError):
            self.run_cleanup(pr_number=260)
        self.assertEqual(self.deleted, [])

    def test_missing_cache_collection_fails_closed(self):
        with self.assertRaises(KeyError):
            self.run_cleanup(pages=[{}])
        self.assertEqual(self.deleted, [])

    def test_privileged_workflow_has_no_untrusted_execution(self):
        self.assertTrue(WORKFLOW.exists())
        source = WORKFLOW.read_text()
        self.assertIn('pull_request_target:\n    types: [closed]', source)
        self.assertNotIn('workflow_dispatch:', source)
        self.assertNotIn('workflow_run:', source)
        self.assertNotIn('schedule:', source)
        self.assertIn('permissions: {}', source)
        self.assertIn('      actions: write\n      pull-requests: read', source)
        self.assertNotRegex(source, r'(?m)^\s*(uses|contents|id-token):')
        self.assertEqual(source.count('run: |'), 1)
        script = source.split('run: |', 1)[1]
        self.assertNotIn('${{', script, 'event expressions must never become executable code')


if __name__ == '__main__':
    unittest.main()
