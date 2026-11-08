#!/usr/bin/env python3
import test_no_cloud
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('runtime', Path(__file__).with_name('verify-release-runtime.py'))
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)
IMAGE = 'example.azurecr.io/example/hhc-web@sha256:' + 'a' * 64
APP = {'latest': 'hhc-web--candidate', 'ready': 'hhc-web--candidate', 'traffic': None, 'ingress': False}
REVISION = {'active': True, 'health': 'Healthy', 'provisioning': 'Provisioned', 'running': 'Running', 'images': [IMAGE]}


class RuntimeVerification(unittest.TestCase):
    def observe(self, app=None, revision=None, expected='hhc-web--candidate'):
        with patch.dict(os.environ, RESOURCE_GROUP='fixture'), patch.object(runtime, 'azure', side_effect=[app or APP, revision or REVISION]):
            return runtime.observe('hhc-web', expected, IMAGE)[0]

    def test_rollback_steps_require_explicit_negative_evidence(self):
        workflow = Path('.github/workflows/release.yml').read_text()
        self.assertNotIn('containerapp exec', workflow)
        for block in workflow.split('      - name: '):
            if block.startswith(('Roll back failed', 'Restore previous watermark', 'Disable and restore failed extractor', 'Verify rolled back runtime', 'Verify recovered runtime')):
                self.assertIn("steps.recovery.outputs.required == 'true'", block)

    def test_exact_healthy_revision_with_or_without_ingress(self):
        self.assertEqual(self.observe(), 0)
        for rule in [{'revisionName': APP['latest'], 'weight': 100}, {'latestRevision': True, 'weight': 100}]:
            self.assertEqual(self.observe(app={**APP, 'ingress': True, 'traffic': [rule]}), 0)

    def test_running_states_and_strict_traffic_schema(self):
        self.assertEqual(self.observe(revision={**REVISION, 'running': 'RunningAtMaxScale'}), 0)
        for running in ('Failed', 'Degraded'):
            self.assertEqual(self.observe(revision={**REVISION, 'running': running}), 10)
        for traffic in [[{'latestRevision': True, 'weight': 100}, {'revisionName': 'old', 'weight': 10}],
                        [{'latestRevision': True, 'weight': '100'}], [{'latestRevision': True, 'weight': True}],
                        [{'latestRevision': True, 'revisionName': 'old', 'weight': 100}],
                        [{'latestRevision': 'true', 'weight': 100}], [{'weight': 100}], [None]]:
            self.assertEqual(self.observe(app={**APP, 'ingress': True, 'traffic': traffic}), 20)

    def test_stale_identity_or_incomplete_traffic_is_unverified(self):
        for state in [{**APP, 'ready': 'old'}, {**APP, 'latest': 'old'}, {**APP, 'ingress': True, 'traffic': []},
                      {**APP, 'ingress': True, 'traffic': [{'revisionName': 'old', 'weight': 100}]}]:
            self.assertEqual(self.observe(app=state), 20)
        self.assertEqual(self.observe(revision={**REVISION, 'images': [IMAGE + 'b']}), 20)
        self.assertEqual(self.observe(revision={**REVISION, 'active': False}), 20)

    def test_known_application_negative(self):
        self.assertEqual(self.observe(revision={**REVISION, 'health': 'Unhealthy'}), 10)
        self.assertEqual(self.observe(revision={**REVISION, 'provisioning': 'Failed'}), 10)
        self.assertEqual(self.observe(revision={**REVISION, 'health': 'Unknown'}), 20)

    def test_recovery_requires_observed_failure_not_failed_request(self):
        with patch.dict(os.environ, {'FAILED_DEPLOYMENTS': '["deployment"]', 'RESOURCE_GROUP': 'fixture'}):
            with patch.object(runtime, 'azure', return_value='Failed'):
                self.assertEqual(runtime.recovery(), 10)
            with patch.object(runtime, 'azure', side_effect=ValueError('ARM429')):
                with self.assertRaises(ValueError):
                    runtime.recovery()

    def test_channel_failure_never_selects_rollback(self):
        for error in [ValueError('ARM429'), ValueError('ARM403'), KeyError('missing'), TypeError('malformed')]:
            with tempfile.TemporaryDirectory() as folder, patch.dict(os.environ, {'GITHUB_OUTPUT': folder + '/output'}), patch.object(runtime.sys, 'argv', ['runtime', 'recovery']), patch.object(runtime, 'recovery', side_effect=error):
                self.assertEqual(runtime.main(), 0)
                self.assertEqual(Path(folder, 'output').read_text(), 'required=false\n')

    def test_only_negative_selects_rollback(self):
        for result, required in [(0, 'false'), (10, 'true'), (20, 'false')]:
            with tempfile.TemporaryDirectory() as folder, patch.dict(os.environ, {'GITHUB_OUTPUT': folder + '/output'}), patch.object(runtime.sys, 'argv', ['runtime', 'recovery']), patch.object(runtime, 'recovery', return_value=result):
                self.assertEqual(runtime.main(), 0)
                self.assertEqual(Path(folder, 'output').read_text(), 'required=' + required + '\n')

    def test_confirmed_negative_stops_without_sleep_or_publishing_candidate(self):
        with tempfile.TemporaryDirectory() as folder, patch.dict(os.environ, {'RESOURCE_GROUP': 'fixture', 'GITHUB_ENV': folder + '/env', 'CANDIDATE_REVISION': APP['latest'], 'APP_NAME': 'hhc-web', 'IMAGE_REF': IMAGE}), patch.object(runtime.sys, 'argv', ['runtime']), patch.object(runtime, 'observe', wraps=runtime.observe) as probe, patch.object(runtime, 'azure', side_effect=[APP, {**REVISION, 'running': 'Failed'}]) as azure, patch.object(runtime.time, 'sleep') as sleep:
            self.assertEqual(runtime.main(), 10)
            self.assertEqual(probe.call_count, 1)
            self.assertEqual(azure.call_count, 2)
            sleep.assert_not_called()
            self.assertFalse(Path(folder, 'env').exists())

    def test_verification_pins_candidate_and_bounds_pending_state(self):
        with tempfile.TemporaryDirectory() as folder, patch.dict(os.environ, {'GITHUB_ENV': folder + '/env', 'APP_NAME': 'hhc-web', 'IMAGE_REF': IMAGE}), patch.object(runtime.sys, 'argv', ['runtime']), patch.object(runtime, 'observe', return_value=(0, APP['latest'])):
            self.assertEqual(runtime.main(), 0)
            self.assertEqual(Path(folder, 'env').read_text(), 'CANDIDATE_REVISION=' + APP['latest'] + '\n')
        with patch.dict(os.environ, {'APP_NAME': 'hhc-web', 'IMAGE_REF': IMAGE}), patch.object(runtime.sys, 'argv', ['runtime']), patch.object(runtime, 'observe', return_value=(20, APP['latest'])) as probe, patch.object(runtime.time, 'sleep') as sleep:
            self.assertEqual(runtime.main(), 20)
            self.assertEqual(probe.call_count, 60)
            self.assertEqual(sleep.call_count, 59)


