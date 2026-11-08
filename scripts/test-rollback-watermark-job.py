#!/usr/bin/env python3
"""Rollback rehearsal for the watermark Event Job cutover (PLAN-v2 W5).

A stub `az` models the Container Apps state (App present/absent, Job
present/absent, revision images) and records every call, so each case proves
both the outcome and that no unexpected write happened.
"""
import test_no_cloud
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

APP_IMAGE = 'example.azurecr.io/example/hhc-web-api@sha256:' + 'a' * 64
JOB_IMAGE = 'example.azurecr.io/example/hhc-web-api@sha256:' + 'b' * 64
NEW_IMAGE = 'example.azurecr.io/example/hhc-web-api@sha256:' + 'c' * 64
API_IMAGE = 'example.azurecr.io/example/hhc-web-api@sha256:' + 'd' * 64

STUB = r'''
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
state_path = Path(os.environ['STUB_STATE'])
state = json.loads(state_path.read_text())
with open(os.environ['STUB_EVENTS'], 'a') as events:
    events.write(json.dumps(args) + '\n')
def save():
    state_path.write_text(json.dumps(state))
def value(flag):
    return args[args.index(flag) + 1]
joined = ' '.join(args)
if args[:2] == ['containerapp', 'list']:
    print(1 if state['app'] else 0)
elif args[:3] == ['containerapp', 'job', 'list']:
    print(1 if state['job'] else 0)
elif args[:3] == ['containerapp', 'job', 'update']:
    if state.get('ignore_pause'):
        sys.exit(0)
    state['max_executions'] = int(value('--max-executions')); save()
elif args[:3] == ['containerapp', 'job', 'show']:
    print(state['max_executions'])
elif args[:3] == ['deployment', 'group', 'create']:
    params = dict(p.split('=', 1) for p in args[args.index('--parameters') + 1:] if '=' in p and not p.startswith('--'))
    state['deploy_params'] = params
    if params.get('deployWatermarkWorker') == 'true':
        state['app'] = True
        state['revision'] = 'hhc-web-bulletin-worker--recreated'
        state['image'] = state.get('recreate_image') or params['watermarkWorkerImage']
        state['ready_after'] = state.get('ready_delay', 0)
    save()
elif args[:3] == ['containerapp', 'revision', 'copy']:
    state['revision'] = 'hhc-web-bulletin-worker--' + value('--revision-suffix')
    state['image'] = state.get('copy_image') or value('--image'); save()
elif args[:2] == ['containerapp', 'show']:
    if not state['app']:
        print('ResourceNotFound', file=sys.stderr); sys.exit(3)
    if 'minReplicas' in joined:
        print(state.get('min', 0))
    elif 'latestReadyRevisionName' in joined:
        if state.get('ready_after', 0) > 0:
            state['ready_after'] -= 1; save(); print('')
        else:
            print(state['revision'])
    else:
        print(state['revision'])
elif args[:3] == ['containerapp', 'revision', 'show']:
    if value('--query') == 'properties.template.containers[0].image':
        print(state['image']); sys.exit(0)
    print(json.dumps({'image': state['image'], 'active': state.get('active', True), 'health': state.get('health', 'Healthy'), 'running': state.get('running', 'Running')}))
else:
    print('unexpected az call: ' + joined, file=sys.stderr)
    sys.exit(2)
'''


