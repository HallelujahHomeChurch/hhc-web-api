#!/usr/bin/env python3
"""Reuse only the fixed renderer authenticated by the existing producer evidence."""
import json
import os
from pathlib import Path
import re
from extractor_evidence import producer_evidence


def select(pin, proof, runner_temp):
    if not isinstance(pin, dict) or set(pin) != {'commit', 'image'} or not re.fullmatch(r'[0-9a-f]{40}', pin['commit']) or not re.fullmatch(r'ghcr.io/hallelujahhomechurch/hhc-bulletin-renderer@sha256:[0-9a-f]{64}', pin['image']):
        raise ValueError('Invalid immutable renderer pin')
    reuse = proof is not None and (proof['commit'], proof['image']) == (pin['commit'], pin['image'])
    return {'reuse': str(reuse).lower(), 'commit': pin['commit'], 'image': pin['image'] if reuse else '',
            'context': 'docker-image://' + pin['image'] if reuse else 'oci-layout://' + runner_temp + '/renderer-layout'}


if __name__ == '__main__':
    pin = json.loads(Path('scripts/extractor-producer.json').read_text())
    selected = select(pin, producer_evidence(pin), os.environ['RUNNER_TEMP'])
    with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
        for name, value in selected.items():
            output.write(name + '=' + value + '\n')
    print('Renderer source: ' + ('authenticated fixed digest' if selected['reuse'] == 'true' else 'exact source fallback'))
