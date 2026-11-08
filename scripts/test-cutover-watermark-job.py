#!/usr/bin/env python3
"""Fixture tests for the watermark Job preflight and App retirement step."""
import copy
import test_no_cloud
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

IMAGE = 'example.azurecr.io/example/hhc-web-api@sha256:' + 'b' * 64
SHA = 'c' * 40
SCOPE = '/subscriptions/11111111-1111-4111-8111-111111111111/resourceGroups/example-group/providers/'
IDENTITY = SCOPE + 'Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-bulletin-worker-identity'
CLIENT = '00000000-0000-4000-8000-000000000002'


def job():
    env = {'ENVIRONMENT': 'production', 'RELEASE': SHA, 'DB_MAX_OPEN_CONNS': '1', 'DB_MAX_IDLE_CONNS': '1',
           'DB_CONN_MAX_LIFETIME': '30m', 'ALLOW_DEV_CALLER_HEADER': 'false', 'CMS_TRANSLATION_ENABLED': 'false',
           'BULLETIN_WATERMARK_PRIVACY_VERSION': 'privacy-test',
           'BULLETIN_WATERMARK_QUEUE_URL': 'https://examplestorage.queue.core.windows.net/bulletin-watermark',
           'BULLETIN_WATERMARK_POISON_QUEUE_URL': 'https://examplestorage.queue.core.windows.net/bulletin-watermark-poison',
           'BULLETIN_WATERMARK_DIRECT_DECODE_ENABLED': 'false', 'AZURE_CLIENT_ID': CLIENT,
           'BULLETIN_WORKER_ASSET_AUTH': 'workload',
           'BULLETIN_WORKER_ASSET_API_BASE_URL': 'https://asset-api.internal.example.eastasia.azurecontainerapps.io',
           'BULLETIN_WORKER_ASSET_API_AUDIENCE': 'api://asset-api', 'BULLETIN_WORKER_ASSET_CLIENT_ID': CLIENT}
    return {
        'id': SCOPE + 'Microsoft.App/jobs/hhc-web-bulletin-watermark',
        'identity': {'type': 'UserAssigned', 'userAssignedIdentities': {IDENTITY.lower(): {}}},
        'properties': {
            'environmentId': SCOPE + 'Microsoft.App/managedEnvironments/example-env', 'workloadProfileName': 'Consumption',
            'configuration': {
                'triggerType': 'Event', 'replicaTimeout': 300, 'replicaRetryLimit': 0,
                'manualTriggerConfig': None, 'scheduleTriggerConfig': None, 'dapr': None, 'ingress': None,
                'eventTriggerConfig': {'parallelism': 1, 'replicaCompletionCount': 1, 'scale': {
                    'pollingInterval': 10, 'minExecutions': 0, 'maxExecutions': 0, 'rules': [{
                        'name': 'bulletin-watermark-queue', 'type': 'azure-queue', 'identity': IDENTITY, 'auth': [],
                        'metadata': {'accountName': 'examplestorage', 'queueName': 'bulletin-watermark',
                                     'queueLength': '1', 'queueLengthStrategy': 'visibleonly'}}]}},
                'registries': [{'server': 'example.azurecr.io', 'identity': IDENTITY, 'username': '', 'passwordSecretRef': ''}],
                'secrets': [{'name': 'database-url', 'identity': IDENTITY,
                             'keyVaultUrl': 'https://example-runtime-kv.vault.azure.net/secrets/jobs-database-url'}]},
            'template': {'initContainers': None, 'volumes': None, 'containers': [{
                'name': 'bulletin-watermark', 'image': IMAGE, 'command': ['/hhc-web-bulletin-worker'], 'args': ['--once'],
                'resources': {'cpu': 2.0, 'memory': '4Gi', 'ephemeralStorage': '8Gi'},
                'env': [{'name': 'DATABASE_URL', 'secretRef': 'database-url'}] + [{'name': k, 'value': v} for k, v in env.items()]}]}}}


