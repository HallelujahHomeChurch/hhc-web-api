#!/usr/bin/env python3
import test_no_cloud
"""Offline regression tests; an executable az blocker guards every subprocess."""
import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    loaded = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(loaded)
    return loaded
runtime = module('runtime_env', ROOT / 'scripts/runtime_env.py')
route = module('runtime_route', ROOT / '.github/scripts/ci-runtime-env.py')
EMPTY = {name: {} for name in runtime.RESOURCES}
IMAGE = 'example.azurecr.io/example/hhc-web-api@sha256:' + 'a' * 64

class RuntimeEnvironment(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.guard = tempfile.TemporaryDirectory()
        folder = Path(cls.guard.name)
        blocker = folder / 'az'
        blocker.write_text('#!/bin/sh\necho "Azure access blocked by offline tests" >&2\nexit 97\n')
        blocker.chmod(0o755)
        (folder / 'azure').mkdir()
        cls.environment = patch.dict(os.environ, {'PATH': str(folder) + os.pathsep + os.environ['PATH'],
            'AZ_BIN': str(blocker), 'AZURE_CONFIG_DIR': str(folder / 'azure')})
        cls.environment.start()

    @classmethod
    def tearDownClass(cls):
        cls.environment.stop()
        cls.guard.cleanup()

    def test_blocker_is_executable_and_azure_errors_are_redacted(self):
        self.assertEqual(subprocess.run(['az', 'account', 'show'], capture_output=True).returncode, 97)
        with self.assertRaisesRegex(ValueError, 'values withheld'):
            runtime.az('anything')

    def test_refresh_uses_latest_main_without_replacing_code(self):
        desired = copy.deepcopy(EMPTY)
        desired['hhc-web-api']['POOL'] = {'value': '27'}
        response = subprocess.CompletedProcess([], 0, json.dumps({'resources': desired}), '')
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / 'runtime-env.json'
            with patch.dict(os.environ, {'GITHUB_REF': 'refs/heads/main'}), patch.object(runtime, 'SOURCE', source), \
                 patch.object(runtime.subprocess, 'run', return_value=response) as command:
                runtime.refresh()
                self.assertEqual(runtime.load(), desired)
                self.assertIn('?ref=main', command.call_args.args[0][2])
                response.stdout = '{}'
                with self.assertRaises(ValueError):
                    runtime.refresh()
                self.assertEqual(runtime.load(), desired)

    def test_revision_secret_reference_normalization_is_exact(self):
        template = {'containers': [{'env': [{'name': 'TOKEN', 'secretRef': 'hhc-web-api-token'}]}]}
        normalized = runtime.normalize_secret_refs(template, 'hhc-web-api', {'token'})
        self.assertEqual(normalized['containers'][0]['env'][0]['secretRef'], 'token')
        self.assertEqual(template['containers'][0]['env'][0]['secretRef'], 'hhc-web-api-token')
        self.assertEqual(runtime.normalize_secret_refs(template, 'hhc-web-api', {'hhc-web-api-token'}), template)
        with self.assertRaises(ValueError):
            runtime.normalize_secret_refs(template, 'other-resource', {'token'})

    def test_release_identity_and_boolean_flag_contract(self):
        for name in ('RELEASE', 'BULLETIN_EXTRACTION_ENABLED', 'BULLETIN_AUTO_EXTRACTION_ENABLED', 'BULLETIN_LLM_EXTRACTION_ENABLED'):
            for value in (None, {'secretRef': 'flag'}, {'value': 'maybe'}):
                data = copy.deepcopy(EMPTY)
                data['hhc-web-api'][name] = value
                with self.assertRaises(ValueError):
                    runtime.parse(json.dumps({'resources': data}))
        desired = copy.deepcopy(EMPTY)
        desired['hhc-web-api']['BULLETIN_AUTO_EXTRACTION_ENABLED'] = {'value': 'false'}
        desired['hhc-web-bulletin-extractor']['BULLETIN_EXTRACTION_ENABLED'] = {'value': 'true'}
        self.assertEqual(runtime.parse(json.dumps({'resources': desired})), desired)
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'env'
            with patch.dict(os.environ, {'GITHUB_ENV': str(output)}), patch.object(runtime, 'load', return_value=desired):
                runtime.export_flags()
            self.assertEqual(output.read_text(), 'BULLETIN_AUTO_EXTRACTION_ENABLED=false\nBULLETIN_EXTRACTION_ENABLED=true\n')
        workflow = (ROOT / '.github/workflows/release.yml').read_text()
        self.assertEqual(workflow.count('python3 scripts/runtime_env.py export-flags'), 2)

    def test_llm_activation_is_shared_with_extractor(self):
        desired = copy.deepcopy(EMPTY)
        desired['hhc-web-api']['BULLETIN_LLM_EXTRACTION_ENABLED'] = {'value': 'true'}
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / 'env'
            with patch.dict(os.environ, {'GITHUB_ENV': str(output)}), patch.object(runtime, 'load', return_value=desired):
                with self.assertRaises(ValueError):
                    runtime.export_flags()
                desired['hhc-web-bulletin-extractor']['BULLETIN_LLM_EXTRACTION_ENABLED'] = {'value': 'true'}
                runtime.export_flags()
            self.assertEqual(output.read_text(), 'BULLETIN_LLM_EXTRACTION_ENABLED=true\n')

    def test_manifest_shape_and_secret_refs(self):
        data = copy.deepcopy(EMPTY)
        data['hhc-web-api'] = {'DB_MAX_OPEN_CONNS': {'value': '20'}, 'TOKEN': {'secretRef': 'existing'}, 'OLD': None}
        self.assertEqual(runtime.parse(json.dumps({'resources': data})), data)
        for value in ['20', {'value': 20}, {'secretRef': 'Bad Name'}, {'value': 'x', 'secretRef': 'y'}]:
            data['hhc-web-api']['DB_MAX_OPEN_CONNS'] = value
            with self.assertRaises(ValueError):
                runtime.parse(json.dumps({'resources': data}))
        with self.assertRaises(ValueError):
            runtime.parse('{"resources":{"other":{}}}')

    def test_preserve_entire_serving_template_except_owned_env(self):
        source = {'containers': [{'name': 'hhc-web-api', 'image': IMAGE, 'command': ['/server'],
                    'resources': {'cpu': 0.5, 'memory': '1Gi'},
                    'env': [{'name': 'KEEP', 'secretRef': 'private'}, {'name': 'OLD', 'value': '1'}]}],
                  'scale': {'minReplicas': 1, 'maxReplicas': 8}, 'revisionSuffix': 'original'}
        expected = copy.deepcopy(source)
        expected['containers'][0]['env'] = [{'name': 'KEEP', 'secretRef': 'private'}, {'name': 'POOL', 'value': '20'}]
        self.assertEqual(runtime.with_env(source, 'hhc-web-api', {'OLD': None, 'POOL': {'value': '20'}}), expected)
        self.assertEqual(source['containers'][0]['env'][1]['value'], '1')
        self.assertIsNone(runtime.with_env(source, 'hhc-web-api', {'OLD': {'value': '1'}}))

    def test_deploy_uses_serving_template_rest_patch_and_converges_all_owned_targets(self):
        desired = copy.deepcopy(EMPTY)
        desired['hhc-web-api']['POOL'] = {'value': '20'}
        desired['hhc-web-migrate']['POOL'] = {'value': '1'}
        serving = {'containers': [{'name': 'hhc-web-api', 'image': IMAGE, 'env': []}], 'scale': {'maxReplicas': 8}}
        current = {'id': '/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/containerApps/hhc-web-api',
                   'properties': {'configuration': {'activeRevisionsMode': 'Single'}, 'latestReadyRevisionName': 'ready',
                                  'template': {'containers': [{'name': 'hhc-web-api', 'image': 'wrong-latest'}]}}}
        job = {'id': '/subscriptions/test/resourceGroups/example-group/providers/Microsoft.App/jobs/hhc-web-migrate',
               'properties': {'configuration': {}, 'template': {'containers': [{'name': 'hhc-web-migrate', 'image': IMAGE, 'env': []}]}}}
        env = {'GITHUB_REF': 'refs/heads/main', 'GITHUB_SHA': 'a'*40, 'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '1', 'RUNTIME_ENV_BASE_SHA': 'b'*40}
        # Previous manifest already contains the values: a missed older deployment must converge too.
        with patch.dict(os.environ, env), patch.object(runtime, 'load', return_value=desired), \
             patch.object(runtime.subprocess, 'check_output', side_effect=['a'*40, 'a'*40+'\trefs/heads/main', json.dumps({'resources': desired})]), \
             patch.object(runtime, 'az', side_effect=[current, {'properties': {'template': serving}}, None, job, None]) as azure, patch.object(runtime, 'wait_for_template') as wait:
            runtime.deploy()
        self.assertEqual(wait.call_count, 2)
        app_patch = azure.call_args_list[2]
        self.assertEqual(app_patch.args[:3], ('rest', '--method', 'PATCH'))
        expected = copy.deepcopy(serving)
        expected['containers'][0]['env'] = [{'name': 'POOL', 'value': '20'}]
        expected['revisionSuffix'] = 'env-123-1'
        self.assertEqual(app_patch.kwargs['body'], {'properties': {'template': expected}})
        self.assertIn('@/dev/stdin', app_patch.args)
        self.assertNotIn('revisionSuffix', azure.call_args_list[4].kwargs['body']['properties']['template'])
        self.assertFalse(any('start' in c.args or 'copy' in c.args for c in azure.call_args_list))

    def test_superseded_commit_does_not_touch_azure(self):
        with patch.dict(os.environ, {'GITHUB_REF': 'refs/heads/main', 'GITHUB_SHA': 'a'*40}), \
             patch.object(runtime, 'load', return_value=EMPTY), \
             patch.object(runtime.subprocess, 'run'), \
             patch.object(runtime.subprocess, 'check_output', side_effect=['a'*40, 'b'*40+'\trefs/heads/main', json.dumps({'resources': {**EMPTY, 'hhc-web-api': {'POOL': {'value': '20'}}}})]), \
             patch.object(runtime, 'az') as azure:
            runtime.deploy()
            azure.assert_not_called()

    def test_missing_secret_ref_fails_before_write(self):
        desired = copy.deepcopy(EMPTY)
        desired['hhc-web-api']['TOKEN'] = {'secretRef': 'missing'}
        with patch.dict(os.environ, {'GITHUB_REF': 'refs/heads/main', 'GITHUB_SHA': 'a'*40, 'RUNTIME_ENV_BASE_SHA': 'b'*40}), \
             patch.object(runtime, 'load', return_value=desired), \
             patch.object(runtime.subprocess, 'check_output', side_effect=['a'*40, 'a'*40+'\trefs/heads/main', json.dumps({'resources': EMPTY})]), \
             patch.object(runtime, 'az', return_value={'properties': {'configuration': {'secrets': []}}}) as azure:
            with self.assertRaises(ValueError): runtime.deploy()
            self.assertEqual(azure.call_count, 1)

    def test_classify_config_mixed_and_code(self):
        previous = os.getcwd()
        with tempfile.TemporaryDirectory() as directory:
            os.chdir(directory)
            try:
                def git(*args):
                    return subprocess.check_output(['git', *args], text=True, stderr=subprocess.DEVNULL).strip()
                git('init'); git('config', 'user.email', 'test@example.invalid'); git('config', 'user.name', 'Test')
                Path('infra').mkdir(); Path('infra/runtime-env.json').write_text('{}')
                git('add', '.'); git('commit', '-m', 'base'); base=git('rev-parse', 'HEAD')
                Path('infra/runtime-env.json').write_text('{"resources":{}}')
                git('add', '.'); git('commit', '-m', 'config'); head=git('rev-parse', 'HEAD')
                self.assertTrue(route.config_only('pull_request', base, head))
                Path('main.go').write_text('package main')
                git('add', '.'); git('commit', '-m', 'code'); mixed=git('rev-parse', 'HEAD')
                self.assertFalse(route.config_only('pull_request', base, mixed))
                self.assertFalse(route.config_only('pull_request', head, mixed))
                self.assertFalse(route.config_only('workflow_dispatch', base, mixed))
                for name in ['README.md', 'example_test.go', '.github/workflows/ci.yml']:
                    git('reset', '--hard', head)
                    path = Path(name); path.parent.mkdir(parents=True, exist_ok=True); path.write_text('changed')
                    git('add', '.'); git('commit', '-m', 'mixed')
                    self.assertFalse(route.config_only('push', base, git('rev-parse', 'HEAD')))
            finally:
                os.chdir(previous)

    def test_newer_docs_head_with_same_manifest_still_applies(self):
        with patch.dict(os.environ, {'GITHUB_REF': 'refs/heads/main', 'GITHUB_SHA': 'a'*40, 'RUNTIME_ENV_BASE_SHA': 'c'*40}), \
             patch.object(runtime, 'load', return_value=EMPTY), \
             patch.object(runtime.subprocess, 'run') as fetch, \
             patch.object(runtime.subprocess, 'check_output', side_effect=['a'*40, 'b'*40+'\trefs/heads/main', json.dumps({'resources': EMPTY}), json.dumps({'resources': EMPTY})]) as git, \
             patch.object(runtime, 'az') as azure:
            runtime.deploy()
            fetch.assert_called_once()
            self.assertEqual(git.call_count, 4)
            azure.assert_not_called()

    def test_forward_verifier_honors_manifest_without_changing_retained_mode(self):
        verifier = module('runtime_verifier', ROOT / 'scripts/verify-release-runtime.py')
        state = {'latest': 'hhc-web-api--candidate', 'ready': 'hhc-web-api--candidate', 'ingress': True,
                 'traffic': [{'latestRevision': True, 'weight': 100}]}
        revision = {'images': [IMAGE], 'active': True, 'health': 'Healthy', 'provisioning': 'Provisioned',
                    'running': 'Running', 'cpu': 0.5, 'memory': '1Gi', 'automatic': [{'value': 'false', 'secretRef': None}]}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/'env.json'
            data = copy.deepcopy(EMPTY); data['hhc-web-api']['BULLETIN_AUTO_EXTRACTION_ENABLED'] = {'value': 'false'}
            path.write_text(json.dumps({'resources': data}))
            with patch.dict(os.environ, {'RESOURCE_GROUP': 'fixture', 'RUNTIME_CPU': '0.5', 'RUNTIME_MEMORY': '1Gi',
                                        'BULLETIN_AUTO_EXTRACTION_ENABLED': 'true', 'RUNTIME_ENV_MANIFEST': str(path)}), \
                 patch.object(verifier, 'azure', side_effect=[state, revision]):
                self.assertEqual(verifier.observe('hhc-web-api', 'hhc-web-api--candidate', IMAGE)[0], 0)
            with patch.dict(os.environ, {'RESOURCE_GROUP': 'fixture', 'RUNTIME_CPU': '0.5', 'RUNTIME_MEMORY': '1Gi',
                                        'BULLETIN_AUTO_EXTRACTION_ENABLED': 'true', 'RUNTIME_ENV_MANIFEST': ''}), \
                 patch.object(verifier, 'azure', side_effect=[state, revision]):
                self.assertEqual(verifier.observe('hhc-web-api', 'hhc-web-api--candidate', IMAGE)[0], 10)

    def test_workflow_routing_and_same_source(self):
        ci=(ROOT/'.github/workflows/ci.yml').read_text()
        release=(ROOT/'.github/workflows/release.yml').read_text()
        fast=release.split('  runtime_env:', 1)[1].split('  configuration:', 1)[0]
        bicep=(ROOT/'infra/main.bicep').read_text()
        self.assertIn("always() && needs.changes.outputs.config_only != 'true'", ci)
        self.assertNotIn('      - infra/runtime-env.json', release)
        self.assertEqual(release.count('concurrency:'), 1)
        self.assertIn('  group: hhc-web-api-production-actions', release)
        self.assertIn("needs.agent_docs.outputs.config_only == 'true'", fast)
        for forbidden in ['docker ', 'smoke', 'migrate', 'test-', 'scan-image']:
            self.assertNotIn(forbidden, fast)
        for resource in runtime.RESOURCES:
            self.assertIn("configuredEnv['" + resource + "']", bicep)
        self.assertNotIn('func runtimeEnv', bicep)

    def test_provisioning_failure_is_not_reported_as_success(self):
        with patch.object(runtime, 'az', return_value={'properties': {'provisioningState': 'Failed'}}), patch.object(runtime.time, 'sleep'):
            with self.assertRaisesRegex(ValueError, 'provisioning failed'):
                runtime.wait_for_template('hhc-web-api', 'app', {}, 'hhc-web-api--env-1-1')
        with patch.object(runtime, 'az', return_value={'properties': {'provisioningState': 'Succeeded', 'template': {'containers': []}}}):
            runtime.wait_for_template('hhc-web-migrate', 'job', {'containers': []}, None)

    def test_refresh_records_exact_config_blob(self):
        import contextlib, io
        raw = json.dumps({'resources': EMPTY}) + '\n'
        expected = subprocess.check_output(['git', 'hash-object', '--stdin'], input=raw, text=True).strip()
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {'GITHUB_REF': 'refs/heads/main'}), patch.object(runtime, 'SOURCE', Path(directory)/'manifest.json'), patch.object(runtime.subprocess, 'run', return_value=subprocess.CompletedProcess([], 0, raw, '')):
            output = io.StringIO()
            with contextlib.redirect_stdout(output): runtime.refresh()
            self.assertEqual(runtime.SOURCE.read_text(), raw)
            self.assertIn(expected, output.getvalue())

    def test_cli_rejects_deleted_ownership_even_with_code_change(self):
        import shutil
        root=Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory() as directory:
            fixture=Path(directory)
            for name in ['.github/scripts','scripts','infra']:
                (fixture/name).mkdir(parents=True,exist_ok=True)
            for name in ['.github/scripts/ci-agent-docs.py','.github/scripts/ci-runtime-env.py','scripts/runtime_env.py','scripts/runtime-env.py']:
                if (root/name).exists():shutil.copyfile(root/name,fixture/name)
            document=json.loads((root/'infra/runtime-env.json').read_text())
            target=next(iter(document['resources']))
            document['resources'][target]['POOL']={'value':'20'}
            source=fixture/'infra/runtime-env.json';source.write_text(json.dumps(document))
            def git(*args):
                return subprocess.check_output(['git',*args],cwd=fixture,text=True,stderr=subprocess.DEVNULL).strip()
            git('init');git('config','user.name','Fixture');git('config','user.email','fixture@example.invalid')
            git('add','.');git('commit','-qm','base');base=git('rev-parse','HEAD')
            document['resources'][target]={};source.write_text(json.dumps(document));(fixture/'code.go').write_text('package fixture')
            git('add','.');git('commit','-qm','deleted ownership')
            command=(['python3','scripts/runtime-env.py','classify'] if (fixture/'scripts/runtime-env.py').exists() else ['python3','.github/scripts/ci-runtime-env.py'])
            result=subprocess.run(command+['push',base,git('rev-parse','HEAD')],cwd=fixture,capture_output=True,text=True)
            self.assertNotEqual(result.returncode,0,'Mixed changes must reject deletion instead of restoring release defaults')

if __name__ == '__main__':
    unittest.main()
