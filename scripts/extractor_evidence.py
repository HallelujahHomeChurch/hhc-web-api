"""Authenticated Actions provenance; missing or expired evidence always runs full."""
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import zipfile
from extractor_selection import digest
from deployment_config import valid_image

REPO = 'HallelujahHomeChurch/hhc-web-api'
PRODUCER = 'HallelujahHomeChurch/frontend-platform'
SHA = re.compile(r'^[a-f0-9]{40}$')
HASH = re.compile(r'^[a-f0-9]{64}$')


def api(repo, path, raw=False):
    data = subprocess.check_output(['gh', 'api', f'repos/{repo}/{path}'], stderr=subprocess.DEVNULL, timeout=45)
    return data if raw else json.loads(data)


def runs(repo):
    result, page = [], 1
    while True:
        batch = api(repo, f'actions/workflows/release.yml/runs?status=completed&per_page=100&page={page}')['workflow_runs']
        result.extend(batch)
        if len(batch) < 100:
            return result
        page += 1


def prior_attempts(run_id, attempt, head):
    for number in range(int(attempt) - 1, 0, -1):
        previous = api(REPO, f'actions/runs/{run_id}/attempts/{number}')
        assert str(previous['id']) == str(run_id) and previous['run_attempt'] == number and previous['head_sha'] == head
        yield previous


def sealed_publication_failure(run):
    page = api(REPO, f"actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100")
    jobs = {job['name']: job for job in page['jobs']}
    assert page['total_count'] == len(jobs) == len(page['jobs'])
    assert all(job['status'] == 'completed' and job['head_sha'] == run['head_sha'] for job in jobs.values())
    return (all(jobs.get(name, {}).get('conclusion') == 'success'
                for name in ('public_acceptance', 'seal_extractor_evidence'))
            and jobs.get('publish_openapi', {}).get('conclusion') == 'failure'
            and all(job['conclusion'] in ('success', 'skipped')
                    for name, job in jobs.items() if name != 'publish_openapi'))


def evidence(repo, run, prefix):
    assert run['status'] == 'completed'
    assert run['path'] == '.github/workflows/release.yml' and run['head_repository']['full_name'] == repo
    assert run['event'] in (['push'] if repo == PRODUCER else ['push', 'workflow_dispatch'])
    if repo == REPO:
        assert run['head_branch'] == 'main'
    assert SHA.fullmatch(run['head_sha'])
    if run['conclusion'] != 'success':
        assert repo == REPO and prefix == 'extractor-release-evidence' and run['conclusion'] == 'failure'
        assert sealed_publication_failure(run)
    artifacts = api(repo, f"actions/runs/{run['id']}/artifacts?per_page=100")['artifacts']
    selected = [a for a in artifacts if a['name'] == f"{prefix}-{run['id']}-{run['run_attempt']}" ]
    if not selected and repo == REPO and run['run_attempt'] > 1:
        return evidence(repo, next(prior_attempts(run['id'], run['run_attempt'], run['head_sha'])), prefix)
    assert len(selected) == 1 and not selected[0]['expired'] and selected[0]['size_in_bytes'] < 1024*1024
    a = selected[0]
    data = api(repo, f"actions/artifacts/{a['id']}/zip", True)
    assert len(data) < 1024*1024 and a['digest'] == 'sha256:' + hashlib.sha256(data).hexdigest()
    with zipfile.ZipFile(io.BytesIO(data)) as z:
        assert z.namelist() == ['checkpoint.json'] and z.getinfo('checkpoint.json').file_size < 1024*1024
        value = json.loads(z.read('checkpoint.json'))
    assert value['schema'] == (1 if repo == PRODUCER else 3) and value['head'] == run['head_sha']
    if repo == REPO:
        assert all(re.fullmatch(r'sha256:[a-f0-9]{64}', value[key]) for key in ('apiDigest', 'extractorDigest'))
    assert str(value['runId']) == str(run['id']) and str(value['runAttempt']) == str(run['run_attempt'])
    return value


def producer_image_config(image):
    # Read only the pinned registry manifest/config; no layer download or new credential.
    data = subprocess.check_output(['docker', 'buildx', 'imagetools', 'inspect', '--format', '{{json .Image}}', image], stderr=subprocess.DEVNULL, timeout=45)
    assert len(data) < 1024*1024
    value = json.loads(data)
    assert value['os'] == 'linux' and value['architecture'] == 'amd64'
    assert isinstance(value['config'], dict) and value['config']
    return digest(value['config'])