STUB = r'''
import json, os, sys
from pathlib import Path
args = sys.argv[1:]
state_path = Path(os.environ['STUB_STATE'])
state = json.loads(state_path.read_text())
with open(os.environ['STUB_EVENTS'], 'a') as events:
    events.write(json.dumps(args) + '\n')
def save(): state_path.write_text(json.dumps(state))
scale = state['job']['properties']['configuration']['eventTriggerConfig']['scale']
if args[:3] == ['containerapp', 'job', 'show'] and '--query' in args:
    print(scale['maxExecutions'])
elif args[:3] == ['containerapp', 'job', 'show']:
    if state.get('job_read_fails'):
        sys.exit(1)
    print(json.dumps(state['job']))
elif args[:3] == ['containerapp', 'job', 'update']:
    maximum = int(args[args.index('--max-executions') + 1])
    if maximum == 0 and state.get('pause_fails'):
        sys.exit(1)
    if maximum != 0 and state.get('activation_fails'):
        sys.exit(1)
    if maximum != 0 and state.get('activation_ignored'):
        sys.exit(0)
    scale['maxExecutions'] = maximum; save()
elif args[:3] == ['containerapp', 'job', 'start']:
    template = json.loads(Path(args[args.index('--yaml') + 1]).read_text())
    state['started_template'] = template; save()
    if state.get('start_fails'):
        sys.exit(1)
    print(state.get('execution', 'hhc-web-bulletin-watermark-pre1'))
elif args[:4] == ['containerapp', 'job', 'execution', 'show']:
    if state.get('execution_read_fails'):
        sys.exit(1)
    statuses = state['statuses']
    status = statuses.pop(0) if len(statuses) > 1 else statuses[0]
    save(); print(status)
elif args[:4] == ['containerapp', 'job', 'logs', 'show']:
    print('{"msg":"bulletin watermark preflight","ok":false,"asset":"denied"}')
elif args[:2] == ['containerapp', 'list']:
    if state['app'] and state.get('delete_requested') and not state.get('delete_stuck'):
        state['app'] = False; save()
    print(1 if state['app'] else 0)
elif args[:2] == ['containerapp', 'show']:
    print('example.azurecr.io/example/hhc-web-api@sha256:' + 'a' * 64)
elif args[:2] == ['containerapp', 'delete']:
    if state.get('delete_fails'):
        sys.exit(1)
    state['delete_requested'] = True; save()
else:
    print('unexpected az call: ' + ' '.join(args), file=sys.stderr); sys.exit(2)
'''


