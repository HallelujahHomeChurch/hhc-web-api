#!/usr/bin/env python3
"""Restore a captured predecessor without reading candidate environment overrides."""
import copy
import json
import os
import re
import subprocess
import sys
from deployment_config import required, resource_scope, valid_image

CONTAINERS = {'hhc-web-api': 'hhc-web-api', 'hhc-web-bulletin-worker': 'hhc-web-bulletin-worker'}


def azure(*args, body=None):
    result = subprocess.run([os.environ.get('AZ_BIN', 'az'), *args, '--only-show-errors', '-o', 'json'],
        input=json.dumps(body) if body is not None else None, text=True, capture_output=True, timeout=300)
    if result.returncode:
        raise ValueError('Runtime recovery failed; values withheld')
    return json.loads(result.stdout) if result.stdout.strip() else None


def restore(resource, revision, image, internal_ingress=None, dapr=None):
    if resource not in CONTAINERS or not re.fullmatch(re.escape(resource) + r'--[a-z0-9-]+', revision):
        raise ValueError('Invalid recovery resource or predecessor')
    if not valid_image(image):
        raise ValueError('Immutable recovery image required')
    current = azure('containerapp', 'show', '-g', required('RESOURCE_GROUP'), '-n', resource)
    source = azure('containerapp', 'revision', 'show', '-g', required('RESOURCE_GROUP'), '-n', resource, '--revision', revision)
    template = copy.deepcopy(source['properties']['template'])
    containers = template['containers']
    if len(containers) != 1 or containers[0]['name'] != CONTAINERS[resource]:
        raise ValueError('Unexpected predecessor container')
    original = containers[0]['image']
    if original.split('@')[0].split(':')[0] != image.split('@')[0] or ('@' in original and original != image):
        raise ValueError('Recovery image does not match captured predecessor')
    containers[0]['image'] = image
    names = {entry['name'] for entry in current['properties']['configuration'].get('secrets', [])}
    for entry in containers[0].get('env', []):
        reference = entry.get('secretRef')
        if reference and reference not in names:
            prefix = resource + '-'
            if reference.startswith(prefix) and reference[len(prefix):] in names:
                entry['secretRef'] = reference[len(prefix):]
            else:
                raise ValueError('Predecessor references an unknown secret')
    suffix = 'rollback-' + os.environ['GITHUB_RUN_ID'] + '-' + os.environ['GITHUB_RUN_ATTEMPT']
    if not re.fullmatch(r'rollback-[0-9]+-[0-9]+', suffix):
        raise ValueError('Invalid recovery revision identity')
    template['revisionSuffix'] = suffix
    properties = {'template': template}
    if internal_ingress is not None or dapr is not None:
        if resource != 'hhc-web' or type(internal_ingress) is not bool or type(dapr) is not bool:
            raise ValueError('Invalid website recovery scope')
        configuration = copy.deepcopy(current['properties']['configuration'])
        configuration['dapr']['enabled'] = dapr
        if internal_ingress:
            configuration['ingress']['external'] = False
            configuration['ingress']['targetPort'] = 10001
        else:
            configuration['ingress'] = None
        # Update only the app-scoped fields captured before the attempted release.
        properties['configuration'] = {key: configuration[key] for key in ('dapr', 'ingress')}
    identity = current['id']
    if identity.lower() != (resource_scope() + '/providers/Microsoft.App/containerApps/' + resource).lower():
        raise ValueError('Unexpected recovery resource identity')
    azure('rest', '--method', 'PATCH', '--url', identity + '?api-version=2025-01-01',
          '--body', '@/dev/stdin', body={'properties': properties})


if __name__ == '__main__':
    try:
        args = sys.argv[1:]
        if len(args) == 3:
            restore(*args)
        elif len(args) == 5 and all(value in ('true', 'false') for value in args[3:]):
            restore(*args[:3], *(value == 'true' for value in args[3:]))
        else:
            raise ValueError('Expected resource, predecessor, image and optional website scope')
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        sys.exit('Runtime recovery failed; values withheld')
