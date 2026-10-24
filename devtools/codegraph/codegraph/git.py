"""Layer A: read a revision's files from Git objects. The only module that runs a process.

Reads objects only: never the working tree or index, and no command here runs hooks or
content filters. A failure raises; nothing partial is returned.
"""
from __future__ import annotations

import os
import subprocess
from dataclasses import dataclass
from pathlib import Path

from codegraph.errors import CodegraphError

SYMLINK_MODE = '120000'


@dataclass(frozen=True)
class Revision:
    commit: str  # the full commit ID the ref resolved to


@dataclass(frozen=True)
class Blob:
    path: str  # from the repository root, '/'-separated
    id: str


def resolve(repo: Path, ref: str) -> Revision:
    if not ref or ref.startswith('-'):
        raise CodegraphError('unknown-ref', 'a ref must not be empty or start with "-"')
    result = _run(repo, 'rev-parse', '--verify', '--quiet', f'{ref}^{{commit}}', allowed=(1,))
    if result.returncode == 1:
        raise CodegraphError('unknown-ref', 'the ref does not name a commit in this repository')
    return Revision(result.stdout.decode().strip())


def tree(repo: Path, revision: Revision, scope: str) -> tuple[Blob, ...]:
    """The regular files under ``scope`` at ``revision``, sorted by path; symlinks and submodules are left out.
    A scope that does not exist at that revision gives no files."""
    out = _run(repo, '--literal-pathspecs', 'ls-tree', '-r', '-z', '--full-tree', revision.commit, '--', scope).stdout
    blobs = []
    for entry in out.split(b'\0'):
        if not entry:
            continue
        header, _, path = entry.partition(b'\t')
        mode, kind, object_id = header.decode().split(' ')
        if kind == 'blob' and mode != SYMLINK_MODE:
            blobs.append(Blob(path.decode(errors='replace'), object_id))
    return tuple(sorted(blobs, key=lambda blob: blob.path))


def blob(repo: Path, blob_id: str) -> bytes:
    return _run(repo, 'cat-file', 'blob', blob_id).stdout


def _run(repo: Path, *args: str, allowed: tuple[int, ...] = ()) -> subprocess.CompletedProcess[bytes]:
    # GIT_OPTIONAL_LOCKS=0: no command may take the index lock to refresh it in passing.
    env = {**os.environ, 'GIT_OPTIONAL_LOCKS': '0', 'LC_ALL': 'C'}
    command = next(arg for arg in args if not arg.startswith('-'))
    try:
        result = subprocess.run(['git', '-C', str(repo), *args], capture_output=True, env=env, check=False)
    except OSError as error:
        raise CodegraphError('git-failed', 'git could not be started') from error
    if result.returncode and result.returncode not in allowed:
        raise CodegraphError('git-failed', f'git {command} exited {result.returncode}')
    return result