class CutoverWatermarkJob(unittest.TestCase):
    def run_cutover(self, *, mutate=None, statuses=('Running', 'Succeeded'), app=True, maximum=None, **extra):
        payload = job()
        if mutate:
            mutate(payload)
        state = {'job': payload, 'statuses': list(statuses), 'app': app, **extra}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'bin').mkdir()
            stub = root / 'bin' / 'az'
            stub.write_text('#!' + sys.executable + '\n' + STUB)
            stub.chmod(0o755)
            (root / 'state.json').write_text(json.dumps(state))
            env = {**os.environ, 'PATH': str(root / 'bin') + os.pathsep + os.environ['PATH'],
                   'STUB_STATE': str(root / 'state.json'), 'STUB_EVENTS': str(root / 'events'),
                   'RESOURCE_GROUP': 'example-group', 'WATERMARK_WORKER_APP_NAME': 'hhc-web-bulletin-worker',
                   'WATERMARK_JOB_NAME': 'hhc-web-bulletin-watermark', 'WATERMARK_IMAGE_REF': IMAGE,
                   'GITHUB_SHA': SHA, 'WATERMARK_PREFLIGHT_POLL_SECONDS': '0', 'GITHUB_ENV': str(root / 'github-env')}
            if maximum is not None:
                env['WATERMARK_JOB_MAX_EXECUTIONS'] = maximum
            result = subprocess.run(['bash', 'scripts/cutover-watermark-job.sh'], env=env, capture_output=True, text=True, timeout=60)
            events = [json.loads(line) for line in (root / 'events').read_text().splitlines()] if (root / 'events').exists() else []
            final = json.loads((root / 'state.json').read_text())
            final['github_env'] = (root / 'github-env').read_text() if (root / 'github-env').exists() else ''
            return result, events, final

    @staticmethod
    def called(events, *prefix):
        return [e for e in events if e[:len(prefix)] == list(prefix)]

    def test_preflight_success_retires_app_after_logging_its_digest(self):
        result, events, state = self.run_cutover()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.max_executions(state), 20)
        updates = self.called(events, 'containerapp', 'job', 'update')
        self.assertEqual(len(updates), 1)
        self.assertEqual(updates[0][updates[0].index('--max-executions') + 1], '20')
        template = state['started_template']['containers'][0]
        self.assertEqual(template['args'], ['--preflight'])
        self.assertEqual(template['image'], IMAGE)
        self.assertEqual(template['command'], ['/hhc-web-bulletin-worker'])
        self.assertIn({'name': 'DATABASE_URL', 'secretRef': 'database-url'}, template['env'])
        self.assertEqual(template['resources'], {'cpu': 2.0, 'memory': '4Gi'})
        self.assertIn('last image (keep in ACR for rollback): sha256:' + 'a' * 64, result.stdout)
        start = events.index(self.called(events, 'containerapp', 'job', 'start')[0])
        delete = events.index(self.called(events, 'containerapp', 'delete')[0])
        self.assertLess(start, delete)
        self.assertIn('--yes', events[delete])
        self.assertFalse(state['app'])

    def test_explicit_maximum_covers_previous_and_expanded_range(self):
        for maximum in ('1', '8', '19'):
            with self.subTest(maximum=maximum):
                result, _, state = self.run_cutover(maximum=maximum, app=False)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(self.max_executions(state), int(maximum))

    def test_invalid_maximum_is_rejected_before_cloud_calls(self):
        for maximum in ('0', '21', '-1', '01', '1.0', '20x'):
            with self.subTest(maximum=maximum):
                result, events, _ = self.run_cutover(maximum=maximum)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(events, [])

    def test_app_already_retired_is_not_deleted_again(self):
        result, events, _ = self.run_cutover(app=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.called(events, 'containerapp', 'delete'))

    def test_failed_or_timed_out_preflight_keeps_app(self):
        for statuses in (('Running', 'Failed'), ('Degraded',), ('Stopped',), ('Running',)):
            with self.subTest(statuses=statuses):
                result, events, state = self.run_cutover(statuses=statuses)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.called(events, 'containerapp', 'delete'))
                self.assertTrue(state['app'])
                self.assertTrue(self.called(events, 'containerapp', 'job', 'logs', 'show'))
                self.assertEqual(self.max_executions(state), 0, 'a failed preflight pauses Job admission')

    @staticmethod
    def max_executions(state):
        return state['job']['properties']['configuration']['eventTriggerConfig']['scale']['maxExecutions']

    def test_unstarted_preflight_keeps_app(self):
        for extra in ({'execution': ''}, {'start_fails': True}):
            with self.subTest(extra=extra):
                result, events, state = self.run_cutover(**extra)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.called(events, 'containerapp', 'delete'))
                self.assertEqual(self.max_executions(state), 0)

    def test_mismatched_or_unsafe_job_is_never_started(self):
        mutations = {
            'old image': lambda j: j['properties']['template']['containers'][0].update(image='example.azurecr.io/example/hhc-web-api@sha256:' + 'f' * 64),
            'other release': lambda j: [e.update(value='d' * 40) for e in j['properties']['template']['containers'][0]['env'] if e['name'] == 'RELEASE'],
            'active admission': lambda j: j['properties']['configuration']['eventTriggerConfig']['scale'].update(maxExecutions=20),
            'loop mode': lambda j: j['properties']['template']['containers'][0].update(args=[]),
            'dapr enabled': lambda j: j['properties']['configuration'].update(dapr={'enabled': True}),
            'api identity attached': lambda j: j['identity']['userAssignedIdentities'].update({SCOPE + 'Microsoft.ManagedIdentity/userAssignedIdentities/hhc-web-api-identity': {}}),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                result, events, state = self.run_cutover(mutate=mutate)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.called(events, 'containerapp', 'job', 'start'))
                self.assertFalse(self.called(events, 'containerapp', 'delete'))
                self.assertTrue(state['app'])
                self.assertEqual(self.max_executions(state), 0)

    def test_app_deletion_must_complete(self):
        result, _, state = self.run_cutover(delete_stuck=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('still present', result.stderr)
        self.assertEqual(self.max_executions(state), 20, 'a proven Job keeps serving until the App is restored')
        self.assertIn('WATERMARK_RECOVERY_REQUIRED=true', state['github_env'])

    def test_unhandled_observation_and_activation_failures_keep_app_and_pause_job(self):
        for extra in ({'job_read_fails': True}, {'execution_read_fails': True},
                      {'activation_fails': True}, {'activation_ignored': True}):
            with self.subTest(extra=extra):
                result, events, state = self.run_cutover(**extra)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(self.called(events, 'containerapp', 'delete'))
                self.assertTrue(state['app'])
                self.assertEqual(self.max_executions(state), 0)

    def test_failed_pause_is_reported_without_retiring_app(self):
        result, events, state = self.run_cutover(mutate=lambda j: j['properties']['configuration']['eventTriggerConfig']['scale'].update(maxExecutions=20), pause_fails=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('could not be paused', result.stderr)
        self.assertFalse(self.called(events, 'containerapp', 'delete'))
        self.assertTrue(state['app'])

    def test_failed_delete_requests_worker_recovery_even_when_api_is_healthy(self):
        result, _, state = self.run_cutover(delete_fails=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('WATERMARK_RECOVERY_REQUIRED=true', state['github_env'])
        workflow = Path('.github/workflows/release.yml').read_text()
        step = workflow.split('      - name: Roll back watermark Job to the previous worker App\n')[1].split('      - name: ')[0]
        self.assertIn("steps.recovery.outputs.required == 'true' || env.WATERMARK_RECOVERY_REQUIRED == 'true'", step)

    def test_invalid_inputs_make_no_calls(self):
        for key, value in (('RESOURCE_GROUP', ''), ('WATERMARK_IMAGE_REF', 'example.azurecr.io/example/hhc-web-api:main'), ('GITHUB_SHA', 'main')):
            with self.subTest(key=key):
                payload_env = {key: value}
                result = subprocess.run(['bash', 'scripts/cutover-watermark-job.sh'], capture_output=True, text=True,
                                        env={**os.environ, 'RESOURCE_GROUP': 'example-group', 'WATERMARK_WORKER_APP_NAME': 'hhc-web-bulletin-worker',
                                             'WATERMARK_JOB_NAME': 'hhc-web-bulletin-watermark', 'WATERMARK_IMAGE_REF': IMAGE,
                                             'GITHUB_SHA': SHA, 'PATH': '/nonexistent:/usr/bin:/bin', **payload_env})
                self.assertNotEqual(result.returncode, 0)

    def test_release_runs_cutover_only_from_the_watermark_step(self):
        text = Path('.github/workflows/release.yml').read_text()
        step = text.split('      - name: Deploy event-driven bulletin worker\n')[1].split('      - name: ')[0]
        self.assertIn('deployWatermarkWorker=false deployWatermarkJob=true watermarkJobMaxExecutions=0 bulletinWorkerIdentityEnabled=true', step)
        self.assertIn('./scripts/cutover-watermark-job.sh', step)
        self.assertEqual(text.count('cutover-watermark-job.sh'), 1)
        self.assertNotIn('deployWatermarkWorker=true', text)


if __name__ == '__main__':
    unittest.main()
