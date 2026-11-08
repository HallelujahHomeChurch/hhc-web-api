#!/usr/bin/env python3
"""The same read-only native verification entry point is used in PR and release."""
import json
import os
from pathlib import Path
import subprocess
import sys
from extractor_selection import digest, load
from image_config_digest import image_config_digest

arguments = sys.argv[1:]
assert len(arguments) in (4, 6)
mode, plan_path, group_text, output_dir = arguments[:4]
assert group_text.isdigit()
group = int(group_text)
targets = [(mode, plan_path, output_dir)]
if len(arguments) == 6:
    # PR-only release-mode rehearsal: one process executes both plans' exact union.
    # Actual releases still use the original four-argument entry point.
    assert mode == 'ci' and arguments[5] != output_dir
    targets.append(('release', arguments[4], arguments[5]))
for _, _, directory in targets:
    (Path(directory) / f'{group}.json').unlink(missing_ok=True)
plans = [(load(path, target_mode), directory) for target_mode, path, directory in targets]
plan = plans[0][0]
for candidate, _ in plans:
    assert 0 <= group < len(candidate['groups'])
    for field in ['head', 'image', 'runtime', 'verification', 'binaries']:
        assert candidate[field] == plan[field], 'different candidate or verification inputs'
image = os.environ['EXTRACTOR_IMAGE_REF']
image_id = image_config_digest(image)
assert image_id == plan['image'], 'prepared image identity mismatch'
# Both binaries come from this revision's preparation job; never use cross-run binaries.
for name in ['parser.test', 'extractor.test']:
    path = Path('artifacts/extractor-selection') / name
    assert digest(path.read_bytes()) == plan['binaries'][name]
env = {**os.environ, 'HHC_EXTRACTOR_LOAD_IMAGE': image,
       'HHC_EXTRACTOR_TEST_BINARY': str(Path('artifacts/extractor-selection/parser.test').resolve()),
       'HHC_EXTRACTOR_TEST_BINARY_SHA256': digest(Path('artifacts/extractor-selection/parser.test').read_bytes())}
selected = [(candidate, directory, [e for v in candidate['groups'][group]
             for e in candidate['entries'] if e['version'] == v]) for candidate, directory in plans]
for tier, test in [('full', 'TestExtractorIsolationLoadGate'), ('smoke', 'TestExtractorRuntimeSmoke')]:
    # Full and smoke are different tests: neither may stand in for the other.
    versions = sorted({e['version'] for _, _, entries in selected for e in entries if e['tier'] == tier})
    if not versions:
        continue
    command = ['go', 'tool', 'test2json', '-t', '-p', 'extractor', str(Path('artifacts/extractor-selection/extractor.test').resolve()),
               '-test.v=test2json', '-test.count=1', '-test.run', '^'+test+'$/^('+'|'.join(versions)+')$']
    process = subprocess.Popen(command, cwd='cmd/extractor', env=env, stdout=subprocess.PIPE, text=True)
    passed = set()
    failed = False
    for line in process.stdout:
        print(line, end='', flush=True)
        event = json.loads(line)
        if event.get('Action') in ['fail', 'skip']:
            failed = True
        if event.get('Action') == 'pass' and 'Test' in event:
            passed.add(event['Test'])
    assert process.wait() == 0 and not failed
    assert passed == {test} | {test+'/'+v for v in versions}, 'missing or unexpected native test cases'
# Emit neither receipt unless every selected case passed. Each existing aggregate
# still validates its own plan digest and exact entries; no cross-run reuse exists.
for candidate, directory, entries in selected:
    Path(directory).mkdir(parents=True, exist_ok=True)
    (Path(directory) / f'{group}.json').write_text(json.dumps(dict(head=candidate['head'], run=os.environ['GITHUB_RUN_ID'],
        attempt=os.environ['GITHUB_RUN_ATTEMPT'], mode=candidate['mode'], plan=candidate['digest'], image=image_id,
        group=group, entries=entries)))
