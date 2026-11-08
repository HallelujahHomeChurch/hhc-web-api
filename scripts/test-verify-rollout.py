#!/usr/bin/env python3
"""Exercise rollout gates with isolated Azure/HTTP fixtures; never contacts Azure."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

FIXTURE = r'''
import json, os, pathlib, sys
name=pathlib.Path(sys.argv[0]).name
case=os.environ['CASE']; root=pathlib.Path(os.environ['FIXTURE_ROOT'])
def count(key):
 p=root/key;n=int(p.read_text())+1 if p.exists() else 1;p.write_text(str(n));return n
if name=='az':
 args=sys.argv[1:]
 if args[:3]==['containerapp','revision','list']:
  print(json.dumps([{'name':'gateway--active','active':True,'traffic':100}]))
 elif args[:3]==['containerapp','revision','show'] and 'gateway--active' in args:
  if 'properties.template.containers[].name' in args:print(json.dumps(['api-gateway']));sys.exit(0)
  print(json.dumps(['on']))
 elif args[:3]==['containerapp','revision','show']:
  if case=='snapshot-error':sys.exit(1)
  x={'active':True,'health':'Healthy','running':'Running','traffic':100,'min':1,'containers':[{'name':os.environ['APP_NAME'],'image':os.environ['IMAGE_REF'],'port':[{'value':'8082','secretRef':None}],'daprPort':[],'audit':[{'value':'true','secretRef':None}]}]}
  if case.startswith('retired-'):x['containers'][0]['daprMode']=[{'value':'false','secretRef':None}]
  if case=='retired-invalid':x['containers'][0]['daprMode']=[{'value':'invalid','secretRef':None}]
  if case=='wrong-image':x['containers'][0]['image']='wrong'
  if case=='unhealthy':x['health']='Unhealthy'
  if case=='api-port-missing':x['containers'][0]['port']=[]
  if case=='api-port-wrong':x['containers'][0]['port']=[{'value':'8083','secretRef':None}]
  if case=='api-audit-missing':x['containers'][0]['audit']=[]
  if case=='api-audit-disabled':x['containers'][0]['audit']=[{'value':'false','secretRef':None}]
  if case=='api-port-secret':x['containers'][0]['port']=[{'value':None,'secretRef':'hidden'}]
  if case=='api-port-duplicate':x['containers'][0]['port'].append({'value':None,'secretRef':'hidden'})
  if case=='api-audit-secret':x['containers'][0]['audit']=[{'value':None,'secretRef':'hidden'}]
  if case=='api-audit-duplicate':x['containers'][0]['audit'].append({'value':None,'secretRef':'hidden'})
  query=args[args.index('--query')+1]
  for field,key in [('port','PORT'),('audit','AUDIT_DISPATCH_ENABLED')]:
   if field+":env[?name=='"+key+"'].value" in query:x['containers'][0][field]=[e['value'] for e in x['containers'][0][field] if e['value'] is not None]
  if case=='api-dapr-port-wrong':x['containers'][0]['daprPort']=[{'value':'3600','secretRef':None}]
  if case=='api-dapr-port-secret':x['containers'][0]['daprPort']=[{'value':None,'secretRef':'hidden'}]
  if case=='api-dapr-port-duplicate':x['containers'][0]['daprPort']=[{'value':'3500','secretRef':None}]*2
  if case=='api-dapr-port-explicit':x['containers'][0]['daprPort']=[{'value':'3500','secretRef':None}]
  print(json.dumps(x))
 elif args[:2]==['containerapp','show']:
  if '-o' in args and args[args.index('-o')+1]=='tsv':
   if case=='gateway-revision-error':print('private_token_should_not_leak',file=sys.stderr);sys.exit(7)
   if case!='gateway-revision-empty':print('gateway-ready')
  else:
   x={'latest':os.environ['CANDIDATE_REVISION'],'ready':os.environ['CANDIDATE_REVISION'] if case!='wrong-revision' else 'old','mode':'Single','dapr':{'enabled':True,'appId':'hhc-web-api','appPort':8082,'appProtocol':'http'}}
   if case.startswith('retired-') and case!='retired-config-mismatch':x['dapr']['enabled']=False
   if case=='api-dapr-disabled':x['dapr']['enabled']=False
   if case=='api-dapr-id-wrong':x['dapr']['appId']='other'
   if case=='api-dapr-port-missing':x['dapr'].pop('appPort')
   if case=='api-dapr-app-port-wrong':x['dapr']['appPort']=8083
   if case=='api-dapr-protocol-wrong':x['dapr']['appProtocol']='grpc'
   if case=='api-multiple-mode':x['mode']='Multiple'
   print(json.dumps(x))
 elif args[:3]==['containerapp','replica','list']:
  if '-o' in args and args[args.index('-o')+1]=='tsv':
   if case=='gateway-replica-error':print('private_token_should_not_leak',file=sys.stderr);sys.exit(7)
   if case!='gateway-replica-empty':print('gateway-replica')
   sys.exit(0)
  n=count('snapshots');x=[{'name':'replica-1','containers':[{'name':os.environ['APP_NAME'],'ready':True,'started':True,'restarts':0},{'name':'daprd','ready':True,'started':True,'restarts':0}]}]
  if case.startswith('retired-') and case!='retired-sidecar-present':x[0]['containers'].pop()
  if case=='replacement':
   x.append({'name':'replica-2' if n==1 else 'replacement-3','containers':x[0]['containers']})
  if case=='zero':x=[]
  if case=='missing-dapr':x[0]['containers'].pop()
  if case=='unready-dapr':x[0]['containers'][1]['ready']=False
  if case=='restarted' or case=='late-restart' and n>1:x[0]['containers'][1]['restarts']=1
  if case.startswith('rollback-'):x[0]['containers'][1]['restarts']=5+(1 if case=='rollback-restart' and n>1 else 0)
  if case=='closing-restart' and (root/'closing-private').exists():x[0]['containers'][1]['restarts']=1
  if case=='null-restarts':x[0]['containers'][1]['restarts']=None
  print(json.dumps(x))
 elif args[:2]==['containerapp','exec']:
  count('private-calls');sys.exit(99)
 else:sys.exit(4)
 sys.exit(0)
if name=='curl':
 args=sys.argv[1:];n=count('requests');url=args[-1]
 status=200
 if case=='http-500' or case=='one-500' and n==2:status=500
 if case=='redirect':status=302
 body={'status':'ready'} if url.endswith('/ready') else {'issuer':'https://account.alive.org.tw','jwks_uri':'https://account.alive.org.tw/.well-known/jwks.json'} if 'openid-configuration' in url else {'data':{'version':1},'error':None,'meta':{}}
 if case.startswith('maintenance') and 'site-layout' in url:
  status=503;body={'error':'Service Unavailable','service':'api-gateway'}
 if case=='maintenance-ready-fail' and url.endswith('/ready'):status=503
 if case=='bad-body':body={'private':'private_token_should_not_leak'}
 pathlib.Path(args[args.index('--output')+1]).write_text(json.dumps(body))
 pathlib.Path(args[args.index('--dump-header')+1]).write_text('HTTP/1.1 %s\r\ncontent-type: application/json\r\nx-hhc-request-id: 0123456789abcdef0123456789abcdef\r\n\r\n'%status+('Retry-After: 300\r\nCache-Control: private, no-store\r\n' if case.startswith('maintenance') and case!='maintenance-bad-header' else ''))
 print(f'{status} 0.001')
 sys.exit(28 if case=='timeout' else 0)
raise SystemExit(5)
'''

class RolloutGate(unittest.TestCase):
    def test_distroless_api_uses_platform_and_http_proofs(self):
        self.run_case('distroless-api',True)
    def test_forward_api_port_and_dapr_contract(self):
        self.run_case('api-dapr-port-explicit',True)
        for case in ('api-port-missing','api-port-wrong','api-port-secret','api-port-duplicate','api-audit-missing','api-audit-disabled','api-audit-secret','api-audit-duplicate','api-dapr-port-wrong','api-dapr-port-secret','api-dapr-port-duplicate','api-dapr-disabled','api-dapr-id-wrong','api-dapr-port-missing','api-dapr-app-port-wrong','api-dapr-protocol-wrong','api-multiple-mode'):
            with self.subTest(case=case):self.run_case(case)
    def test_maintenance_preserves_rollout_checks(self):
        self.run_case('maintenance',True)
        self.run_case('maintenance-ready-fail')
        self.run_case('maintenance-bad-header')
    def run_case(self, case, success=False):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory); tools=root/'bin'; tools.mkdir()
            fixture=tools/'fixture'; fixture.write_text('#!'+sys.executable+'\n'+FIXTURE);fixture.chmod(0o755)
            for name in ('az','curl'):(tools/name).symlink_to(fixture)
            (tools/'sleep').write_text('#!/bin/sh\nexit 0\n');(tools/'sleep').chmod(0o755)
            evidence=root/'evidence'
            app='hhc-web-api'
            env={**os.environ,'PATH':str(tools)+os.pathsep+os.environ['PATH'],'CASE':case,'FIXTURE_ROOT':str(root),'RESOURCE_GROUP':'fixture','APP_NAME':app,'IMAGE_REF':'image@sha256:fixture','CANDIDATE_REVISION':'candidate-1','ROLLOUT_EVIDENCE_DIR':str(evidence),'SMOKE_MODE':'rollback' if case.startswith('rollback-') else 'forward'}
            if case.startswith('maintenance'):env['RELEASE_PUBLIC_EXPECTATION']='maintenance'
            result=subprocess.run(['bash','scripts/verify-rollout.sh'],env=env,capture_output=True,text=True,timeout=45)
            self.assertEqual(result.returncode==0,success,result.stdout+result.stderr+((evidence/"result.jsonl").read_text() if (evidence/"result.jsonl").exists() else ""))
            saved=''.join(p.read_text() for p in evidence.glob('*.json*'))
            self.assertNotIn('private_token_should_not_leak',result.stdout+result.stderr+saved)
            self.assertFalse((root/'private-calls').exists(), 'remote exec was attempted')
            self.assertFalse((root/'api-shell-exec-attempts').exists(),'distroless API shell exec was attempted')
            if success:
                samples=[json.loads(line) for line in (evidence/'samples.jsonl').read_text().splitlines()]
                self.assertEqual(len(samples),90)
                self.assertTrue(all(s['status']==(503 if case.startswith('maintenance') and s['route']=='cms' else 200) and s['request_id']=='0123456789abcdef0123456789abcdef' for s in samples))
            if case=='one-500':
                self.assertEqual(int((root/'requests').read_text()),2,'retried a failed sample to green')
    def test_workflow_gate_before_promotion(self):
        workflow=Path('.github/workflows/release.yml').read_text()
        self.assertIn('CANDIDATE_REVISION=', workflow)
        self.assertIn('if: ${{ always() }}',workflow)
        gate=workflow.index('./scripts/verify-rollout.sh')
        if 'name: Deploy event-driven bulletin worker' in workflow:
            self.assertLess(gate,workflow.index('name: Deploy event-driven bulletin worker'))
        else:
            self.assertLess(gate,workflow.index('name: Capture production release outputs'))
        self.assertIn('SMOKE_MODE=rollback',workflow)
    def test_retired_sidecar_contract(self):
        self.run_case('retired-healthy',True)
        for case in ('retired-config-mismatch','retired-sidecar-present','retired-invalid'):
            self.run_case(case)
    def test_healthy(self):self.run_case('healthy',True)
    def test_rollback_preserves_history_but_rejects_new_restarts(self):
        self.run_case('rollback-restored',True)
        self.run_case('rollback-restart')
    def test_no_console_execution_in_release_helpers(self):
        for name in ('verify-rollout.sh', 'smoke-release.sh'):
            self.assertNotIn('containerapp exec', Path('scripts', name).read_text())

    def test_candidate_and_path_failures(self):
        for case in ('replacement','wrong-image','wrong-revision','unhealthy','zero','missing-dapr','unready-dapr','restarted','late-restart','null-restarts','http-500','one-500','redirect','timeout','bad-body','snapshot-error'):
            with self.subTest(case=case):self.run_case(case)

if __name__=='__main__':unittest.main()
