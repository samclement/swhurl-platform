"""Render the console's pages from the Python console, for the Go templates' byte comparisons.

The Go templates are accepted only when they produce the same bytes as the console they replace. This script is the
other half of that test: it renders each page from the fixture in test_console.py, with the clock fixed so the same
page renders the same bytes on any machine, and writes it under v2/cmd/swhurl-v2/testdata/.

Run it from the repository root after changing the Python console, the fixtures or a Go template:

    PYTHONPATH=tools:tests uv run python tests/render_console_goldens.py

A page whose Go template does not exist yet is skipped, so the script grows one entry per ported page.
"""
from __future__ import annotations

import datetime as dt
import sys
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import test_console as tc

TESTS = Path(__file__).parent
GOLDENS = TESTS.parent / 'v2' / 'cmd' / 'swhurl-v2' / 'testdata'


class FixedClock(dt.datetime):
    """The moment every page in these goldens was read: fixed, so a golden is reproducible."""

    @classmethod
    def now(cls, tz=None):
        moment = dt.datetime(2026, 11, 9, 9, 43)  # noqa: DTZ001 - local, as the console takes it
        return moment.replace(tzinfo=tz) if tz else moment


PAGES = {
    'overview.html': '/',
    'platform.html': '/platform',
    'apps.html': '/apps',
    'apps-map.html': '/apps?view=map',
    'egress.html': '/egress',
    'unit.html': '/units/infra-base',
    'teams.html': '/teams',
    'catalogue.html': '/catalogue',
}

DESCRIBED = {
    'catalogue-described.html': '/catalogue',
    'catalogue-empty.html': '/catalogue?env=prod&kind=event&team=nobody',
}
"""The catalogue's states, from the fixture that gives apps their own OpenAPI and AsyncAPI documents
(test_console.py's catalogue cases): what an app describes, a worker that offers no API, and a filter that leaves
nothing."""


def render(client, pages: dict[str, str]) -> int:
    for name, path in pages.items():
        response = client.get(path, headers=tc.WHO)
        if response.status_code != 200:
            print(f'{name}: {path} answered {response.status_code}', file=sys.stderr)
            return 1
        (GOLDENS / name).write_text(response.text)
        print(f'{name}: {path} ({len(response.text)} bytes)')
    return 0


def main() -> int:
    runner = tc.fake(**tc.CHECKS)
    client = tc.client(runner)
    described = tc.ConnectionsTests()
    frozen = SimpleNamespace(**{name: getattr(tc.server.dt, name) for name in dir(tc.server.dt) if not name.startswith('_')})
    frozen.datetime = FixedClock
    GOLDENS.mkdir(parents=True, exist_ok=True)
    with mock.patch.object(tc.server, 'dt', frozen):
        if status := render(client, PAGES):
            return status
        return render(tc.client(described.described_cluster(), documents=described.documents()), DESCRIBED)
    return 0


if __name__ == '__main__':
    sys.exit(main())
