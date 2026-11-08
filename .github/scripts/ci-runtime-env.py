#!/usr/bin/env python3
"""Route manifest-only changes without application verification."""
import importlib.util
import re
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('docs_classifier', Path(__file__).with_name('ci-agent-docs.py'))
docs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(docs)

sys.path.insert(0, str(ROOT / 'scripts'))
import runtime_env


def config_only(event, base, head):
    docs.document = lambda path: path == 'infra/runtime-env.json'
    return docs.docs_only(event, base, head)


def validate_transition(event, base, head):
    if event not in ('push', 'pull_request') or not all(re.fullmatch('[0-9a-f]{40}', sha) and set(sha) != {'0'} for sha in (base, head)):
        return
    changed = subprocess.check_output(['git', 'diff', '--name-only', base, head, '--', 'infra/runtime-env.json'], text=True)
    if not changed:
        return
    def read(sha):
        result = subprocess.run(['git', 'show', sha + ':infra/runtime-env.json'], capture_output=True, text=True)
        if result.returncode:
            if sha == base:
                return {}
            raise ValueError('Runtime manifest must remain present')
        text = result.stdout
        return runtime_env.parse(text)
    before, after = read(base), read(head)
    if any(not set(owned) <= set(after[target]) for target, owned in before.items()):
        raise ValueError('Use null tombstones; do not delete owned keys')


if __name__ == '__main__':
    transition_args = sys.argv[2:] if sys.argv[1:2] == ['deploy'] else sys.argv[1:]
    if len(transition_args) == 3:
        try:
            validate_transition(*transition_args)
        except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
            sys.exit('Invalid runtime configuration transition; values withheld')
    try:
        result = len(sys.argv) == 4 and config_only(*sys.argv[1:])
    except (OSError, ValueError, KeyError, TypeError, UnicodeError, subprocess.SubprocessError):
        result = False
    print('config_only=' + str(result).lower())
