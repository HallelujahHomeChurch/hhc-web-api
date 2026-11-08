#!/usr/bin/env python3
"""Fixture-only maintenance and retained-deployment acceptance checks."""
import test_no_cloud
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

FIXTURE = r'''
import json, os, pathlib, sys
args=sys.argv[1:]; name=pathlib.Path(sys.argv[0]).name
root=pathlib.Path(os.environ['FIXTURE_ROOT']);case=os.environ['CASE']
with (root/'events').open('a') as f:f.write(name+' '+json.dumps(args)+'\n')
if name=='timeout':
 print('READY_OK\nPUBLIC_OK\nHTTP/1.1 401 Unauthorized');sys.exit(0)
if name=='gh':
 print(json.dumps({'jobs':[{'name':'deploy','conclusion':'failure' if case=='failed-deploy' else 'success','head_sha':'b'*40 if case=='wrong-head' else os.environ['DEPLOYED_COMMIT']}]}));sys.exit(0)
if name=='git':sys.exit(1 if case=='unrelated-source' else 0)
if name=='az':
 if 'hhc-web-bulletin-watermark' in args:
  print(json.dumps({'image':'wrong' if case=='changed-worker-image' else os.environ['DEPLOYED_WORKER_IMAGE'],'args':['--once'],
                    'release':['b'*40 if case=='changed-worker-release' else os.environ['DEPLOYED_COMMIT']],'max':0 if case=='paused-worker' else 8 if case=='old-worker-maximum' else 20}))
  sys.exit(0)
 if args[:2]==['containerapp','list']:
  print(json.dumps(1 if case=='worker-app-restored' else 0));sys.exit(0)
 if args[:3]==['containerapp','job','show']:
  late=case=='late-extractor' and int((root/'identity-count').read_text())>1
  print(json.dumps({'image':'wrong' if case=='changed-extractor' or late else os.environ['DEPLOYED_EXTRACTOR_IMAGE'],'enabled':['true' if case=='changed-extractor-enabled' else os.environ['DEPLOYED_EXTRACTOR_ENABLED']],'trigger':'Event','max':1}));sys.exit(0)
 if args[:3]==['containerapp','revision','list']:
  rows=[{'name':'gateway--one','active':True,'traffic':100}]
  if case=='no-gateway':rows=[]
  if case=='mixed-gateway':rows=[{'name':'gateway--one','active':True,'traffic':50},{'name':'gateway--two','active':True,'traffic':50}]
  if case=='missing-traffic':rows[0].pop('traffic')
  if case=='invalid-traffic':rows[0]['traffic']='100'
  if case=='partial-traffic':rows[0]['traffic']=50
  if case=='excess-traffic':rows[0]['traffic']=150
  print(json.dumps(rows))
 elif args[:3]==['containerapp','revision','show'] and any(a.startswith('gateway--') for a in args):
  if 'properties.template.containers[].name' in args:print(json.dumps(['api-gateway']));sys.exit(0)
  if case=='mixed-gateway':print(json.dumps(['on' if 'gateway--one' in args else 'off']));sys.exit(0)
  print(json.dumps(['on' if case in ('pending','bad503','wrong-header','unexpected200') else 'invalid' if case=='unknown-mode' else 'off']))
 elif args[:2]==['containerapp','show']:
  counter=root/'identity-count';n=int(counter.read_text())+1 if counter.exists() else 1;counter.write_text(str(n))
  rev='hhc-web-api--changed' if case=='changed-revision' or case=='late-revision' and n>1 else os.environ['DEPLOYED_REVISION']
  print(json.dumps({'latest':rev,'ready':rev}))
 else:print(json.dumps({'active':True,'health':'Healthy','running':'Running','traffic':100,'image': 'wrong' if case=='wrong-image' else os.environ['DEPLOYED_IMAGE']}))
 sys.exit(0)
if name=='curl':
 if '--output' not in args:sys.exit(22)
 body={'error':'Service Unavailable','service':'api-gateway'} if case!='bad503' else {'error':'unrelated'}
 pathlib.Path(args[args.index('--output')+1]).write_text(json.dumps(body))
 pathlib.Path(args[args.index('--dump-header')+1]).write_text('Content-Type: application/json\r\nCache-Control: private, no-store\r\n'+('' if case=='wrong-header' else 'Retry-After: 300\r\n'))
 print('200' if case=='unexpected200' else '503',end='');sys.exit(0)
if name=='smoke-release.sh' or name=='verify-rollout.sh':
 assert os.environ['RELEASE_PUBLIC_EXPECTATION']=='normal' and os.environ['SMOKE_MODE']=='forward'
 sys.exit(1 if case=='public-failure' else 0)
raise SystemExit(2)
'''

