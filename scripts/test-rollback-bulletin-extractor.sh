#!/bin/sh
set -eu
python3 - <<'PYTEST'
import sys
sys.path.insert(0,'scripts')
import test_no_cloud
import copy,json,os,pathlib,subprocess,sys,tempfile,unittest
IMAGE='example.azurecr.io/example/hhc-web-bulletin-extractor@sha256:'+'a'*64
ID='/subscriptions/11111111-1111-4111-8111-111111111111/resourceGroups/example-group/providers/Microsoft.App/jobs/hhc-web-bulletin-extractor'
STUB='''
import json,os,pathlib,sys
args=sys.argv[1:];p=pathlib.Path(os.environ['STATE']);s=json.loads(p.read_text())
if args[0]=='rest':
 body=json.loads(pathlib.Path(args[args.index('--body')+1][1:]).read_text());s['body']=body;p.write_text(json.dumps(s))
 assert args[args.index('--method')+1]=='PATCH'
 config=body['properties']['configuration'];opposite='eventTriggerConfig' if config['triggerType']=='Schedule' else 'scheduleTriggerConfig'
 assert opposite in config and config[opposite] is None
 assert 'dapr' not in config and 'identitySettings' not in config
 assert 'imageType' not in body['properties']['template']['containers'][0]
 assert args[args.index('--url')+1].endswith('/Microsoft.App/jobs/hhc-web-bulletin-extractor?api-version=2025-01-01')
elif args[:3]==['containerapp','job','show']:
 body=s['body'];body['properties']['provisioningState']='Succeeded'
 if s.get('wrong') is True:body['properties']['template']['containers'][0]['image']='wrong'
 elif s.get('wrong')=='environment':body['properties']['template']['containers'][0]['env'][0]['value']='false'
 elif s.get('wrong')=='configuration':body['properties']['configuration']['replicaTimeout']=999
 print(json.dumps(body))
else:sys.exit(2)
'''
class Recovery(unittest.TestCase):
 def run_recovery(self,trigger='Schedule',mutate=None,wrong=False,**overrides):
  config={'dapr':None,'identitySettings':[],'triggerType':trigger,'secrets':[{'name':'database-url','keyVaultUrl':'https://vault.invalid/secrets/jobs'}]}
  config['scheduleTriggerConfig' if trigger=='Schedule' else 'eventTriggerConfig']={'cronExpression':'* * * * *'} if trigger=='Schedule' else {'scale':{'maxExecutions':1}}
  snapshot={'id':ID,'properties':{'configuration':config,'template':{'containers':[{'imageType':'ContainerImage','image':IMAGE,'env':[{'name':'BULLETIN_EXTRACTION_ENABLED','value':'true'},{'name':'DATABASE_URL','secretRef':'database-url'}]}]}}}
  if mutate:mutate(snapshot)
  with tempfile.TemporaryDirectory() as d:
   root=pathlib.Path(d);(root/'snapshot').write_text(json.dumps(snapshot));(root/'state').write_text(json.dumps({'wrong':wrong}))
   (root/'az').write_text('#!'+sys.executable+'\n'+STUB);(root/'az').chmod(0o755)
   env={**os.environ,'PATH':d+os.pathsep+os.environ['PATH'],'STATE':str(root/'state'),'RESOURCE_GROUP':'example-group','PREVIOUS_EXTRACTOR_IMAGE_REF':IMAGE,'PREVIOUS_EXTRACTOR_SNAPSHOT':str(root/'snapshot'),**overrides}
   r=subprocess.run(['bash','scripts/rollback-bulletin-extractor.sh'],env=env,text=True,capture_output=True,timeout=20)
   return r,json.loads((root/'state').read_text())
 def test_restores_both_trigger_kinds_and_activation(self):
  for trigger in ['Schedule','Event']:
   with self.subTest(trigger=trigger):
    r,s=self.run_recovery(trigger);self.assertEqual(r.returncode,0,r.stderr);p=s['body']['properties'];self.assertEqual(p['configuration']['triggerType'],trigger);self.assertEqual(p['template']['containers'][0]['image'],IMAGE);self.assertEqual(p['template']['containers'][0]['env'][0]['value'],'true')
 def test_invalid_snapshots_do_not_write(self):
  for mutate in [lambda s:s.update(id=ID+'-other'),lambda s:s['properties']['configuration'].update(triggerType='Manual'),lambda s:s['properties']['configuration'].update(dapr={'enabled':True}),lambda s:s['properties']['configuration'].update(identitySettings=[{'lifecycle':'None'}]),lambda s:s['properties']['template']['containers'][0].update(imageType='Unknown'),lambda s:s['properties']['configuration']['secrets'][0].update(value='do-not-persist')]:
   r,s=self.run_recovery(mutate=mutate);self.assertNotEqual(r.returncode,0);self.assertNotIn('body',s)
 def test_invalid_inputs_do_not_write(self):
  for env in [{'RESOURCE_GROUP':'other'},{'PREVIOUS_EXTRACTOR_IMAGE_REF':'mutable:tag'}]:
   r,s=self.run_recovery(**env);self.assertNotEqual(r.returncode,0);self.assertNotIn('body',s)
 def test_failed_readback_blocks_recovery_success(self):
  for wrong in [True,'environment','configuration']:
   with self.subTest(wrong=wrong):
    r,_=self.run_recovery(wrong=wrong);self.assertNotEqual(r.returncode,0)
unittest.main()
PYTEST
