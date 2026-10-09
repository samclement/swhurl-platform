"""ClickStack dashboards for apps, from Git: one dashboard per app in ``apps/``, kept in step by this command.

Each app gets the dashboard ``App: <app>`` (tag ``swhurl-app``) with its environments as separate lines:

- web: requests per minute, server errors and p95 latency (server spans);
- worker: runs per minute, errors and p95 duration (root spans);
- both: error logs and all log lines per minute, and the latest log lines.

Every tile filters on the app's namespaces (``<app>-staging``, ``<app>-prod``) rather than its service name,
so an app without an OpenTelemetry SDK still gets its log tiles. The definitions below are the source of
truth: the command creates missing dashboards, overwrites changed ones (edits made in the HyperDX UI are
lost) and deletes ``swhurl-app`` dashboards whose app is gone from Git.

The scheduled sync (``--cluster``) takes the apps from the cluster's HelmReleases instead and deletes more
narrowly: only a tagged dashboard named exactly ``App: <app name>`` whose app has no HelmRelease left, and
nothing at all when it finds no app releases (a cluster still being restored).

HyperDX's external API (``/api/v2``) authenticates with a user's access key; the command reads the admin
account's key from MongoDB, as ``make clickstack-bootstrap`` reads the team key, and never prints it.
"""
from __future__ import annotations

import argparse
import json
import re
from dataclasses import dataclass
from pathlib import Path

import yaml

from swhurl import clickstack
from swhurl.apps.new import NAME_RE
from swhurl.platform import ROOT
from swhurl.report import Report
from swhurl.run import CommandError, Runner

TAG = 'swhurl-app'
NAMESPACE = "ResourceAttributes['k8s.namespace.name']"
APP_DASHBOARD = re.compile('App: ' + NAME_RE.pattern.strip('^$'))  # what the scheduled sync may delete
IN_GIT, ON_CLUSTER = 'its app is not in Git', 'its app has no HelmRelease on the cluster'
ADMIN_KEY = ('const u = db.users.findOne({email: EMAIL}, {accessKey: 1});'
             ' print("RESULT " + JSON.stringify({key: (u && u.accessKey) || ""}));')


@dataclass(frozen=True)
class App:
    name: str
    kind: str  # web or worker
    envs: tuple[str, ...]

    @property
    def namespaces(self) -> str:
        return ', '.join(f"'{self.name}-{env}'" for env in self.envs)


def apps(paths: list[Path] | None = None) -> list[App]:
    """Apps in Git, from their instances' HelmReleases (a web app has a Service)."""
    found: dict[str, dict] = {}
    paths = paths if paths is not None else [p.parent for p in sorted((ROOT / 'apps').glob('*/*/helmrelease.yaml'))]
    for instance in paths:
        release = yaml.safe_load((instance / 'helmrelease.yaml').read_text())
        kind = 'web' if 'service' in (release['spec'].get('values') or {}) else 'worker'
        entry = found.setdefault(instance.parent.name, {'kind': kind, 'envs': []})
        entry['envs'].append(instance.name)
    return [App(name, entry['kind'], tuple(sorted(entry['envs'], key=lambda e: e != 'staging')))
            for name, entry in sorted(found.items())]


RELEASES = ['kubectl', 'get', 'helmreleases.helm.toolkit.fluxcd.io', '--all-namespaces', '-o', 'json']


def cluster_apps(runner: Runner) -> list[App]:
    """Discover applied app environments, including installs still becoming Ready.

    Match the generated Flux unit, release and namespace together so shared services
    and manually installed releases never become app dashboards. Discovery failures
    propagate before any API write.
    """
    found: dict[str, dict] = {}
    for release in runner.json(RELEASES)['items']:
        metadata = release['metadata']
        labels = metadata.get('labels') or {}
        unit = labels.get('kustomize.toolkit.fluxcd.io/name', '')
        match = re.fullmatch(r'app-([a-z0-9-]+)-(staging|prod)', unit)
        if not match or labels.get('kustomize.toolkit.fluxcd.io/namespace') != 'flux-system':
            continue
        app, env = match.groups()
        if metadata['name'] != app or metadata['namespace'] != f'{app}-{env}':
            continue
        kind = 'web' if 'service' in (release['spec'].get('values') or {}) else 'worker'
        entry = found.setdefault(app, {'kind': kind, 'envs': []})
        entry['envs'].append(env)
    return [App(name, entry['kind'], tuple(sorted(set(entry['envs']), key=lambda e: e != 'staging')))
            for name, entry in sorted(found.items())]


