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

import httpx
import test_console as tc
from console_observation import inventory, report

from swhurl.console import actions, proposal, repos, stages
from swhurl.console import github as gh

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


ACTIVITY = {
    'activity.html': '/activity',
    'job-open.html': '/jobs/8',
    'job-running.html': '/jobs/9',
    'job-resumable.html': '/jobs/5',
    'job-merged.html': '/jobs/7',
}
"""Activity and a job's page, from the fixture below: jobs in every state, read from the store (another pod's
records, so none streams), and their pull requests open, merged, closed and one no job opened."""

REPO = 'https://github.com/samclement/swhurl-platform'
MERGED, MAIN = 'c' * 40, 'a' * 40


def at(hour: int, minute: int) -> dt.datetime:
    return dt.datetime(2026, 11, 9, hour, minute, tzinfo=dt.UTC)


def activity_jobs() -> actions.Jobs:
    """The jobs the Go test states too (cmd/swhurl-v2/console_activity_test.go)."""
    runner = tc.fake()
    jobs = actions.Jobs(runner, inline=True)
    for job in (
        actions.Job(9, 'reconcile', 'infra-base', 'sam@swhurl.com', at(9, 42),
                    ['Reconciling', 'revision 1234567890abcdef1234567890abcdef12345678 <applied>']),
        actions.Job(8, 'scale', 'web/staging', 'sam@swhurl.com', at(9, 30), ['Opened'], 'succeeded', at(9, 31), f'{REPO}/pull/42'),
        actions.Job(7, 'promote', 'web/prod', 'sam@swhurl.com', at(8, 0), ['Downloading', 'Opened'], 'succeeded', at(8, 2),
                    f'{REPO}/pull/41'),
        actions.Job(6, 'remove', 'my-api/staging', 'sam@swhurl.com', at(7, 0), ['[ERROR] <b>refused</b>'], 'failed', at(7, 0)),
        actions.Job(5, 'new app and repository', 'shop/staging', 'sam@swhurl.com', at(6, 0), ['created repository'],
                    'interrupted', at(6, 10), resume={'kind': proposal.NEW_APP}),
        actions.Job(4, 'team add', 'team:payments', 'sam@swhurl.com', dt.datetime(2026, 11, 7, 12, 0, tzinfo=dt.UTC), [],
                    'succeeded', dt.datetime(2026, 11, 7, 12, 1, tzinfo=dt.UTC), f'{REPO}/pull/40'),
        actions.Job(3, 'resume', 'infra-base', 'sam@swhurl.com', at(5, 0), ['✔ applied revision main@sha1:abc1234'],
                    'succeeded', at(5, 2)),
    ):
        job.live = False
        jobs._before[job.id] = job
    return jobs


def github_answers(request: httpx.Request) -> httpx.Response:
    """GitHub as the Go test's fake states it: two open console pull requests, one merged and one closed."""
    path = request.url.path
    head = {'ref': 'console/v2-scale-web-staging-abc1234', 'sha': 'd' * 40}
    opened = [
        {'number': 43, 'title': '[console] apps: change access to web/prod', 'html_url': f'{REPO}/pull/43', 'body': '',
         'created_at': '2026-11-08T10:00:00Z', 'head': {'ref': 'console/v2-expose-web-prod-abc1234', 'sha': 'e' * 40}},
        {'number': 42, 'title': '[console] apps: scale web/staging', 'html_url': f'{REPO}/pull/42',
         'body': 'Scale it.\n\n**Merge plan:** it needs Secret values to be set.\n', 'created_at': '2026-11-09T09:31:00Z', 'head': head},
    ]
    if path.endswith('/pulls'):
        return httpx.Response(200, json=opened)
    if path.endswith('/actions/runs'):
        sha = request.url.params['head_sha']
        run = {'name': 'Validate', 'head_sha': sha, 'status': 'completed', 'html_url': f'{REPO}/actions/runs/{sha[:4]}',
               'conclusion': 'success' if sha == 'd' * 40 else 'failure'}
        return httpx.Response(200, json={'workflow_runs': [run]})
    number = int(path.rsplit('/', 1)[1]) if path.rsplit('/', 1)[1].isdigit() else 0
    if number == 41:
        return httpx.Response(200, json={'number': 41, 'state': 'closed', 'merged_at': '2026-11-09T08:30:00Z',
                                         'merge_commit_sha': MERGED, 'mergeable_state': 'unknown'})
    if number == 40:
        return httpx.Response(200, json={'number': 40, 'state': 'closed', 'merged_at': None,
                                         'closed_at': '2026-11-08T12:00:00Z', 'mergeable_state': 'unknown'})
    if number in (42, 43):
        return httpx.Response(200, json={'number': number, 'state': 'open', 'mergeable_state': 'clean'})
    return httpx.Response(404, json={})


def render_activity() -> int:
    """Activity and the job pages, with web/prod's promotion merged at MERGED and running on the cluster."""
    render_target = stages.Target('records-1', 'settings-1', 'sha256:x', '', 'sha256:x', '', v2=True)
    github = gh.GitHub('samclement/swhurl-platform', 'tok', transport=httpx.MockTransport(github_answers))
    jobs = activity_jobs()
    client = tc.client(jobs.runner, jobs=jobs, github=github, app_repos=repos.AppReposToken('apps-token'))
    with mock.patch.object(tc.server.Files, 'revision', return_value=MAIN), \
            mock.patch.object(tc.server.Files, 'inputs', return_value=(MAIN, Path('/unused'))), \
            mock.patch('swhurl.console.status.read', return_value={
                'instances': {('web', 'prod'): {'report': report(), 'inventory': inventory('web', 'prod')}}, 'removing': ()}), \
            mock.patch.object(stages, 'render_targets', return_value={'web/prod': render_target}):
        if status := render(client, ACTIVITY):
            return status
        return render(client, {'job-missing.html': '/jobs/99'}, status=404)


def render(client, pages: dict[str, str], status: int = 200) -> int:
    for name, path in pages.items():
        response = client.get(path, headers=tc.WHO)
        if response.status_code != status:
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
        if status := render(tc.client(described.described_cluster(), documents=described.documents()), DESCRIBED):
            return status
        return render_activity()
    return 0


if __name__ == '__main__':
    sys.exit(main())
