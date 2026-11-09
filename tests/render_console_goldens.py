"""Render the console's pages from the Python console, for the Go templates' byte comparisons.

The Go templates are accepted only when they produce the same bytes as the console they replace. This script is the
other half of that test: it renders each page from the fixture in test_console.py, with the clock fixed so the same
page renders the same bytes on any machine, and writes it under v2/cmd/swhurl-v2/testdata/.

Run it from the repository root after changing the Python console, the fixtures or a Go template:

    PYTHONPATH=tools:tests uv run python tests/render_console_goldens.py

A page whose Go template does not exist yet is skipped, so the script grows one entry per ported page.
"""
from __future__ import annotations

import base64
import datetime as dt
import json
import sys
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import httpx
import test_console as tc
from console_observation import flags_card, inventory, report

from swhurl.console import actions, proposal, repos, stages
from swhurl.console import github as gh
from swhurl.console import v2 as console_v2
from swhurl.run import Result

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


APP_UNITS = [tc.unit('app-demo-orders-staging'), tc.unit('app-demo-orders-prod'),
             tc.unit('app-demo-shop-staging'), tc.unit('app-demo-shop-prod')]
"""Two apps, both environments, so one instance's page has a callee, a caller, a route, a database and an egress grant."""

TEAMS = {
    'demo': {'description': 'Apps that show how to use the platform', 'channel': {'name': 'platform', 'transport': 'ntfy'}},
    'payments': {'description': 'Orders and payments', 'channel': {'name': 'payments', 'transport': 'ntfy'}},
}
TEAMS_JSON = json.dumps({'schemaVersion': 1, 'teams': TEAMS})
ROUTES_JSON = json.dumps({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': 'notification-routes', 'namespace': 'notify'},
                          'data': {'routes.json': json.dumps({'channels': {'platform': 'ntfy', 'payments': 'ntfy'},
                                                              'teams': {'demo': ['platform'], 'payments': ['payments']}})}})
NO_FLAGS_KEY = 'This console has no NATS key (console-nats Secret), so overrides cannot be read or set.'


def app_release(app, env, tag, *, calls=(), allows=(), publishes=(), subscriptions=(), database=False, egress='',
                team='demo'):
    """An instance's Helm release, as the render compiles the records: the page reads its env, host and image."""
    host = f'{app}.homelab.swhurl.com' if env == 'prod' else f'staging-{app}.homelab.swhurl.com'
    variables = {'SWHURL_IMAGE': f'ghcr.io/samclement/{app}', 'SWHURL_TEAM': team,
                 'SWHURL_ALLOWED_CALLERS': ','.join(allows)}
    if publishes:
        variables['SWHURL_EVENTS_PUBLISH'] = ','.join(publishes)
    if subscriptions:
        variables['SWHURL_EVENTS_SUBSCRIBE'] = ','.join(subscriptions)
    for peer in calls:
        variables[f'SWHURL_SERVICE_{peer.upper().replace("-", "_")}_URL'] = \
            f'http://{peer}.{peer}-{env}.svc.cluster.local:8080'
    if database:
        variables['DATABASE_PATH'] = '/data/app.db'
    if egress:
        variables['HTTPS_PROXY'] = 'http://egress.platform.svc.cluster.local:7001'
    annotations = {'platform.swhurl.com/egress': egress} if egress else {}
    return {'metadata': {'namespace': f'{app}-{env}', 'name': app, 'annotations': annotations},
            'spec': {'values': {'controllers': {'main': {'containers': {'main': {
                'image': {'repository': f'ghcr.io/samclement/{app}', 'tag': tag}, 'env': variables}}}},
                'service': {'main': {'ports': {'http': {'port': 8080}}}},
                'ingress': {'main': {'hosts': [{'host': host}]}}}},
            'status': {'conditions': [{'type': 'Ready', 'status': 'True', 'message': 'ok'}]}}


APP_RELEASES = [
    app_release('demo-shop', 'staging', '2.0', calls=['demo-orders'], allows=['demo-orders'], publishes=['item-added'],
                subscriptions=['demo-orders.paid'], database=True, egress='api.stripe.com'),
    app_release('demo-shop', 'prod', '1.0', calls=['demo-orders'], allows=['demo-orders'], publishes=['item-added'],
                subscriptions=['demo-orders.paid'], database=True, egress='api.stripe.com'),
    app_release('demo-orders', 'staging', '3.0', publishes=['paid']),
    app_release('demo-orders', 'prod', '3.0', publishes=['paid']),
]

APP = {'app.html': '/apps/demo-shop/staging', 'app-prod.html': '/apps/demo-shop/prod',
       'pending-removing.html': '/apps/demo-gone/staging', 'pending-absent.html': '/apps/demo-waiting/prod',
       'promotion.html': '/apps/demo-shop/staging/promotion',
       'new-repo.html': '/new'}
