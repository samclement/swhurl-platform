"""check-apps: acquire the pinned chart, then run v2's whole-tree render and policy checks.

Policy decisions and record compilation belong to Go. This entrypoint accepts no paths or flags.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

import yaml

from swhurl import ROOT, charts, platform
from swhurl.run import CommandError, Runner

RENDERED = Path('v2/check-rendered.sh')


def main(argv: list[str] | None = None, runner: Runner | None = None, *, root: Path = ROOT) -> int:
    if argv:
        print('usage: check-apps (no arguments; validates the whole tree)', file=sys.stderr)
        return 2
    runner = runner or Runner.from_environment(cwd=root)
    try:
        chart = json.loads((root / platform.PROFILE).read_text())['chart']
        name, version = chart['name'], chart['version']
        repository = charts.helm_repositories(root)[chart['repository']]
    except (OSError, ValueError, KeyError, TypeError, AttributeError, yaml.YAMLError):
        print(f'[BAD] {platform.PROFILE} or {platform.HELM_REPOSITORIES}: '
              'invalid or unreadable pinned chart metadata', file=sys.stderr)
        return 1
    try:
        local_chart = charts.chart_dir(name, version, repository, runner)
        result = runner.run(['bash', str(root / RENDERED), '--chart', str(local_chart)], cwd=root, check=False)
    except (CommandError, OSError) as error:
        print(f'[BAD] check-apps: {error}', file=sys.stderr)
        return 1
    # Keep Go's JSON report and source/render diagnostics intact, on their original streams.
    print(result.stdout, end='')
    print(result.stderr, end='', file=sys.stderr)
    if result.returncode:
        print(f'[BAD] {RENDERED} exited {result.returncode}: render comparison or Go Kubernetes policy failed')
    else:
        print("[OK] v2's records render to Git and every compiled instance passes Go Kubernetes policy")
    return result.returncode