class RollbackWatermarkJob(unittest.TestCase):
    def run_rollback(self, state, **env_overrides):
        defaults = {'app': False, 'job': True, 'max_executions': 20, 'revision': 'hhc-web-bulletin-worker--previous', 'image': APP_IMAGE}
        state = {**defaults, **state}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bin').mkdir()
            for name, body in (('az', STUB), ('sleep', 'import sys\n')):
                path = root / 'bin' / name
                path.write_text('#!' + sys.executable + '\n' + body)
                path.chmod(0o755)
            (root / 'state.json').write_text(json.dumps(state))
            env = {**os.environ, 'PATH': str(root / 'bin') + os.pathsep + os.environ['PATH'],
                   'STUB_STATE': str(root / 'state.json'), 'STUB_EVENTS': str(root / 'events'),
                   'RESOURCE_GROUP': 'example-group', 'WATERMARK_WORKER_APP_NAME': 'hhc-web-bulletin-worker',
                   'WATERMARK_JOB_NAME': 'hhc-web-bulletin-watermark',
                   'PREVIOUS_WORKER_REVISION': 'hhc-web-bulletin-worker--previous',
                   'PREVIOUS_WORKER_IMAGE_REF': APP_IMAGE, 'PREVIOUS_WATERMARK_JOB_IMAGE_REF': '',
                   'IMAGE_REF': API_IMAGE, 'RUNTIME_CPU': '0.5', 'RUNTIME_MEMORY': '1Gi',
                   'BULLETIN_WATERMARK_PRIVACY_VERSION': 'privacy-test', 'BULLETIN_EXTRACTOR_ASSET_AUDIENCE': 'api://asset-api',
                   'BULLETIN_WORKER_IDENTITY_ENABLED': 'false', 'BULLETIN_WORKER_ASSET_AUTH': 'dapr',
                   'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '2'}
            env.update(env_overrides)
            result = subprocess.run(['bash', 'scripts/rollback-watermark-job.sh'], env=env,
                                    capture_output=True, text=True, timeout=30)
            events = [json.loads(line) for line in (root / 'events').read_text().splitlines()] if (root / 'events').exists() else []
            return result, events, json.loads((root / 'state.json').read_text())

    @staticmethod
    def writes(events):
        return [e for e in events if e[:3] in (['containerapp', 'job', 'update'], ['deployment', 'group', 'create'], ['containerapp', 'revision', 'copy'])
                or e[:2] in (['containerapp', 'delete'], ['containerapp', 'update'])]

    def test_app_still_present_and_unchanged_only_pauses_job(self):
        result, events, state = self.run_rollback({'app': True})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state['max_executions'], 0)
        self.assertEqual(self.writes(events), [['containerapp', 'job', 'update', '-g', 'example-group', '-n', 'hhc-web-bulletin-watermark',
                                                '--max-executions', '0', '--only-show-errors', '--output', 'none']])

    def test_app_present_with_changed_revision_is_restored_by_exact_copy(self):
        result, events, state = self.run_rollback({'app': True, 'image': NEW_IMAGE, 'revision': 'hhc-web-bulletin-worker--other'})
        self.assertEqual(result.returncode, 0, result.stderr)
        copies = [e for e in events if e[:3] == ['containerapp', 'revision', 'copy']]
        self.assertEqual(len(copies), 1)
        self.assertIn('hhc-web-bulletin-worker--previous', copies[0])
        self.assertIn(APP_IMAGE, copies[0])
        self.assertEqual(state['image'], APP_IMAGE)
        self.assertEqual(state['max_executions'], 0)
        self.assertFalse([e for e in events if e[:3] == ['deployment', 'group', 'create']])

    def test_app_already_deleted_is_recreated_from_bicep_with_previous_image(self):
        result, events, state = self.run_rollback({'app': False, 'ready_delay': 2})
        self.assertEqual(result.returncode, 0, result.stderr)
        params = state['deploy_params']
        self.assertEqual(params['deployWatermarkWorker'], 'true')
        self.assertEqual(params['deployWatermarkJob'], 'false')
        self.assertEqual(params['deployRuntime'], 'false')
        self.assertEqual(params['deployMigrationJob'], 'false')
        self.assertEqual(params['watermarkWorkerImage'], APP_IMAGE)
        self.assertEqual(params['runtimeImage'], API_IMAGE)
        self.assertEqual(params['bulletinWorkerAssetAuth'], 'dapr')
        self.assertEqual(state['max_executions'], 0)
        order = [e[:3] for e in self.writes(events)]
        self.assertEqual(order, [['deployment', 'group', 'create'], ['containerapp', 'job', 'update']],
                         'the App is ready before Job admission is paused')

    def test_after_completed_cutover_the_previous_job_image_recreates_the_app(self):
        result, _, state = self.run_rollback({'app': False}, PREVIOUS_WORKER_IMAGE_REF='', PREVIOUS_WORKER_REVISION='',
                                             PREVIOUS_WATERMARK_JOB_IMAGE_REF=JOB_IMAGE)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state['deploy_params']['watermarkWorkerImage'], JOB_IMAGE)
        self.assertEqual(state['image'], JOB_IMAGE)

    def test_job_absent_restores_app_without_job_writes(self):
        for app in (True, False):
            with self.subTest(app=app):
                result, events, _ = self.run_rollback({'app': app, 'job': False})
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertFalse([e for e in events if e[:2] == ['containerapp', 'job'] and e[2] != 'list'])

    def test_wrong_image_after_recreate_fails_and_keeps_job_running(self):
        result, events, state = self.run_rollback({'app': False, 'recreate_image': NEW_IMAGE})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(state['max_executions'], 20)
        self.assertFalse([e for e in events if e[:3] == ['containerapp', 'job', 'update']])

    def test_wrong_image_after_revision_copy_fails(self):
        result, _, state = self.run_rollback({'app': True, 'image': NEW_IMAGE, 'copy_image': NEW_IMAGE})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(state['max_executions'], 20)

    def test_app_never_ready_fails(self):
        result, _, state = self.run_rollback({'app': False, 'ready_delay': 1000})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(state['max_executions'], 20)

    def test_healthy_scaled_to_zero_app_can_take_over(self):
        result, _, state = self.run_rollback({'app': False, 'running': 'ScaledToZero'})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(state['max_executions'], 0)

    def test_unhealthy_recreated_app_keeps_job_running(self):
        for extra in ({'health': 'Unhealthy'}, {'active': False}, {'running': 'Failed'}):
            with self.subTest(extra=extra):
                result, events, state = self.run_rollback({'app': False, **extra})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(state['max_executions'], 20)
                self.assertFalse([e for e in events if e[:3] == ['containerapp', 'job', 'update']])

    def test_previous_image_missing_fails_without_any_write(self):
        result, events, state = self.run_rollback({'app': False}, PREVIOUS_WORKER_IMAGE_REF='', PREVIOUS_WATERMARK_JOB_IMAGE_REF='')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('No previous watermark worker image', result.stderr)
        self.assertEqual(events, [])
        self.assertEqual(state['max_executions'], 20)

    def test_invalid_inputs_fail_without_any_write(self):
        for overrides in ({'PREVIOUS_WORKER_IMAGE_REF': 'example.azurecr.io/example/hhc-web-api:main'},
                          {'PREVIOUS_WATERMARK_JOB_IMAGE_REF': 'docker.io/other@sha256:' + 'a' * 64},
                          {'RESOURCE_GROUP': ''},
                          {'WATERMARK_JOB_NAME': 'hhc-web-bulletin-extractor'},
                          {'GITHUB_RUN_ID': 'x'}):
            with self.subTest(overrides=overrides):
                result, events, _ = self.run_rollback({'app': False}, **overrides)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.writes(events), [])

    def test_recreate_requires_release_inputs(self):
        for missing in ('IMAGE_REF', 'RUNTIME_CPU', 'BULLETIN_WATERMARK_PRIVACY_VERSION'):
            with self.subTest(missing=missing):
                result, events, _ = self.run_rollback({'app': False}, **{missing: ''})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.writes(events), [])
        result, events, _ = self.run_rollback({'app': False}, BULLETIN_WORKER_ASSET_AUTH='workload')
        self.assertNotEqual(result.returncode, 0, 'workload auth without the identity is rejected')
        self.assertEqual(self.writes(events), [])

    def test_present_app_without_captured_revision_fails_without_write(self):
        result, events, _ = self.run_rollback({'app': True}, PREVIOUS_WORKER_IMAGE_REF='', PREVIOUS_WORKER_REVISION='',
                                              PREVIOUS_WATERMARK_JOB_IMAGE_REF=JOB_IMAGE)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.writes(events), [])

    def test_job_pause_readback_is_required(self):
        result, _, _ = self.run_rollback({'app': True, 'ignore_pause': True})
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('not paused', result.stderr)

    def test_release_wires_job_rollback_into_failure_path(self):
        text = Path('.github/workflows/release.yml').read_text()
        deploy = text.split('  deploy:')[1].split('  retained_deployment:')[0]
        self.assertIn('run: ./scripts/rollback-watermark-job.sh', deploy)
        self.assertNotIn('run: ./scripts/rollback-watermark-worker.sh', deploy)
        self.assertIn('PREVIOUS_WATERMARK_JOB_IMAGE_REF', deploy)


if __name__ == '__main__':
    unittest.main()