CHART_HEIGHT = 9  # grid rows; 3 (HyperDX's usual) is too short to read a line chart


def _line(name: str, x: int, y: int, w: int, source: str, select: dict) -> dict:
    return {'name': name, 'x': x, 'y': y, 'w': w, 'h': CHART_HEIGHT,
            'config': {'displayType': 'line', 'sourceId': source, 'groupBy': NAMESPACE, 'select': [select]}}


def dashboard(app: App, traces: str, logs: str) -> dict:
    """The dashboard definition for ``app`` (request body for create and update)."""
    mine = f'{NAMESPACE} IN ({app.namespaces})'
    spans = f"{mine} AND SpanKind = 'Server'" if app.kind == 'web' else f"{mine} AND ParentSpanId = ''"
    noun = 'Requests' if app.kind == 'web' else 'Runs'

    def select(where: str, **extra) -> dict:
        return {'aggFn': 'count', 'where': where, 'whereLanguage': 'sql', **extra}

    tiles = [
        _line(f'{noun} per minute', 0, 0, 8, traces, select(spans, alias=noun.lower())),
        _line('Errors', 8, 0, 8, traces, select(f"{spans} AND StatusCode = 'Error'", alias='errors')),
        _line('p95 duration (ms)', 16, 0, 8, traces,
              {'aggFn': 'quantile', 'level': 0.95, 'valueExpression': 'Duration / 1e6', 'where': spans,
               'whereLanguage': 'sql', 'alias': 'p95 ms'}),
        _line('Error logs', 0, CHART_HEIGHT, 12, logs, select(f"{mine} AND lower(SeverityText) IN ('error', 'fatal')", alias='errors')),
        _line('Log lines', 12, CHART_HEIGHT, 12, logs, select(mine, alias='lines')),
        {'name': 'Latest logs', 'x': 0, 'y': 2 * CHART_HEIGHT, 'w': 24, 'h': 6,
         'config': {'displayType': 'search', 'sourceId': logs, 'whereLanguage': 'sql', 'where': mine,
                    'select': f"Timestamp, {NAMESPACE} AS environment, SeverityText, Body"}},
    ]
    return {'name': f'App: {app.name}', 'tags': [TAG], 'tiles': tiles}


def same(live: dict, wanted: dict) -> bool:
    """Whether the live dashboard already matches (ignoring server-assigned tile ids, defaults and the empty
    ``valueExpression`` the server adds to a count)."""
    def tile(t: dict) -> dict:
        config = {k: v for k, v in (t.get('config') or {}).items() if k in TILE_CONFIG_KEYS}
        config['select'] = ([{k: v for k, v in s.items() if k in SELECT_KEYS and v != ''} for s in config['select']]
                            if isinstance(config.get('select'), list) else config.get('select'))
        return {**{k: t.get(k) for k in ('name', 'x', 'y', 'w', 'h')}, 'config': config}
    return (live.get('name') == wanted['name'] and sorted(live.get('tags') or []) == sorted(wanted['tags'])
            and [tile(t) for t in live.get('tiles') or []] == [tile(t) for t in wanted['tiles']])


TILE_CONFIG_KEYS = {'displayType', 'sourceId', 'groupBy', 'select', 'where', 'whereLanguage'}
SELECT_KEYS = {'aggFn', 'where', 'whereLanguage', 'alias', 'level', 'valueExpression'}


