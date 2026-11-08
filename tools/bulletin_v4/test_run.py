import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class RunFailureTest(unittest.TestCase):
    def test_interrupted_creation_cleans_only_owned_resources(self):
        script = Path(__file__).resolve().parent / "run.sh"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "tools/bulletin_v4").mkdir(parents=True)
            (root / "tools/bulletin_v4/preview.py").write_text(
                (script.parent / "preview.py").read_text())
            az = root / "az"
            az.write_text('''#!/usr/bin/env python3
import json,os,sys,signal
from pathlib import Path
a=sys.argv[1:]
p=Path(os.environ['FAKE_AZ_ROOT'])
with (p/'calls').open('a') as f: f.write(json.dumps(a)+'\\n')
base='/subscriptions/11111111-1111-4111-8111-111111111111/resourceGroups/example-group/providers/Microsoft.App/'
if a[:2]==['account','show']: print('11111111-1111-4111-8111-111111111111')
elif a[:3]==['containerapp','job','show']: print('example.azurecr.io/example/hhc-web-api@sha256:56a3fe5b76e72ed46424bb56da0a686d459e939a222db91ed3e50e6b131ec04f')
elif a[:3]==['deployment','group','what-if']:
 print(json.dumps({'status':'Succeeded','changes':[{'changeType':'Create','resourceId':base+x} for x in ['containerApps/hhc-v4-db-123-1','jobs/hhc-v4-job-123-1']]}))
elif a[:3]==['deployment','group','create']:
 (p/'created').touch(); os.kill(os.getppid(),signal.SIGTERM); sys.exit(7)
elif a[:2]==['resource','list']:
 if not (p/'created').exists(): print('0')
 else:
  kind=a[a.index('--resource-type')+1].split('/')[-1]
  if (p/('deleted-'+kind)).exists(): print('0')
  else: print(json.dumps([{'id':base+kind+'/hhc-v4-'+('job' if kind=='jobs' else 'db')+'-123-1','tags':{'owner':'hhc-web-api','task':'V4-405','temporary':'true','run':'123-1'}}]))
elif a[:2]==['resource','delete']:
 kind=a[-2].split('/')[-2]; (p/('deleted-'+kind)).touch()
else: sys.exit(9)
''')
            az.chmod(0o755)
            env = dict(os.environ, PATH=str(root)+os.pathsep+os.environ['PATH'],
                       FAKE_AZ_ROOT=str(root), GITHUB_REF="refs/heads/main",
                       GITHUB_EVENT_NAME="workflow_dispatch", GITHUB_RUN_ID="123",
                       RESOURCE_GROUP="example-group",AZURE_SUBSCRIPTION_ID="11111111-1111-4111-8111-111111111111",ACR_LOGIN_SERVER="example.azurecr.io",V4_IMAGE_REPOSITORY="example/hhc-web-v4-lab",CONTAINER_APP_ENVIRONMENT_NAME="example-env",V4_BASE_IMAGE="example.azurecr.io/example/hhc-web-api@sha256:56a3fe5b76e72ed46424bb56da0a686d459e939a222db91ed3e50e6b131ec04f",
                       GITHUB_RUN_ATTEMPT="1", V4_IMAGE="example.azurecr.io/example/hhc-web-v4-lab@sha256:"+"a"*64)
            result = subprocess.run(["bash",str(script)],cwd=root,env=env,
                                    capture_output=True,text=True,timeout=15)
            self.assertEqual(result.returncode,143)
            calls=[json.loads(line) for line in (root/'calls').read_text().splitlines()]
            deleted=[x for x in calls if x[:2]==['resource','delete']]
            self.assertEqual(len(deleted),2)
            self.assertTrue(all(x[-2].endswith(('hhc-v4-job-123-1','hhc-v4-db-123-1')) for x in deleted))
            self.assertFalse(any(x[:3]==['containerapp','job','start'] for x in calls))
            self.assertEqual(json.loads((root/'v4-evidence/cleanup.json').read_text()),{'cleanupSucceeded':True})