class ConfigurationRecovery(unittest.TestCase):
    def test_confirmed_config_mismatch_and_unknown_metadata(self):
        app = {**APP, 'ingress': True, 'traffic': [{'latestRevision': True, 'weight': 100}]}
        revision = {**REVISION, 'cpu': 0.5, 'memory': '1Gi', 'automatic': [{'value': 'true', 'secretRef': None}]}
        env = {'RESOURCE_GROUP': 'fixture', 'RUNTIME_CPU': '0.5', 'RUNTIME_MEMORY': '1Gi', 'BULLETIN_AUTO_EXTRACTION_ENABLED': 'true'}
        for change, expected in [({}, 0), ({'cpu': 1}, 10), ({'memory': '2Gi'}, 10), ({'automatic': []}, 10),
                                 ({'automatic': [{'value': 'false', 'secretRef': None}]}, 10), ({'cpu': None}, 10)]:
            with patch.dict(os.environ, env), patch.object(runtime, 'azure', side_effect=[app, {**revision, **change}]):
                self.assertEqual(runtime.observe('hhc-web-api', APP['latest'], IMAGE)[0], 20)
            # Bind the profile's actual revision name before judging its configuration.
            bound = {**app, 'latest': 'hhc-web-api--candidate', 'ready': 'hhc-web-api--candidate'}
            with patch.dict(os.environ, env), patch.object(runtime, 'azure', side_effect=[bound, {**revision, **change}]):
                self.assertEqual(runtime.observe('hhc-web-api', bound['latest'], IMAGE)[0], expected)
        with patch.dict(os.environ, env), patch.object(runtime, 'azure', side_effect=[bound, {**revision, 'images': ['other'], 'cpu': 1}]):
            self.assertEqual(runtime.observe('hhc-web-api', bound['latest'], IMAGE)[0], 20)

    def test_actual_workflow_gate_failure_selects_recovery_only_for_readable_mismatch(self):
        workflow = Path('.github/workflows/release.yml').read_text()
        def step(name):
            block = workflow.split('      - name: ' + name + '\n', 1)[1].split('      - name: ', 1)[0]
            return '\n'.join(line[10:] for line in block.split('        run: |\n', 1)[1].splitlines())
        # One observation exercises the same configuration guard without 60 polling iterations.
        waiting = step('Wait for API revision').replace('{1..60}', '{1..1}')
        deciding = step('Classify recovery')
        image = IMAGE.replace('/hhc-web@', '/hhc-web-api@')
        for change, required in [({'cpu': '1'}, 'true'), ({'memory': '2Gi'}, 'true'), ({'flag': 'false'}, 'true'), ({'read_error': '1'}, 'false')]:
            with tempfile.TemporaryDirectory() as folder:
                tools = Path(folder, 'bin'); tools.mkdir()
                az = tools / 'az'
                az.write_text('''#!/bin/sh
if [ "$READ_ERROR" = 1 ]; then exit 1; fi
while [ "$1" != --query ]; do shift; done
case "$2" in
  properties.latestRevisionName|properties.latestReadyRevisionName) echo hhc-web-api--candidate ;;
  'properties.template.containers[0].image') printf '%s\\n' "$IMAGE_REF" ;;
  'properties.template.containers[0].resources.cpu') printf '%s\\n' "$ACTUAL_CPU" ;;
  'properties.template.containers[0].resources.memory') printf '%s\\n' "$ACTUAL_MEMORY" ;;
  *BULLETIN_AUTO_EXTRACTION_ENABLED*'| [0]') printf '%s\\n' "$ACTUAL_FLAG" ;;
  '{latest:'*) printf '%s\\n' "$APP_JSON" ;;
  '{active:'*) printf '%s\\n' "$REVISION_JSON" ;;
  *) exit 99 ;;
esac
'''); az.chmod(0o755)
                (tools / 'sleep').write_text('#!/bin/sh\nexit 0\n'); (tools / 'sleep').chmod(0o755)
                app = {'latest': 'hhc-web-api--candidate', 'ready': 'hhc-web-api--candidate', 'ingress': True, 'traffic': [{'latestRevision': True, 'weight': 100}]}
                actual = {'cpu': change.get('cpu', '0.5'), 'memory': change.get('memory', '1Gi'), 'flag': change.get('flag', 'true')}
                revision = {**REVISION, 'images': [image], 'cpu': float(actual['cpu']), 'memory': actual['memory'], 'automatic': [{'value': actual['flag'], 'secretRef': None}]}
                env = {**os.environ, 'PATH': str(tools) + os.pathsep + os.environ['PATH'], 'RESOURCE_GROUP': 'fixture', 'API_APP_NAME': 'hhc-web-api', 'IMAGE_REF': image,
                       'CANDIDATE_REVISION': '', 'GITHUB_ENV': folder + '/env', 'GITHUB_OUTPUT': folder + '/output',
                       'RUNTIME_CPU': '0.5', 'RUNTIME_MEMORY': '1Gi', 'BULLETIN_AUTO_EXTRACTION_ENABLED': 'true',
                       'RUNTIME_OUTCOME': 'success', 'WATERMARK_OUTCOME': 'skipped', 'EXTRACTOR_OUTCOME': 'skipped', 'GITHUB_RUN_ID': '1', 'GITHUB_RUN_ATTEMPT': '1',
                       'READ_ERROR': change.get('read_error', '0'), 'ACTUAL_CPU': actual['cpu'], 'ACTUAL_MEMORY': actual['memory'], 'ACTUAL_FLAG': actual['flag'],
                       'APP_JSON': json.dumps(app), 'REVISION_JSON': json.dumps(revision)}
                gate = runtime.subprocess.run(['bash', '-euo', 'pipefail', '-c', waiting], env=env, capture_output=True, text=True, timeout=30)
                self.assertNotEqual(gate.returncode, 0, change)
                decision = runtime.subprocess.run(['bash', '-euo', 'pipefail', '-c', deciding], env=env, capture_output=True, text=True, timeout=10)
                self.assertEqual(decision.returncode, 0, decision.stderr)
                self.assertEqual(Path(folder, 'output').read_text(), 'required=' + required + '\n', change)


if __name__ == '__main__':
    unittest.main()