class HyperDX:
    """The external API from inside the app pod, with the admin's access key (registered as a secret)."""

    def __init__(self, runner: Runner, key: str):
        self.runner, self.headers = runner, {'authorization': f'Bearer {key}'}
        runner.add_secret(key)

    def call(self, method: str, path: str, body: dict | None = None) -> dict:
        response = clickstack.api(self.runner, method, f'/api/v2{path}', body, headers=self.headers,
                                  mutating=method != 'GET')
        if response['status'] >= 300:
            detail = json.dumps(response['body'])[:500] if response['body'] else 'no detail'
            raise CommandError(['hyperdx', method, path], f'HTTP {response["status"]}: {detail}')
        return response['body'] or {}


def sources(api: HyperDX) -> tuple[str, str]:
    """The ids of the trace and log sources on ``otel_traces`` and ``otel_logs``."""
    found = {}
    for source in api.call('GET', '/sources').get('data', []):
        table = (source.get('from') or {}).get('tableName')
        if (source.get('kind'), table) in (('trace', 'otel_traces'), ('log', 'otel_logs')):
            found.setdefault(source['kind'], source['id'])
    if set(found) != {'trace', 'log'}:
        raise CommandError(['hyperdx', 'GET', '/sources'],
                                      f'expected a trace source on otel_traces and a log source on otel_logs, found {sorted(found)}')
    return found['trace'], found['log']


def sync(runner: Runner, report: Report, wanted_apps: list[App], *, prune: bool = True, cluster: bool = False) -> int:
    """Create, update and (with ``prune``) delete; ``cluster`` narrows deletion to dashboards named for an app."""
    report.section('ClickStack app dashboards')
    inputs = clickstack.read_secret(runner, clickstack.INPUTS_SECRET)
    email = inputs.get('CLICKSTACK_ADMIN_EMAIL', '')
    uri = clickstack.read_secret(runner, clickstack.MONGO_URI_SECRET).get('connectionString.standard', '')
    if not (email and uri):
        report.bad('no admin email or MongoDB connection string; run make clickstack-bootstrap first')
        return report.exit_code()
    key = clickstack.mongo(runner, uri, f'const EMAIL = {json.dumps(email)}; {ADMIN_KEY}')['key']
    if not key:
        report.bad(f'{email} has no access key in HyperDX; sign in once, or run make clickstack-bootstrap')
        return report.exit_code()
    api = HyperDX(runner, key)
    traces, logs = sources(api)
    live = {d['name']: d for d in api.call('GET', '/dashboards').get('data', []) if TAG in (d.get('tags') or [])}
    for app in wanted_apps:
        body = dashboard(app, traces, logs)
        current = live.pop(body['name'], None)
        if current and same(current, body):
            report.ok(f"{body['name']} is up to date")
        elif runner.dry_run:
            report.info(f"would {'update' if current else 'create'} {body['name']}")
        elif current:
            api.call('PUT', f"/dashboards/{current['id']}", body)
            report.ok(f"updated {body['name']}")
        else:
            api.call('POST', '/dashboards', body)
            report.ok(f"created {body['name']}")
    if not prune and live:
        report.info(f'no app releases found; not deleting dashboards ({len(live)} left)')
    reason = ON_CLUSTER if cluster else IN_GIT
    for name, stale in sorted(live.items()) if prune else []:
        if cluster and not APP_DASHBOARD.fullmatch(name):
            report.info(f'left {name}: tagged {TAG} but not named for an app')
        elif runner.dry_run:
            report.info(f'would delete {name} ({reason})')
        else:
            api.call('DELETE', f"/dashboards/{stale['id']}")
            report.ok(f'deleted {name} ({reason})')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    parser = argparse.ArgumentParser(prog='swhurl clickstack-dashboards', description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--cluster', action='store_true',
                        help='discover Flux-managed live app environments; delete only dashboards named for an app with no '
                             'HelmRelease left, and none when no app release is found')
    args = parser.parse_args(argv)
    runner = runner or Runner.from_environment()
    wanted = cluster_apps(runner) if args.cluster else apps()
    return sync(runner, report or Report(redact=runner.redact), wanted,
                prune=bool(wanted) or not args.cluster, cluster=args.cluster)