"""An instance deployed, one being removed, one whose records Flux has not applied, a promotion to review and the
New app form; the pending page for an app not created yet is below."""


def app_cluster():
    runner = tc.fake(units=APP_UNITS, releases=APP_RELEASES)
    runner.status_inputs['v2-records/teams.json'] = TEAMS_JSON
    runner.status_inputs['v2/testdata/profile.json'] = (TESTS.parent / 'v2' / 'testdata' / 'profile.json').read_text()
    runner.removing = [('demo-gone', 'staging', 'unit')]
    runner.status_reports['demo-waiting', 'prod'] = {**report('demo-waiting', 'prod'), 'live': {
        **report('demo-waiting', 'prod')['live'], 'unit': {'status': 'absent', 'suspended': False, 'conditions': []}}}
    runner.inventories['demo-waiting', 'prod'] = inventory('demo-waiting', 'prod')
    runner.on(console_v2.BINARY, '--help', stdout='Usage: swhurl-v2 <command>')
    runner._rules.insert(0, (('swhurl-v2', 'flags'),
                             Result((), 0, flags_card(problem=NO_FLAGS_KEY, writable=False), '')))
    return runner


def github_contents(request: httpx.Request) -> httpx.Response:
    """The registry and the generated routes as the console reads them from main, and no console pull request."""
    path = request.url.path
    if path.endswith('/v2-records/teams.json'):
        return httpx.Response(200, json={'content': base64.b64encode(TEAMS_JSON.encode()).decode(), 'encoding': 'base64'})
    if path.endswith('/platform/notify/routes.json'):
        return httpx.Response(200, json={'content': base64.b64encode(ROUTES_JSON.encode()).decode(), 'encoding': 'base64'})
    if path.endswith('/pulls'):
        return httpx.Response(200, json=[])
    return httpx.Response(404, json={})


def dump_fixture() -> None:
    """The same documents and rows the Go test builds from, so the byte comparison starts from one fixture."""
    from console_observation import HEAD, answer
    runner = app_cluster()
    document = json.loads(answer(['status', '--all', f'--revision={HEAD}'], runner.status_reports,
                                 runner.inventories, runner.removing).stdout)
    jobs = activity_jobs()
    dump = {'now': '2026-11-09T09:43:00Z', 'instances': document['instances'], 'removing': document['removing'],
            'files': runner.status_inputs,
            'pulls': [{'number': 44, 'branch': 'console/new-shop-staging-abc1234', 'title': '[console] new app: shop',
                       'url': f'{REPO}/pull/44', 'body': '', 'opened': '2026-11-09'}],
            'jobs': [{'id': job.id, 'action': job.action, 'unit': job.unit, 'identity': job.identity,
                      'started': job.started.isoformat(), 'state': job.state,
                      'finished': job.finished.isoformat() if job.finished else '', 'link': job.link or '',
                      'lines': job.lines, 'resume': job.resume} for job in jobs.recent()]}
    (GOLDENS / 'app-fixture.json').write_text(json.dumps(dump, indent=1, sort_keys=True) + '\n')
    print(f'app-fixture.json: {len(dump["instances"])} instances, {len(dump["files"])} files')


def render_app() -> int:
    runner = app_cluster()
    github = gh.GitHub('samclement/swhurl-platform', 'tok', transport=httpx.MockTransport(github_contents))
    client = tc.client(runner, github=github, documents=runner.status_inputs.get,
                       app_repos=repos.AppReposToken('apps-token'))
    dump_fixture()
    if status := render(client, APP):
        return status
    return render_pending_new()


def render_pending_new() -> int:
    """A new app's job and pull request, for an instance that has no report yet."""
    jobs = activity_jobs()
    client = tc.client(jobs.runner, jobs=jobs, documents=jobs.runner.status_inputs.get,
                       github=gh.GitHub('samclement/swhurl-platform', 'tok',
                                        transport=httpx.MockTransport(pending_answers)))
    return render(client, {'pending-new.html': '/apps/shop/staging'})


def pending_answers(request: httpx.Request) -> httpx.Response:
    if request.url.path.endswith('/pulls'):
        return httpx.Response(200, json=[{'number': 44, 'title': '[console] new app: shop', 'html_url': f'{REPO}/pull/44',
                                          'body': '', 'created_at': '2026-11-09T09:40:00Z',
                                          'head': {'ref': 'console/new-shop-staging-abc1234', 'sha': 'f' * 40}}])
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
            print(response.text[:4000], file=sys.stderr)
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
        if status := render_activity():
            return status
        return render_app()
    return 0


if __name__ == '__main__':
    sys.exit(main())
