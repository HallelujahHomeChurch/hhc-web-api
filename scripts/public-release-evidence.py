"""Publish a whitelist receipt; raw rollout files stay on the ephemeral runner."""
import json
from pathlib import Path
import re
import sys


def require(value, pattern):
    if not isinstance(value, str) or not re.fullmatch(pattern, value):
        raise ValueError('Malformed public release evidence')
    return value


def integer(value, maximum):
    if type(value) is not int or not 0 <= value <= maximum:
        raise ValueError('Malformed public release metric')
    return value


def export(root, destination):
    destination = Path(destination)
    destination.mkdir(parents=True, exist_ok=True)
    for raw in sorted(Path(root).glob('rollout-cms-*')):
        if not raw.is_dir() or raw.is_symlink():
            continue
        proof = {'schemaVersion': 1, 'results': [], 'samples': []}
        result = raw / 'result.jsonl'
        if result.exists():
            for line in result.read_text().splitlines():
                item = json.loads(line)
                proof['results'].append({
                    'stage': require(item['stage'], r'initializing|candidate-(?:start|end)|public-(?:ready|cms|oidc)|passed|runtime-verified-public-pending'),
                    'exit_code': integer(item['exit_code'], 255),
                })
        samples = raw / 'samples.jsonl'
        if samples.exists():
            for line in samples.read_text().splitlines():
                item = json.loads(line)
                latency = item['latency']
                if latency is not None and (type(latency) not in (int, float) or not 0 <= latency <= 3600):
                    raise ValueError('Malformed public release latency')
                proof['samples'].append({
                    'route': require(item['route'], r'ready|cms|oidc'),
                    'status': integer(item['status'], 599),
                    'transport_error': integer(item['transport_error'], 255),
                    'latency': latency,
                })
        acceptance = raw / 'acceptance.json'
        if acceptance.exists():
            item = json.loads(acceptance.read_text())
            accepted = {'state': require(item['state'], 'accepted')}
            for key in ('commit', 'verificationSource'):
                accepted[key] = require(item[key], r'[a-f0-9]{40}')
            for key in ('imageDigest', 'workerDigest', 'extractorDigest'):
                accepted[key] = require(item[key], r'sha256:[a-f0-9]{64}')
            for key in ('run', 'deploymentRun', 'deploymentAttempt', 'acceptanceAttempt'):
                accepted[key] = require(item[key], r'[1-9][0-9]*')
            proof['acceptance'] = accepted
        if proof['results'] or proof['samples'] or 'acceptance' in proof:
            name = require(raw.name, r'rollout-cms-(?:forward|rollback|public-acceptance)')
            (destination / (name + '.json')).write_text(json.dumps(proof, indent=2)+'\n')


if __name__ == '__main__':
    try:
        export(sys.argv[1], sys.argv[2])
    except (ValueError, KeyError, TypeError, OSError):
        raise SystemExit('Cannot create public release receipt')
