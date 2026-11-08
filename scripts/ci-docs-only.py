#!/usr/bin/env python3
"""Only proven human documentation diffs may omit application verification."""
from pathlib import PurePosixPath
import re
import subprocess
import sys


def git(*args):
    return subprocess.check_output(['git', '--literal-pathspecs', *args], stderr=subprocess.DEVNULL)


def docs_only(event, base, head):
    if event != 'pull_request' or not all(re.fullmatch('[0-9a-f]{40}', x) for x in (base, head)):
        return False
    if git('rev-parse', 'HEAD').decode().strip() != head:
        return False
    subprocess.run(['git', 'merge-base', '--is-ancestor', base, head], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    # Disable rename detection so both old and new paths must be documents.
    paths = git('diff', '--name-only', '-z', '--no-renames', base, head, '--').split(b'\0')
    paths = [path.decode('utf-8') for path in paths if path]
    if not paths:
        return False
    for path in paths:
        document = PurePosixPath(path)
        if (any(ord(char) < 32 or ord(char) == 127 for char in path) or
                document.as_posix() != path or '..' in document.parts or
                path.startswith('docs/obs-capture/') or
                any(part in {'fixtures', 'testdata', 'contracts'} for part in document.parts) or
                not ((path.startswith('docs/') and document.suffix in {'.md', '.txt', '.png', '.svg'})
                     or path == 'README.md')):
            return False
        for revision in (base, head):
            entry = git('ls-tree', '-z', revision, '--', path)
            if entry and not entry.startswith(b'100644 blob '):
                return False
    return True


if __name__ == '__main__':
    try:
        result = len(sys.argv) == 4 and docs_only(*sys.argv[1:])
    except (OSError, UnicodeError, subprocess.SubprocessError):
        result = False
    print('docs_only=' + str(result).lower())
