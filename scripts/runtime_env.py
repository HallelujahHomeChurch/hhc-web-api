#!/usr/bin/env python3
"""Apply env overrides to the serving template without building application code."""
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from deployment_config import required, valid_image
import time

SOURCE = Path(__file__).resolve().parents[1] / 'infra/runtime-env.json'
RESOURCES = {
    'hhc-web-api': ('app', 'hhc-web-api'),
    'hhc-web-bulletin-worker': ('app', 'hhc-web-bulletin-worker'),
    'hhc-web-bulletin-watermark': ('job', 'bulletin-watermark'),
    'hhc-web-migrate': ('job', 'hhc-web-migrate'),
    'hhc-web-content-import': ('job', 'content-import'),
    'hhc-web-bulletin-extractor': ('job', 'bulletin-extractor'),
}


def parse(text):
    data = json.loads(text)
    if not isinstance(data, dict) or set(data) != {'resources'} or not isinstance(data['resources'], dict) or set(data['resources']) != set(RESOURCES):
        raise ValueError('Invalid runtime environment resources')
    for overrides in data['resources'].values():
        if not isinstance(overrides, dict):
            raise ValueError('Invalid environment map')
        for name, entry in overrides.items():
            if not re.fullmatch(r'[A-Z_][A-Z0-9_]*', name):
                raise ValueError('Invalid environment name')
            if name == 'RELEASE':
                raise ValueError('RELEASE is reserved for immutable artifact identity')
            if name in ('BULLETIN_EXTRACTION_ENABLED', 'BULLETIN_AUTO_EXTRACTION_ENABLED', 'BULLETIN_LLM_EXTRACTION_ENABLED') and entry not in ({'value': 'true'}, {'value': 'false'}):
                raise ValueError('Bulletin flags require literal true or false values')
            if entry is None:
                continue
            if not isinstance(entry, dict) or set(entry) not in ({'value'}, {'secretRef'}):
                raise ValueError('Use value objects, existing secretRef objects, or null')
            value = next(iter(entry.values()))
            if not isinstance(value, str) or '\0' in value:
                raise ValueError('Invalid environment value')
            if 'secretRef' in entry and not re.fullmatch(r'[a-z0-9][a-z0-9-]*', value):
                raise ValueError('Invalid secret reference')
    return data['resources']


def load():
    return parse(SOURCE.read_text())


def export_flags():
    desired = load()
    flags = [('hhc-web-api', 'BULLETIN_AUTO_EXTRACTION_ENABLED'),
             ('hhc-web-bulletin-extractor', 'BULLETIN_EXTRACTION_ENABLED'),
             ('hhc-web-api', 'BULLETIN_LLM_EXTRACTION_ENABLED'),
             ('hhc-web-api', 'AZURE_OPENAI_DEPLOYMENT')]
    api_llm = desired['hhc-web-api'].get('BULLETIN_LLM_EXTRACTION_ENABLED')
    job_llm = desired['hhc-web-bulletin-extractor'].get('BULLETIN_LLM_EXTRACTION_ENABLED')
    if api_llm != job_llm:
        raise ValueError('API and extractor LLM activation must agree')
    with Path(os.environ['GITHUB_ENV']).open('a') as output:
        for resource, name in flags:
            if name in desired[resource]:
                output.write(name + '=' + desired[resource][name]['value'] + '\n')


def refresh():
    """Use latest merged settings with the immutable release code and images."""
    if os.environ.get('GITHUB_REF') != 'refs/heads/main':
        raise ValueError('Configuration refresh requires main')
    result = subprocess.run(['gh', 'api', 'repos/HallelujahHomeChurch/hhc-web-api/contents/infra/runtime-env.json?ref=main',
                             '-H', 'Accept: application/vnd.github.raw+json'],
                            capture_output=True, text=True, timeout=60)
    if result.returncode:
        raise ValueError('Latest configuration unavailable')
    desired = parse(result.stdout)
    SOURCE.write_text(result.stdout)
    payload = result.stdout.encode()
    blob = hashlib.sha1(b'blob ' + str(len(payload)).encode() + b'\0' + payload).hexdigest()
    print('Runtime configuration source blob: ' + blob)


def normalize_secret_refs(template, resource, secrets):
    result = copy.deepcopy(template)
    for container in result['containers']:
        for entry in container.get('env', []):
            ref = entry.get('secretRef')
            if ref is None or ref in secrets:
                continue
            prefix = resource + '-'
            if ref.startswith(prefix) and ref[len(prefix):] in secrets:
                entry['secretRef'] = ref[len(prefix):]
            else:
                raise ValueError('Serving template references an unknown secret')
    return result


def az(*args, body=None):
    # Never persist templates or forward Azure errors, which may contain env values.
    result = subprocess.run([os.environ.get('AZ_BIN', 'az'), *args, '--only-show-errors', '-o', 'json'],
                            input=json.dumps(body) if body is not None else None,
                            capture_output=True, text=True, timeout=300)
    if result.returncode:
        raise ValueError('Azure operation failed; environment values withheld')
    return json.loads(result.stdout) if result.stdout.strip() else None


