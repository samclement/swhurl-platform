"""Shared profile-pinned Helm chart acquisition for offline checks."""
from __future__ import annotations

import os
import tempfile
from pathlib import Path

from swhurl import ROOT, platform, yamlio
from swhurl.run import Runner

CACHE = Path(os.environ.get('XDG_CACHE_HOME', Path.home() / '.cache')) / 'swhurl-platform/charts'


def helm_repositories(root: Path = ROOT) -> dict[str, str]:
    path = root / platform.HELM_REPOSITORIES
    return {d['metadata']['name']: d['spec']['url'] for d in yamlio.load_all(path.read_text())
            if d and d.get('kind') == 'HelmRepository'}


def chart_dir(name: str, version: str, repo: str, runner: Runner) -> Path:
    target = CACHE / f'{name}-{version}'
    if not (target / 'Chart.yaml').exists():
        CACHE.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=CACHE) as tmp:
            runner.run(['helm', 'pull', name, '--repo', repo, '--version', version, '--untar', '--untardir', tmp])
            try:
                (Path(tmp) / name).rename(target)
            except OSError:
                # A parallel check (make check runs its targets together) pulled it first.
                if not (target / 'Chart.yaml').exists():
                    raise
    return target
