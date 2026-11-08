import json
import os
from pathlib import Path
import runpy
import sys
import tempfile
import types
import unittest
from unittest.mock import patch

class SealAttempt(unittest.TestCase):
    def test_full_verification_keeps_original_attempt_when_acceptance_retried(self):
        source=Path('scripts/seal_extractor_evidence.py').resolve()
        plan={'head':'a'*40,'runtime':'runtime','verification':{'v1':'verification'},'entries':[{'version':'v1','tier':'full'}]}
        receipt={'attempt':'1','entries':plan['entries']}
        owner=types.ModuleType('extractor_selection');owner.load=lambda *_:plan;owner.VERSIONS=['v1'];owner.aggregate=lambda *_:None
        with tempfile.TemporaryDirectory() as temp,patch.dict(sys.modules,{'extractor_selection':owner}),patch.dict(os.environ,{'DEPLOYED_COMMIT':'a'*40,'DEPLOYED_API_DIGEST':'sha256:'+'b'*64,'DEPLOYED_EXTRACTOR_DIGEST':'sha256:'+'c'*64,'ACCEPTANCE_ATTEMPT':'2','GITHUB_RUN_ID':'42','GITHUB_RUN_ATTEMPT':'2'}):
            previous=Path.cwd()
            try:
                os.chdir(temp);Path('extractor-results').mkdir();Path('extractor-results/result.json').write_text(json.dumps(receipt));runpy.run_path(str(source))
                result=json.loads(Path('artifacts/extractor-release-evidence/checkpoint.json').read_text())
                self.assertEqual(result['runAttempt'],'2');self.assertEqual(result['full']['v1']['runAttempt'],'1')
                self.assertEqual(result['receipts'],[receipt])
                self.assertEqual(result['schema'],3)
                self.assertEqual(result['apiDigest'],'sha256:'+'b'*64)
                self.assertEqual(result['extractorDigest'],'sha256:'+'c'*64)
                self.assertNotIn('azurecr',json.dumps(result))
                Path('artifacts/extractor-release-evidence/checkpoint.json').unlink()
                for key in ['DEPLOYED_API_DIGEST','DEPLOYED_EXTRACTOR_DIGEST']:
                    original=os.environ[key];os.environ[key]='not-a-digest'
                    with self.assertRaises(AssertionError):runpy.run_path(str(source))
                    self.assertFalse(Path('artifacts/extractor-release-evidence/checkpoint.json').exists())
                    os.environ[key]=original
                os.environ['ACCEPTANCE_ATTEMPT']='1'
                with self.assertRaises(AssertionError):runpy.run_path(str(source))
                self.assertFalse(Path('artifacts/extractor-release-evidence/checkpoint.json').exists())
            finally:os.chdir(previous)

if __name__=='__main__':unittest.main()
