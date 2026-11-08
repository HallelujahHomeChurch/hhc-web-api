#!/usr/bin/env python3
"""Drain old Event Job executions before enabling the new API producer."""
import json
import os
import re
import subprocess
import time
from deployment_config import valid_image

TERMINAL = {'Succeeded', 'Failed', 'Stopped', 'Canceled', 'Cancelled'}


def drained(executions, image):
    if not isinstance(executions, list):
        raise ValueError('Execution inventory unavailable')
    for execution in executions:
        if not isinstance(execution, dict) or not isinstance(execution.get('status'), str):
            raise ValueError('Execution status unavailable')
        if execution['status'] not in TERMINAL and execution.get('images') != [image]:
            return False
    return True


def main():
    image = os.environ['EXTRACTOR_IMAGE_REF']
    if not valid_image(image, 'extractor'):
        raise ValueError('Pinned extractor image required')
    for attempt in range(60):
        result = subprocess.run([
            'az', 'containerapp', 'job', 'execution', 'list', '-g', os.environ['RESOURCE_GROUP'],
            '-n', 'hhc-web-bulletin-extractor', '--query',
            '[].{status:properties.status,images:properties.template.containers[].image}',
            '-o', 'json', '--only-show-errors',
        ], capture_output=True, text=True, timeout=30)
        if result.returncode:
            raise ValueError('Execution inventory unavailable')
        if drained(json.loads(result.stdout), image):
            print('Compatible extractor deployed; old executions drained')
            return
        if attempt < 59:
            time.sleep(5)
    raise ValueError('Old extractor executions still active; producer remains unchanged')


if __name__ == '__main__':
    main()