class Acceptance(unittest.TestCase):
    def run_case(self, case, expected, state='pending', missing=None):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);(root/'scripts').mkdir();(root/'bin').mkdir()
            for name in ('accept-release.sh','maintenance-contract.sh','deployment_config.py'):
                shutil.copyfile(Path('scripts')/name,root/'scripts'/name)
            fixture=root/'fixture';fixture.write_text('#!'+sys.executable+'\n'+FIXTURE);fixture.chmod(0o755)
            for name in ('gh','az','curl','git'):(root/'bin'/name).symlink_to(fixture)
            for name in ('smoke-release.sh','verify-rollout.sh'):(root/'scripts'/name).symlink_to(fixture)
            env={**os.environ,'PATH':str(root/'bin')+os.pathsep+os.environ['PATH'],'FIXTURE_ROOT':str(root),'CASE':case,
                 'DEPLOYED_COMMIT':'a'*40,'DEPLOYED_IMAGE':'example.azurecr.io/example/hhc-web-api@sha256:'+'c'*64,
                 'DEPLOYED_WORKER_IMAGE':'example.azurecr.io/example/hhc-web-api@sha256:'+'d'*64,'DEPLOYED_WORKER_JOB':'hhc-web-bulletin-watermark@'+'a'*40,'DEPLOYED_EXTRACTOR_IMAGE':'example.azurecr.io/example/hhc-web-bulletin-extractor@sha256:'+'e'*64,'DEPLOYED_EXTRACTOR_ENABLED':'false',
                 'DEPLOYED_REVISION':'hhc-web-api--candidate','DEPLOYED_ATTEMPT':'1','DEPLOYED_PUBLIC_STATE':state,
                 'GITHUB_SHA':'a'*40,'GITHUB_RUN_ID':'42','GITHUB_RUN_ATTEMPT':'2','GITHUB_REPOSITORY':'HallelujahHomeChurch/hhc-web-api',
                 'GITHUB_REF':'refs/heads/main','GITHUB_OUTPUT':str(root/'output'),'RESOURCE_GROUP':'fixture','API_APP_NAME':'hhc-web-api','ROLLOUT_EVIDENCE_DIR':str(root/'evidence')}
            if case in ('cross-run','unrelated-source','cross-run-same-origin'):
                env.update(GITHUB_SHA='b'*40,DEPLOYED_RUN_ID='42' if case=='cross-run-same-origin' else '77')
            if case=='normal':env['GITHUB_RUN_ATTEMPT']='1'
            if missing:env.pop(missing)
            if case=='future-attempt':env['DEPLOYED_ATTEMPT']='3'
            if case=='wrong-worker-job':env['DEPLOYED_WORKER_JOB']='hhc-web-bulletin-worker--original'
            if case=='wrong-repository':env['GITHUB_REPOSITORY']='other/repo'
            result=subprocess.run(['bash','scripts/accept-release.sh'],cwd=root,env=env,capture_output=True,text=True,timeout=15)
            self.assertEqual(result.returncode,expected,result.stdout+result.stderr)
            events=(root/'events').read_text() if (root/'events').exists() else ''
            output=(root/'output').read_text() if (root/'output').exists() else ''
            if expected==0:
                self.assertIn('commit='+'a'*40,output)
                self.assertIn('acceptance-attempt='+env['GITHUB_RUN_ATTEMPT'],output)
                receipt=json.loads((root/'evidence/acceptance.json').read_text())
                self.assertEqual(receipt['workerJob'],env['DEPLOYED_WORKER_JOB'])
                self.assertNotIn('workerRevision',receipt)
                self.assertNotIn('azurecr.io',json.dumps(receipt)+output)
                self.assertEqual(receipt['extractorDigest'],env['DEPLOYED_EXTRACTOR_IMAGE'].rsplit('@',1)[1])
                self.assertEqual(receipt['deploymentAttempt'],'1')
                if case=='cross-run':
                    self.assertEqual(receipt['deploymentRun'],'77')
                    self.assertEqual(receipt['run'],'42')
                    self.assertEqual(receipt['verificationSource'],'b'*40)
                    self.assertIn('runs/77/attempts/1/jobs',events)
                if state=='pending' or case=='normal-retry':
                    self.assertIn('smoke-release.sh',events);self.assertIn('verify-rollout.sh',events)
                    self.assertGreaterEqual(int((root/'identity-count').read_text()),2)
                else:self.assertNotIn('az ',events);self.assertNotIn('smoke-release.sh',events)
            else:
                self.assertEqual(output,'','unaccepted release emitted healthy outputs')
                self.assertFalse((root/'evidence/acceptance.json').exists())
            self.assertNotIn('deployment group create',events)
            self.assertNotIn('revision copy',events)
            if case=='pending':self.assertIn('PUBLIC_ACCEPTANCE_PENDING',result.stdout);self.assertNotIn('smoke-release.sh',events)
    def test_recovery_preserves_original_deployment_and_new_verification_identity(self):
        self.run_case('cross-run',0)
        self.run_case('unrelated-source',1)
        self.run_case('cross-run-same-origin',1)
    def test_retained_deploy_same_run_after_reopening(self):self.run_case('reopened',0)
    def test_normal_deploy_already_verified_without_duplicate_cloud_work(self):self.run_case('normal',0,'accepted')
    def test_normal_acceptance_retry_rechecks_current_runtime(self):self.run_case('normal-retry',0,'accepted')
    def test_pending_is_not_accepted(self):self.run_case('pending',42)
    def test_invalid_or_superseded_evidence(self):
        for case in ('bad503','wrong-header','unexpected200','failed-deploy','wrong-head','changed-revision','late-revision','wrong-image','changed-worker-image','changed-worker-release','paused-worker','old-worker-maximum','worker-app-restored','wrong-worker-job','changed-extractor','changed-extractor-enabled','late-extractor','no-gateway','mixed-gateway','missing-traffic','invalid-traffic','partial-traffic','excess-traffic','unknown-mode','future-attempt','wrong-repository','public-failure'):
            with self.subTest(case=case):self.run_case(case,1)
    def test_missing_retained_outputs_fail_closed(self):
        for key in ('DEPLOYED_COMMIT','DEPLOYED_IMAGE','DEPLOYED_REVISION','DEPLOYED_ATTEMPT','DEPLOYED_PUBLIC_STATE','DEPLOYED_WORKER_IMAGE','DEPLOYED_WORKER_JOB','DEPLOYED_EXTRACTOR_IMAGE','DEPLOYED_EXTRACTOR_ENABLED'):
            with self.subTest(key=key):self.run_case('reopened',1,missing=key)
    def test_real_smoke_maintenance_and_default_contract(self):
        for mode,expectation,case,expected in [('forward','maintenance','pending',0),('rollback','maintenance','pending',0),('forward','normal','pending',22),('forward','maintenance','bad503',1),('forward','maintenance','wrong-header',1),('forward','maintenance','unexpected200',1),('forward','invalid','pending',1)]:
            with self.subTest(mode=mode,expectation=expectation,case=case),tempfile.TemporaryDirectory() as temp:
                root=Path(temp);tools=root/'bin';tools.mkdir()
                fixture=tools/'fixture';fixture.write_text('#!'+sys.executable+'\n'+FIXTURE);fixture.chmod(0o755)
                for name in ('az','curl','timeout'):(tools/name).symlink_to(fixture)
                env={**os.environ,'PATH':str(tools)+os.pathsep+os.environ['PATH'],'FIXTURE_ROOT':str(root),'CASE':case,'SMOKE_MODE':mode,'RELEASE_PUBLIC_EXPECTATION':expectation}
                result=subprocess.run(['bash','scripts/smoke-release.sh'],env=env,capture_output=True,text=True,timeout=15)
                self.assertEqual(result.returncode,expected if expectation == 'invalid' else (0 if expected == 0 else 20),result.stdout+result.stderr)
                events=(root/'events').read_text() if (root/'events').exists() else ''
                if expected==0:
                    self.assertEqual(events.count('curl '),7)
                    self.assertIn('RUNTIME_VERIFIED_PUBLIC_MAINTENANCE_PENDING',result.stdout)
                    self.assertNotIn('script ', events)
    def test_downstream_only_retry_requires_fresh_acceptance(self):
        text=Path('.github/workflows/release.yml').read_text()
        block=text.split('      - name: Validate publication configuration\n        run: |\n')[1].split('      - name: Sign in with API docs publisher identity')[0]
        script='\n'.join(line[10:] for line in block.splitlines())+'\nprintf completed'
        for accepted in ('1','2',''):
            with self.subTest(accepted=accepted):
                env={**os.environ,'ACCEPTANCE_ATTEMPT':accepted,'GITHUB_RUN_ATTEMPT':'2','RELEASE_COMMIT':'a'*40,'GITHUB_SHA':'a'*40,'RELEASE_IMAGE_DIGEST':'sha256:'+'a'*64,'API_DOCS_AZURE_CLIENT_ID':'fixture','AZURE_TENANT_ID':'fixture','AZURE_SUBSCRIPTION_ID':'fixture'}
                result=subprocess.run(['bash','-euo','pipefail','-c',script],env=env,capture_output=True,text=True)
                self.assertEqual(result.returncode==0,accepted=='2',result.stderr)
                self.assertEqual('completed' in result.stdout,accepted=='2')
    def test_workflow_completion_depends_on_acceptance(self):
        text=Path('.github/workflows/release.yml').read_text()
        accept=text.split('  public_acceptance:')[1].split('  publish_openapi:')[0]
        self.assertNotIn('runner.temp',accept.split('    steps:')[0], 'runner context is step-scoped, not job env')
        self.assertIn('needs: [deploy]',accept)
        self.assertIn('timeout-minutes: 15',accept)
        self.assertIn('az extension add --name containerapp --upgrade --yes',accept)
        self.assertIn('needs.deploy.outputs.deployment-attempt',accept)
        self.assertNotIn('continue-on-error',accept)
        self.assertNotIn('if: ${{ always() }}\n    runs-on:',accept)
        publish=text.split('  publish_openapi:')[1].split('  seal_extractor_evidence:')[0]
        self.assertIn('needs: public_acceptance',publish)
        self.assertIn('needs.public_acceptance.outputs.commit',publish)
        self.assertIn('needs: [prepare_extractor, verify_extractor_evidence, public_acceptance]',text)
        deploy=text.split('  deploy:')[1].split('  public_acceptance:')[0]
        self.assertIn("if: ${{ failure() && steps.deploy_runtime.outcome != 'skipped' }}",deploy)
        self.assertIn('SMOKE_MODE=rollback ./scripts/smoke-release.sh',deploy)

if __name__=='__main__':unittest.main()
