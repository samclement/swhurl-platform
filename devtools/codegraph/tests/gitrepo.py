"""Fixture Git repositories built by a test in its own temporary directory, isolated from the user's Git config."""
from __future__ import annotations

import os
import subprocess
from collections.abc import Mapping
from pathlib import Path

ENV = {**os.environ, 'GIT_CONFIG_GLOBAL': os.devnull, 'GIT_CONFIG_NOSYSTEM': '1',
       'GIT_AUTHOR_NAME': 'fixture', 'GIT_AUTHOR_EMAIL': 'fixture@example.invalid', 'GIT_AUTHOR_DATE': '2026-01-01T00:00:00Z',
       'GIT_COMMITTER_NAME': 'fixture', 'GIT_COMMITTER_EMAIL': 'fixture@example.invalid',
       'GIT_COMMITTER_DATE': '2026-01-01T00:00:00Z'}


def git(repo: Path, *args: str) -> str:
    return subprocess.run(['git', '-C', str(repo), *args], check=True, capture_output=True, text=True, env=ENV).stdout


def make_repo(repo: Path, *commits: Mapping[str, str | bytes | None]) -> list[str]:
    """Commit each mapping of path to content in turn (None deletes the path); return the commit IDs."""
    repo.mkdir(parents=True, exist_ok=True)
    git(repo, 'init', '-q', '-b', 'main')
    ids = []
    for number, files in enumerate(commits):
        for path, content in files.items():
            target = repo / path
            if content is None:
                target.unlink()
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(content if isinstance(content, bytes) else content.encode())
        git(repo, 'add', '-A')
        git(repo, 'commit', '-q', '--allow-empty', '-m', f'commit {number}')
        ids.append(git(repo, 'rev-parse', 'HEAD').strip())
    return ids