def with_env(template, container_name, overrides):
    desired = copy.deepcopy(template)
    containers = [c for c in desired['containers'] if c['name'] == container_name]
    if len(containers) != 1:
        raise ValueError('Expected the owned runtime container')
    container = containers[0]
    old = container.get('env', [])
    # Retain existing entries byte-for-byte unless explicitly owned by the manifest.
    env = [entry for entry in old if entry['name'] not in overrides]
    env += [{'name': name, **value} for name, value in overrides.items() if value is not None]
    if sorted(env, key=lambda e: e['name']) == sorted(old, key=lambda e: e['name']):
        return None
    container['env'] = env
    return desired


def wait_for_template(resource, kind, template, revision):
    command = ['containerapp'] + (['job'] if kind == 'job' else [])
    for _ in range(60):
        props = az(*command, 'show', '-g', required('RESOURCE_GROUP'), '-n', resource)['properties']
        if props.get('provisioningState') in ('Failed', 'Canceled'):
            raise ValueError('Runtime template provisioning failed')
        if props.get('provisioningState') == 'Succeeded':
            if (kind == 'job' and props['template'] == template) or (kind == 'app' and props.get('latestReadyRevisionName') == revision):
                return
        time.sleep(5)
    raise ValueError('Runtime template provisioning timed out')


def deploy():
    if os.environ.get('GITHUB_REF') != 'refs/heads/main':
        raise ValueError('Runtime deployment requires merged main')
    sha = os.environ['GITHUB_SHA']
    if not re.fullmatch(r'[0-9a-f]{40}', sha):
        raise ValueError('Invalid commit identity')
    head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
    latest = subprocess.check_output(['git', 'ls-remote', 'origin', 'refs/heads/main'], text=True).split()
    if head != sha:
        raise ValueError('Checkout does not match the requested commit')
    if not latest or not re.fullmatch(r'[0-9a-f]{40}', latest[0]):
        raise ValueError('Main branch identity unavailable')
    desired = load()
    if latest[0] != sha:
        subprocess.run(['git', 'fetch', '--no-tags', 'origin', latest[0]], check=True, capture_output=True)
        current = parse(subprocess.check_output(['git', 'show', latest[0] + ':infra/runtime-env.json'], text=True))
        if current != desired:
            print('Skipped superseded runtime configuration')
            return
    base = os.environ['RUNTIME_ENV_BASE_SHA']
    if not re.fullmatch(r'[0-9a-f]{40}', base):
        raise ValueError('Invalid configuration base')
    previous = parse(subprocess.check_output(['git', 'show', base + ':infra/runtime-env.json'], text=True))
    if any(not set(previous[name]).issubset(values) for name, values in desired.items()):
        raise ValueError('Use explicit null to remove an environment variable')
    for resource, overrides in desired.items():
        if not overrides:
            continue
        kind, container = RESOURCES[resource]
        command = ['containerapp'] + (['job'] if kind == 'job' else [])
        current = az(*command, 'show', '-g', required('RESOURCE_GROUP'), '-n', resource)
        props = current['properties']
        secrets = {entry['name'] for entry in props['configuration'].get('secrets', [])}
        if any(value is not None and 'secretRef' in value and value['secretRef'] not in secrets for value in overrides.values()):
            raise ValueError('Runtime secret reference is not provisioned')
        template = props['template']
        if kind == 'app':
            if props['configuration']['activeRevisionsMode'] != 'Single':
                raise ValueError('CMS fast updates require the existing single revision mode')
            revision = props['latestReadyRevisionName']
            template = az('containerapp', 'revision', 'show', '-g', required('RESOURCE_GROUP'), '-n', resource,
                          '--revision', revision)['properties']['template']
        owned = [c for c in template['containers'] if c['name'] == container]
        if len(owned) != 1 or not valid_image(owned[0]['image']):
            raise ValueError('Expected an immutable serving image')
        template = normalize_secret_refs(template, resource, secrets)
        updated = with_env(template, container, overrides)
        if updated is None:
            continue
        revision = None
        if kind == 'app':
            suffix = 'env-' + os.environ['GITHUB_RUN_ID'] + '-' + os.environ['GITHUB_RUN_ATTEMPT']
            if not re.fullmatch(r'env-[0-9]+-[0-9]+', suffix):
                raise ValueError('Invalid revision identity')
            updated['revisionSuffix'] = suffix
            revision = resource + '--' + suffix
        # The CLI revision-copy path can replace serving containers with latest containers.
        # PATCH the captured template instead, preserving the serving image and all other fields.
        az('rest', '--method', 'PATCH', '--url', current['id'] + '?api-version=2025-01-01',
           '--body', '@/dev/stdin', body={'properties': {'template': updated}})
        wait_for_template(resource, kind, updated, revision)
        print('Applied runtime environment: ' + resource)


if __name__ == '__main__':
    try:
        if sys.argv[1:] == ['deploy']:
            deploy()
        elif sys.argv[1:] == ['export-flags']:
            export_flags()
        elif sys.argv[1:] == ['refresh']:
            refresh()
        elif sys.argv[1:] == ['validate']:
            load()
        else:
            raise ValueError('Expected deploy, refresh, export-flags, or validate')
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        raise SystemExit('Runtime environment operation failed; values withheld')
