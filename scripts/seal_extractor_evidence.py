import json
import os
from pathlib import Path
import re
import subprocess
from extractor_selection import aggregate, load, VERSIONS

assert os.environ['ACCEPTANCE_ATTEMPT'] == os.environ['GITHUB_RUN_ATTEMPT'], 'rerun public_acceptance before completion'

selection = Path('artifacts/extractor-selection')
plan = load(selection / 'release.json', 'release')
head = plan['head']
receipts = [json.loads(p.read_text()) for p in Path('extractor-results').rglob('*.json')]
aggregate(plan, receipts)
assert os.environ['DEPLOYED_COMMIT'] == head
for key in ['DEPLOYED_API_DIGEST', 'DEPLOYED_EXTRACTOR_DIGEST']:
    assert re.fullmatch(r'sha256:[a-f0-9]{64}', os.environ[key])
baseline_path = selection / 'baseline.json'
baseline = json.loads(baseline_path.read_text()) if baseline_path.exists() else {}
full = {}
for entry in plan['entries']:
    version = entry['version']
    current = dict(runtime=plan['runtime'], verification=plan['verification'][version])
    if entry['tier'] == 'full':
        full[version] = dict(**current, head=plan['head'], runId=os.environ['GITHUB_RUN_ID'], runAttempt=next(r['attempt'] for r in receipts if any(e['version'] == version for e in r['entries'])))
    else:
        prior = baseline['full'][version]
        assert all(prior[k] == v for k,v in current.items())
        full[version] = prior
assert set(full) == set(VERSIONS)
result = dict(schema=3, head=head, runId=os.environ['GITHUB_RUN_ID'], runAttempt=os.environ['GITHUB_RUN_ATTEMPT'],
              apiDigest=os.environ['DEPLOYED_API_DIGEST'], extractorDigest=os.environ['DEPLOYED_EXTRACTOR_DIGEST'], full=full, plan=plan, receipts=receipts)
path = Path('artifacts/extractor-release-evidence/checkpoint.json'); path.parent.mkdir(parents=True, exist_ok=True)
path.write_text(json.dumps(result))