def producer_evidence(pin=None):
    try:
        pin = pin or json.loads(Path('scripts/extractor-producer.json').read_text())
        assert set(pin) == {'commit', 'image'} and SHA.fullmatch(pin['commit'])
        assert re.fullmatch(r'ghcr.io/hallelujahhomechurch/hhc-bulletin-renderer@sha256:[a-f0-9]{64}', pin['image'])
        candidates = [r for r in runs(PRODUCER) if r['head_sha'] == pin['commit']]
        run = max(candidates, key=lambda r: r['updated_at'])
        proof = evidence(PRODUCER, run, 'renderer-release-evidence')
        manifest = proof['manifest']
        assert proof['publishedImage'] == manifest['publishedImage'] == pin['image']
        assert manifest['producerCommit'] == pin['commit'] and manifest['schema'] == 1
        assert HASH.fullmatch(manifest['runtimeFingerprint'])
        paths = manifest['dependencyPaths']; hashes = manifest['dependencyHashes']
        assert paths and len(paths) == len(set(paths)) and set(paths) == set(hashes)
        assert all(HASH.fullmatch(h) for h in hashes.values())
        # Match the producer's compact JSON serialization and insertion ordering.
        source = hashlib.sha256(json.dumps(hashes, separators=(',', ':'), ensure_ascii=False).encode()).hexdigest()
        assert source == manifest['sourceFingerprint']
        assert set(manifest['versions']) == {'v'+str(i) for i in range(1,12)}
        for value in manifest['versions'].values():
            assert value['dependencyPaths'] == paths and value['inputFingerprint'] == manifest['runtimeFingerprint']
        return {'commit': pin['commit'], 'image': pin['image'], 'run': str(run['id']), 'config': producer_image_config(pin['image']), 'runtime': manifest['runtimeFingerprint'], 'versions': {v: x['inputFingerprint'] for v,x in manifest['versions'].items()}}
    except (KeyError, TypeError, ValueError, AssertionError, OSError, subprocess.SubprocessError, zipfile.BadZipFile):
        print('Producer evidence unavailable; full extractor verification required')
        return None


def non_image_release(run):
    if (run.get('conclusion') != 'success' or run.get('status') != 'completed'
            or run.get('path') != '.github/workflows/release.yml'
            or run.get('head_repository', {}).get('full_name') != REPO
            or run.get('event') not in ('push', 'workflow_dispatch') or run.get('head_branch') != 'main'):
        return False
    page = api(REPO, f"actions/runs/{run['id']}/attempts/{run['run_attempt']}/jobs?per_page=100")
    jobs = {job['name']: job for job in page['jobs']}
    assert page['total_count'] == len(jobs) == len(page['jobs'])
    assert all(job['status'] == 'completed' and job['head_sha'] == run['head_sha'] for job in jobs.values())
    required = {'agent_docs', 'runtime_env', 'deploy', 'public_acceptance', 'seal_extractor_evidence'}
    return (required <= jobs.keys() and jobs['agent_docs']['conclusion'] == 'success'
            and jobs['runtime_env']['conclusion'] in ('success', 'skipped')
            and all(job['conclusion'] == 'skipped' for name, job in jobs.items() if name not in ('agent_docs', 'runtime_env')))


def restore():
    try:
        candidates = [r for r in runs(REPO) if str(r['id']) != os.environ['GITHUB_RUN_ID']]
        # Successful env/docs runs do not deploy an image or replace its checkpoint.
        run = next((r for r in sorted(candidates, key=lambda r: r['updated_at'], reverse=True) if not non_image_release(r)), None)
        if run is None:
            raise ValueError('no image release')
        proof = evidence(REPO, run, 'extractor-release-evidence')
        assert set(proof['full']) == {'v'+str(i) for i in range(1,12)}
        origins = {}
        for version, record in proof['full'].items():
            assert HASH.fullmatch(record['runtime']) and HASH.fullmatch(record['verification'])
            origin = str(record['runId'])
            assert re.fullmatch(r'[1-9][0-9]*', origin)
            if origin not in origins:
                origins[origin] = evidence(REPO, api(REPO, f'actions/runs/{origin}'), 'extractor-release-evidence')
            original = origins[origin]['full'][version]
            assert original == record and str(original['runId']) == origin
        subprocess.run(['git', 'merge-base', '--is-ancestor', proof['head'], 'HEAD'], check=True, capture_output=True)
        return proof
    except (KeyError, TypeError, ValueError, AssertionError, OSError, subprocess.SubprocessError, zipfile.BadZipFile):
        print('No trusted deployed baseline; full extractor verification required')
        return None


def guard():
    current = os.environ['GITHUB_RUN_ID']
    attempt = int(os.environ.get('GITHUB_RUN_ATTEMPT', '1'))
    candidates = [run for run in runs(REPO) if str(run['id']) != current]
    candidates.extend(prior_attempts(current, attempt, os.environ.get('GITHUB_SHA')))
    for run in candidates:
        if run['conclusion'] not in ('success', 'failure') or run['head_branch'] != 'main':
            continue
        if run['conclusion'] == 'failure':
            # Failed ancestors with different source cannot change this retry's image identity.
            if run['head_sha'] != os.environ.get('GITHUB_SHA'):
                ancestor = subprocess.run(['git', 'merge-base', '--is-ancestor', run['head_sha'], 'HEAD'], capture_output=True)
                if ancestor.returncode == 0:
                    continue
            if not sealed_publication_failure(run):
                continue
        subprocess.run(['git', 'merge-base', '--is-ancestor', run['head_sha'], 'HEAD'], check=True)
        if run['head_sha'] == os.environ['GITHUB_SHA']:
            prior = evidence(REPO, run, 'extractor-release-evidence')
            assert valid_image(os.environ['IMAGE_REF'], 'api') and valid_image(os.environ['EXTRACTOR_IMAGE_REF'], 'extractor')
            assert prior['apiDigest'] == os.environ['IMAGE_REF'].split('@')[1] and prior['extractorDigest'] == os.environ['EXTRACTOR_IMAGE_REF'].split('@')[1], 'rerun must preserve published identities'


if __name__ == '__main__':
    guard()
