#!/usr/bin/env python3
"""Read-only ACA release verification: 0 healthy, 10 negative, 20 unverified."""
import json
from decimal import Decimal, InvalidOperation
from pathlib import Path
import os
import re
import subprocess
import sys
import time
from deployment_config import valid_image


def azure(*args):
    result = subprocess.run(['az', *args, '-o', 'json', '--only-show-errors'],
                            capture_output=True, text=True, timeout=60)
    if result.returncode:
        raise ValueError('ARM verification unavailable')
    return json.loads(result.stdout)


def observe(app, revision, image):
    if not valid_image(image):
        raise ValueError('immutable image required')
    state = azure('containerapp', 'show', '-g', os.environ['RESOURCE_GROUP'], '-n', app,
                  '--query', '{latest:properties.latestRevisionName,ready:properties.latestReadyRevisionName,ingress:properties.configuration.ingress != null,traffic:properties.configuration.ingress.traffic}')
    revision = revision or state['latest']
    if not isinstance(revision, str) or not re.fullmatch(re.escape(app) + r'--[a-z0-9-]+', revision) or revision != state['latest']:
        return 20, revision
    runtime = azure('containerapp', 'revision', 'show', '-g', os.environ['RESOURCE_GROUP'],
                    '-n', app, '--revision', revision, '--query',
                    "{active:properties.active,health:properties.healthState,provisioning:properties.provisioningState,running:properties.runningState,images:properties.template.containers[].image,cpu:properties.template.containers[0].resources.cpu,memory:properties.template.containers[0].resources.memory,automatic:properties.template.containers[0].env[?name=='BULLETIN_AUTO_EXTRACTION_ENABLED'].{value:value,secretRef:secretRef}}")
    if runtime['images'] != [image]:
        return 20, revision
    if (runtime['provisioning'] == 'Failed' or runtime['health'] == 'Unhealthy' or
            runtime['running'] in ('Failed', 'Degraded')):
        return 10, revision
    if app == 'hhc-web-api':
        expected_cpu = Decimal(os.environ['RUNTIME_CPU'])
        actual_cpu = runtime['cpu']
        if actual_cpu is not None and (isinstance(actual_cpu, bool) or not isinstance(actual_cpu, (int, float, str))):
            raise ValueError('invalid CPU metadata')
        if actual_cpu is not None and not Decimal(str(actual_cpu)).is_finite():
            raise ValueError('invalid CPU metadata')
        if runtime['memory'] is not None and not isinstance(runtime['memory'], str):
            raise ValueError('invalid memory metadata')
        automatic = runtime['automatic']
        if automatic is not None and (not isinstance(automatic, list) or any(
                not isinstance(entry, dict) or set(entry) != {'value', 'secretRef'} or
                any(value is not None and not isinstance(value, str) for value in entry.values())
                for entry in automatic)):
            raise ValueError('invalid flag metadata')
        expected_automatic = [{'value': os.environ['BULLETIN_AUTO_EXTRACTION_ENABLED'], 'secretRef': None}]
        if os.environ.get('RUNTIME_ENV_MANIFEST'):
            overrides = json.loads(Path(os.environ['RUNTIME_ENV_MANIFEST']).read_text())['resources']['hhc-web-api']
            if 'BULLETIN_AUTO_EXTRACTION_ENABLED' in overrides:
                flag = overrides['BULLETIN_AUTO_EXTRACTION_ENABLED']
                expected_automatic = [] if flag is None else [{'value': flag.get('value'), 'secretRef': flag.get('secretRef')}]
        if (actual_cpu is None or Decimal(str(actual_cpu)) != expected_cpu or
                runtime['memory'] != os.environ['RUNTIME_MEMORY'] or
                runtime['automatic'] != expected_automatic):
            return 10, revision
    traffic = state['traffic']
    if state['ingress'] is False and app == 'hhc-web':
        serving = traffic is None
    else:
        if state['ingress'] is not True or not isinstance(traffic, list) or not traffic:
            return 20, revision
        total = 0
        for entry in traffic:
            if not isinstance(entry, dict) or type(entry.get('weight')) is not int or not 0 <= entry['weight'] <= 100:
                return 20, revision
            latest = entry.get('latestRevision', False)
            named = entry.get('revisionName')
            if type(latest) is not bool or (named is not None and not isinstance(named, str)):
                return 20, revision
            if latest and named is not None or not latest and not named:
                return 20, revision
            if entry['weight'] > 0 and not (latest or named == revision):
                return 20, revision
            total += entry['weight']
        serving = total == 100
    if (state['ready'] == revision and runtime['active'] is True and
            runtime['health'] == 'Healthy' and runtime['running'] in ('Running', 'RunningAtMaxScale') and serving):
        return 0, revision
    return 20, revision


def recovery():
    # A failed ARM request alone is not proof a deployment failed.
    deployments = json.loads(os.environ.get('FAILED_DEPLOYMENTS', '[]'))
    for deployment in deployments:
        state = azure('deployment', 'group', 'show', '-g', os.environ['RESOURCE_GROUP'],
                      '-n', deployment, '--query', 'properties.provisioningState')
        if state == 'Failed':
            return 10
    result, _ = observe(os.environ.get('APP_NAME') or os.environ['API_APP_NAME'],
                   os.environ.get('CANDIDATE_REVISION', ''), os.environ['IMAGE_REF'])
    return result


def main():
    recovering = sys.argv[1:] == ['recovery']
    result = 20
    try:
        if recovering:
            result = recovery()
        else:
            for attempt in range(60):
                result, revision = observe(os.environ.get('APP_NAME') or os.environ['API_APP_NAME'],
                                 os.environ.get('CANDIDATE_REVISION', ''), os.environ['IMAGE_REF'])
                if result == 0:
                    if os.environ.get('GITHUB_ENV'):
                        with open(os.environ['GITHUB_ENV'], 'a') as output:
                            output.write('CANDIDATE_REVISION=' + revision + '\n')
                    break
                if result == 10:
                    break
                if attempt < 59:
                    time.sleep(10)
    except (ValueError, InvalidOperation, KeyError, TypeError, AttributeError, OSError, subprocess.TimeoutExpired):
        result = 20
    if recovering:
        if os.environ.get('GITHUB_OUTPUT'):
            with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
                output.write('required=' + ('true' if result == 10 else 'false') + '\n')
        print(json.dumps({'event': 'release_recovery_decision', 'result': result,
                          'rollback': result == 10}))
        return 0
    if result == 20:
        print('::error::Release verification unavailable; candidate remains unaccepted')
    print(json.dumps({'event': 'release_runtime_verification', 'result': result}))
    return result


if __name__ == '__main__':
    sys.exit(main())
