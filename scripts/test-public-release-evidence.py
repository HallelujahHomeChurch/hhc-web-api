#!/usr/bin/env python3
"""Public receipts must never copy arbitrary cloud fields or request identifiers."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


class PublicEvidence(unittest.TestCase):
    def test_whitelist_and_unaccepted_failure(self):
        spec = importlib.util.spec_from_file_location('public_evidence', 'scripts/public-release-evidence.py')
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            raw = root / 'rollout-cms-forward'
            raw.mkdir()
            canary = 'private-account-and-topology-canary'
            (raw / 'revision-start.json').write_text(json.dumps({'image': canary}))
            (raw / 'result.jsonl').write_text(json.dumps({'time': '2026-11-09T00:00:00Z', 'stage': 'public-cms', 'exit_code': 1, 'extra': canary})+'\n')
            (raw / 'samples.jsonl').write_text(json.dumps({'route': 'cms', 'status': 0, 'latency': None, 'transport_error': 28, 'request_id': canary, 'url': canary})+'\n')
            destination = root / 'public'
            module.export(root, destination)
            output = (destination / 'rollout-cms-forward.json').read_text()
            receipt = json.loads(output)
            self.assertNotIn(canary, output)
            self.assertIsNone(receipt['samples'][0]['latency'])
            self.assertEqual(receipt['results'][0]['exit_code'], 1)
            self.assertNotIn('acceptance', receipt)
            (raw / 'result.jsonl').write_text('{broken')
            with self.assertRaises(ValueError): module.export(root, destination)

    def test_accepted_identity_is_digest_only(self):
        spec = importlib.util.spec_from_file_location('public_evidence', 'scripts/public-release-evidence.py')
        module = importlib.util.module_from_spec(spec); spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);raw=root/'rollout-cms-public-acceptance';raw.mkdir()
            proof=dict(state='accepted', commit='a'*40, verificationSource='a'*40, imageDigest='sha256:'+'b'*64,
                       workerDigest='sha256:'+'c'*64, extractorDigest='sha256:'+'d'*64,
                       revision='hhc-web-api--r1', workerJob='hhc-web-bulletin-watermark@'+'a'*40,
                       extractorEnabled='false', run='123', deploymentRun='123', deploymentAttempt='1', acceptanceAttempt='1', private='canary')
            (raw/'acceptance.json').write_text(json.dumps(proof))
            module.export(root, root/'public')
            exported=json.loads((root/'public/rollout-cms-public-acceptance.json').read_text())
            self.assertEqual(exported['acceptance']['commit'],proof['commit'])
            self.assertNotIn('private',exported['acceptance'])
            proof['imageDigest']='private.example/image@'+proof['imageDigest']
            (raw/'acceptance.json').write_text(json.dumps(proof))
            with self.assertRaises(ValueError):module.export(root,root/'public')

if __name__=='__main__':unittest.main()
