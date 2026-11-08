#!/usr/bin/env python3
"""Closed extractor coverage plan; uncertain inputs retain the original full gate."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys

DOCS_SPEC = importlib.util.spec_from_file_location('ci_docs', Path(__file__).resolve().parents[1] / '.github/scripts/ci-agent-docs.py')
DOCS = importlib.util.module_from_spec(DOCS_SPEC)
DOCS_SPEC.loader.exec_module(DOCS)

VERSIONS = ['v' + str(i) for i in range(1, 12)]
SHA = re.compile(r'^[a-f0-9]{40}$')
HASH = re.compile(r'^[a-f0-9]{64}$')
GROUPS = [['v11', 'v4', 'v5'], ['v10', 'v2', 'v6'], ['v9', 'v1', 'v3'], ['v8', 'v7']]
SMOKE_GROUPS = [['v11', 'v10', 'v7', 'v6'], ['v9', 'v8', 'v5'], ['v1', 'v2', 'v3', 'v4']]
VERSION_TESTS = {'internal/onlinebulletins/template_v1_test.go': VERSIONS,
                 'internal/onlinebulletins/template_v2_test.go': VERSIONS[1:],
                 'internal/onlinebulletins/template_v9_test.go': ['v9', 'v10', 'v11'],
                 'internal/onlinebulletins/template_v10_test.go': ['v10', 'v11']}


def digest(value):
    data = value if isinstance(value, bytes) else json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
    return hashlib.sha256(data).hexdigest()


def git(*args):
    return subprocess.check_output(['git', '--literal-pathspecs', *args], stderr=subprocess.DEVNULL).decode().strip()


def changes(base, head):
    if not SHA.fullmatch(base or '') or not SHA.fullmatch(head or '') or git('rev-parse', 'HEAD') != head:
        raise ValueError('invalid diff identity')
    git('merge-base', '--is-ancestor', base, head)
    raw = subprocess.check_output(['git', 'diff', '--raw', '--no-abbrev', '--no-renames', '-z', base, head, '--']).decode().split('\0')
    assert raw.pop() == '' and len(raw) % 2 == 0
    result = []
    for i in range(0, len(raw), 2):
        m = re.fullmatch(r':(\d{6}) (\d{6}) [a-f0-9]{40} [a-f0-9]{40} ([A-Z])', raw[i])
        if not m:
            raise ValueError('unsupported diff')
        assert not raw[i+1].startswith('producer-version/')
        result.append((m[3], raw[i+1], m[1] in ['100644', '000000'] and m[2] == '100644'))
    return result


def ci_required(diff):
    if not diff:
        return True
    for status, path, regular in diff:
        if status not in ('A', 'M') or not regular:
            return True
        # Main packages are absent from both native acceptance binaries' dependency graphs.
        if re.fullmatch(r'cmd/(server|migrate|content-import|home-v2-migrate)/[a-z0-9_]+\.go', path):
            continue
        if path == 'infra/runtime-env.json':
            continue  # Serving overrides are absent from native build/test inputs.
        if DOCS.document(path):
            continue
        return True
    return False


def affected(diff):
    if not diff:
        return set(VERSIONS)
    result = set()
    for status, path, regular in diff:
        if status not in ['A', 'M'] or not regular:
            return set(VERSIONS)
        if path in {'producer-version/' + v for v in VERSIONS}:
            result.add(path.split('/')[1])
        elif path in VERSION_TESTS:
            result.update(VERSION_TESTS[path])
        elif re.fullmatch(r'internal/(content|recordings|sitesettings|legal)/[a-z0-9_]+_test\.go', path):
            continue  # These tests still run in the ordinary complete Go lane.
        elif not ci_required([(status, path, regular)]):
            continue
        else:
            return set(VERSIONS)
    return result


def producer_versions(before, after):
    if before['runtime'] != after['runtime'] or before['config'] != after['config']:
        return set(VERSIONS)
    return {v for v in VERSIONS if before['versions'][v] != after['versions'][v]}


def make_plan(head, base, mode, diff, runtime, verification, producer, checkpoint=None, force=False, image=None, binaries=None):
    assert SHA.fullmatch(head) and mode in ['ci', 'release'] and HASH.fullmatch(runtime)
    selected = set(VERSIONS) if force or not producer else affected(diff)
    entries = []
    for version in VERSIONS:
        tier, reason = ('full', 'changed-or-unknown') if version in selected else ('smoke', 'unaffected')
        if mode == 'release' and tier == 'smoke':
            prior = (checkpoint or {}).get('full', {}).get(version, {})
            if prior.get('runtime') != runtime or prior.get('verification') != verification[version]:
                tier, reason = 'full', 'missing-full-evidence'
        entries.append({'version': version, 'tier': tier, 'reason': reason})
    # Measured smoke groups reduce the critical path and use fewer runners than full.
    groups = SMOKE_GROUPS if all(e['tier'] == 'smoke' for e in entries) else GROUPS
    plan = dict(schema=1, head=head, base=base if SHA.fullmatch(base or '') else None, mode=mode,
                runtime=runtime, verification=verification, producer=producer, image=image, binaries=binaries, entries=entries, groups=groups)
    return {**plan, 'digest': digest(plan)}


def validate(plan, mode, head):
    assert set(plan) == {'schema', 'head', 'base', 'mode', 'runtime', 'verification', 'producer', 'image', 'binaries', 'entries', 'groups', 'digest'}
    assert plan['schema'] == 1 and plan['head'] == head and SHA.fullmatch(head) and plan['mode'] == mode
    assert plan['base'] is None or SHA.fullmatch(plan['base'])
    assert set(plan['binaries']) == {'parser.test', 'extractor.test'} and all(HASH.fullmatch(v) for v in plan['binaries'].values())
    assert re.fullmatch(r'sha256:[a-f0-9]{64}', plan['image'])
    assert HASH.fullmatch(plan['runtime']) and set(plan['verification']) == set(VERSIONS)
    assert all(HASH.fullmatch(v) for v in plan['verification'].values())
    assert plan['digest'] == digest({k: v for k, v in plan.items() if k != 'digest'})
    assert len(plan['entries']) == len(VERSIONS)
    for version, entry in zip(VERSIONS, plan['entries']):
        assert set(entry) == {'version', 'tier', 'reason'} and entry['version'] == version
        assert entry['tier'] in ['full', 'smoke'] and entry['reason'] in ['changed-or-unknown', 'unaffected', 'missing-full-evidence']
    assert plan['groups'] in [GROUPS, SMOKE_GROUPS]
    if plan['groups'] == SMOKE_GROUPS:
        assert all(e['tier'] == 'smoke' for e in plan['entries'])
    return plan


def load(path, mode):
    data = Path(path).read_bytes()
    assert len(data) < 1024 * 1024
    return validate(json.loads(data), mode, os.environ['GITHUB_SHA'])


def aggregate(plan, receipts):
    assert len(receipts) == len(plan['groups'])
    assert sorted(r['group'] for r in receipts) == list(range(len(plan['groups'])))
    prior_jobs = {}
    run_head = None
    for r in receipts:
        assert set(r) == {'head', 'run', 'attempt', 'mode', 'plan', 'image', 'group', 'entries'}
        assert (r['head'], r['run'], r['mode'], r['plan']) == (plan['head'], os.environ['GITHUB_RUN_ID'], plan['mode'], plan['digest'])
        assert re.fullmatch(r'[1-9][0-9]*', r['attempt']) and int(r['attempt']) <= int(os.environ['GITHUB_RUN_ATTEMPT'])
        if r['attempt'] != os.environ['GITHUB_RUN_ATTEMPT']:
            from extractor_evidence import api, REPO
            # Actions jobs identify the PR head; the executed plan identifies its merge commit.
            if run_head is None:
                run_head = api(REPO, f"actions/runs/{r['run']}")['head_sha']
            if r['attempt'] not in prior_jobs:
                prior_jobs[r['attempt']] = api(REPO, f"actions/runs/{r['run']}/attempts/{r['attempt']}/jobs?per_page=100")['jobs']
            matches = [j for j in prior_jobs[r['attempt']] if j['name'] == f"verify_extractor ({r['group']})"]
            assert len(matches) == 1 and matches[0]['conclusion'] == 'success' and matches[0]['head_sha'] == run_head, 'unverified prior attempt'
        assert r['image'] == plan['image']
        expected = [e for v in plan['groups'][r['group']] for e in plan['entries'] if e['version'] == v]
        assert r['entries'] == expected
    assert len({r['image'] for r in receipts}) == 1


def prepare(mode):
    from extractor_evidence import producer_evidence, restore
    head = git('rev-parse', 'HEAD')
    assert head == os.environ['GITHUB_SHA']
    target = Path('artifacts/extractor-selection'); target.mkdir(parents=True, exist_ok=True)
    checkpoint = restore() if mode == 'release' else None
    base = checkpoint['head'] if checkpoint else os.environ.get('BASE_SHA') if mode == 'ci' else None
    try:
        diff = changes(base, head)
    except (ValueError, AssertionError, subprocess.SubprocessError):
        diff = None
    check_release = diff is None or any(p.startswith(('scripts/', '.github/', 'cmd/extractor/')) for _, p, _ in diff)
    producer = producer_evidence()
    if producer:
        if mode == 'ci':
            if os.environ.get('CI_RENDERER_IMAGE'):
                if os.environ['CI_RENDERER_IMAGE'] != producer['image']: producer = None
            else:
                actual = subprocess.check_output(['git', '-C', '.renderer-source', 'rev-parse', 'HEAD']).decode().strip()
                if actual != producer['commit']: producer = None
        elif os.environ.get('BULLETIN_RENDERER_IMAGE') != producer['image']:
            producer = None
    if diff and any(p == 'scripts/extractor-producer.json' for _, p, _ in diff):
        try:
            old = producer_evidence(json.loads(git('show', base + ':scripts/extractor-producer.json')))
            if not producer or not old: raise ValueError('missing producer lineage')
            changed = producer_versions(old, producer)
            # Reuse the same classifier through a bounded, explicit dependency mapping.
            diff = [(status, path, regular) for status, path, regular in diff if path != 'scripts/extractor-producer.json']
            diff += [('M', 'producer-version/' + v, True) for v in changed]
            if not diff: diff = [('M', 'docs/producer-unchanged.md', True)]
        except (ValueError, AssertionError, subprocess.SubprocessError):
            producer = None
    image = os.environ['EXTRACTOR_IMAGE_REF']
    runtime = subprocess.check_output(['docker', 'run', '--rm', '--network', 'none', '--read-only', '--user', '0',
        '--mount', f'type=bind,src={Path("scripts/extractor-runtime-fingerprint.mjs").resolve()},dst=/tmp/fingerprint.mjs,readonly',
        '--entrypoint', 'node', image, '/tmp/fingerprint.mjs']).decode().strip()
    assert HASH.fullmatch(runtime)
    details = json.loads(subprocess.check_output(['docker', 'image', 'inspect', image]))[0]
    assert details['Os'] == 'linux' and details['Architecture'] == 'amd64' and isinstance(details['Config'], dict)
    runtime = digest({'filesystem': runtime, 'config': details['Config'], 'architecture': details['Architecture'], 'os': details['Os']})
    tracked = git('ls-files', '-z').split('\0')
    common = [p for p in tracked if p != 'scripts/extractor-producer.json' and (p.startswith(('scripts/', '.github/', 'cmd/extractor/', 'internal/onlinebulletins/', 'internal/httpapi/')) or p in ['go.mod', 'go.sum', 'Dockerfile.extractor'])]
    verification = {v: digest({p: digest(Path(p).read_bytes()) for p in sorted(common) if p not in VERSION_TESTS or v in VERSION_TESTS[p]}) for v in VERSIONS}
    from image_config_digest import image_config_digest
    image_id = image_config_digest(image)
    binaries = json.loads((target / 'binaries.json').read_text())
    plan = make_plan(head, base, mode, diff, runtime, verification, producer, checkpoint, os.environ.get('EXTRACTOR_FORCE_FULL') == 'true', image_id, binaries)
    (target / f'{mode}.json').write_text(json.dumps(plan))
    if checkpoint:
        (target / 'baseline.json').write_text(json.dumps(checkpoint))
    if mode == 'ci':
        synthetic = {'full': {v: {'runtime': runtime, 'verification': verification[v]} for v in VERSIONS}}
        check_diff = [('M', 'internal/onlinebulletins/template_v10_test.go' if plan['groups'] == GROUPS else 'docs/unchanged.md', True)]
        check = make_plan(head, head, 'release', check_diff, runtime, verification, {'synthetic': True}, synthetic, image=image_id, binaries=binaries)
        (target / 'release-check.json').write_text(json.dumps(check))
    with open(os.environ['GITHUB_OUTPUT'], 'a') as out:
        out.write('release_check=' + str(mode == 'ci' and check_release).lower() + '\n')
        out.write('matrix=' + json.dumps({'group': list(range(len(plan['groups'])))}) + '\n')
        out.write('plan_sha=' + digest((target / f'{mode}.json').read_bytes()) + '\n')


if __name__ == '__main__':
    action, mode = sys.argv[1:3]
    if action == 'route-ci':
        try:
            required = mode not in ('pull_request', 'push') or os.environ.get('EXTRACTOR_FORCE_FULL') == 'true' or ci_required(changes(*sys.argv[3:5]))
        except (ValueError, AssertionError, OSError, subprocess.SubprocessError):
            required = True
        print('extractor_required=' + str(required).lower())
    elif action == 'prepare':
        prepare(mode)
    elif action == 'verify':
        plan = load(sys.argv[3], mode)
        receipts = [json.loads(p.read_text()) for p in Path(sys.argv[4]).rglob('*.json')]
        aggregate(plan, receipts)
        print('Exact extractor coverage verified')
    else:
        raise ValueError('unknown action')
