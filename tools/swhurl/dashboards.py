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

Each run also records which dashboard belongs to which app (``{"<app>": "<dashboard id>"}``) in the
``console/app-dashboards`` ConfigMap, so the console can link to an app's dashboard without a HyperDX key
of its own. It is written only when it changed, and holds nothing that the next run cannot rebuild.

HyperDX's external API (``/api/v2``) authenticates with a user's access key; the command reads the admin
account's key from MongoDB, as ``make clickstack-bootstrap`` reads the team key, and never prints it.
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import re
from dataclasses import dataclass
from pathlib import Path

from swhurl import app_alerts, clickstack, kube, statestore
from swhurl.apps import helmrelease
from swhurl.apps.instance import NAME_RE, Instance
from swhurl.apps.model import store
from swhurl.platform import ROOT
from swhurl.report import Report
from swhurl.run import CommandError, Runner

TAG = app_alerts.TAG
NAMESPACE = "ResourceAttributes['k8s.namespace.name']"
APP_DASHBOARD = re.compile('App: ' + NAME_RE.pattern.strip('^$'))  # what the scheduled sync may delete
IN_GIT, ON_CLUSTER = 'its app is not in Git', 'its app has no HelmRelease on the cluster'
RECORD = {'namespace': 'console', 'name': 'app-dashboards', 'key': 'dashboards.json'}  # read by the console
DASHBOARD_ID = re.compile(r'[0-9a-f]{24}')  # a MongoDB ObjectId: the only thing that may become part of a link
ADMIN_KEY = ('const u = db.users.findOne({email: EMAIL}, {accessKey: 1});'
             ' print("RESULT " + JSON.stringify({key: (u && u.accessKey) || ""}));')


@dataclass(frozen=True)
class App:
    name: str
    kind: str  # web, worker or job (helmrelease.kind)
    envs: tuple[str, ...]
    alerts: str = ''  # its own alert settings (app_alerts.ANNOTATION); '' for the defaults

    @property
    def namespaces(self) -> str:
        return ', '.join(f"'{Instance(self.name, env).namespace}'" for env in self.envs)


def apps(root: Path = ROOT) -> list[App]:
    """Apps in Git, from their Release and App records, including records not rendered yet."""
    world, errors = store.world(root)
    if errors:
        raise ValueError('; '.join(errors))
    found: dict[str, dict] = {}
    for each, record in world.items():
        entry = found.setdefault(each.app, {'kind': record.release.kind, 'envs': [], 'alerts': record.app.alerts})
        entry['envs'].append(each.env)
    return [App(name, entry['kind'], tuple(sorted(entry['envs'], key=lambda e: e != 'staging')), entry['alerts'])
            for name, entry in sorted(found.items())]


RELEASES = ['kubectl', 'get', 'helmreleases.helm.toolkit.fluxcd.io', '--all-namespaces', '-o', 'json']
NAMESPACES = ['kubectl', 'get', 'namespaces', '-o', 'json']  # an app's alert settings are an annotation on its namespaces


def cluster_apps(runner: Runner) -> list[App]:
    """Discover applied app environments, including installs still becoming Ready.

    Match the generated Flux unit, release and namespace together so shared services
    and manually installed releases never become app dashboards. Discovery failures
    propagate before any API write.
    """
    found: dict[str, dict] = {}
    settings = {n['metadata']['name']: str((n['metadata'].get('annotations') or {}).get(app_alerts.ANNOTATION, ''))
                for n in runner.json(NAMESPACES)['items']}
    for release in runner.json(RELEASES)['items']:
        metadata = release['metadata']
        labels = metadata.get('labels') or {}
        unit = labels.get('kustomize.toolkit.fluxcd.io/name', '')
        found_instance = Instance.of_unit(unit)
        if not found_instance or labels.get('kustomize.toolkit.fluxcd.io/namespace') != 'flux-system':
            continue
        app, env = found_instance.app, found_instance.env
        if metadata['name'] != app or metadata['namespace'] != found_instance.namespace:
            continue
        kind = helmrelease.kind(release)
        entry = found.setdefault(app, {'kind': kind, 'envs': [], 'alerts': ''})
        entry['envs'].append(env)
        entry['alerts'] = entry['alerts'] or settings.get(found_instance.namespace, '')
    return [App(name, entry['kind'], tuple(sorted(set(entry['envs']), key=lambda e: e != 'staging')), entry['alerts'])
            for name, entry in sorted(found.items())]


CHART_HEIGHT = 9  # grid rows; 3 (HyperDX's usual) is too short to read a line chart


def _line(name: str, x: int, y: int, w: int, source: str, select: dict) -> dict:
    return {'name': name, 'x': x, 'y': y, 'w': w, 'h': CHART_HEIGHT,
            'config': {'displayType': 'line', 'sourceId': source, 'groupBy': NAMESPACE, 'select': [select]}}


def job_tiles(mine: str, traces: str, logs: str) -> list[dict]:
    """A scheduled job's tiles: its runs (each a root span \"run\", which the templates make), how many
    failed and how long they took, its last runs, and its logs. It has no errors alert: the notification
    checker sends \"run failed\" for each failed run already (docs/apps.md#dashboards)."""
    runs = f"{mine} AND ParentSpanId = '' AND SpanName = 'run'"

    def select(where: str, **extra) -> dict:
        return {'aggFn': 'count', 'where': where, 'whereLanguage': 'sql', **extra}

    return [
        _line('Runs', 0, 0, 8, traces, select(runs, alias='runs')),
        _line('Failed runs', 8, 0, 8, traces, select(f"{runs} AND StatusCode = 'Error'", alias='failed')),
        _line('Run duration p95 (s)', 16, 0, 8, traces,
              {'aggFn': 'quantile', 'level': 0.95, 'valueExpression': 'Duration / 1e9', 'where': runs,
               'whereLanguage': 'sql', 'alias': 'p95 s'}),
        {'name': 'Last runs', 'x': 0, 'y': CHART_HEIGHT, 'w': 12, 'h': CHART_HEIGHT,
         'config': {'displayType': 'search', 'sourceId': traces, 'whereLanguage': 'sql', 'where': runs,
                    'select': f"Timestamp, {NAMESPACE} AS environment, StatusCode AS status, "
                              "round(Duration / 1e9, 1) AS seconds, StatusMessage AS reason"}},
        _line('Error logs', 12, CHART_HEIGHT, 12, logs,
              select(f"{mine} AND lower(SeverityText) IN ('error', 'fatal')", alias='errors')),
        {'name': 'Latest logs', 'x': 0, 'y': 2 * CHART_HEIGHT, 'w': 24, 'h': 6,
         'config': {'displayType': 'search', 'sourceId': logs, 'whereLanguage': 'sql', 'where': mine,
                    'select': f"Timestamp, {NAMESPACE} AS environment, SeverityText, Body"}},
    ]


def dashboard(app: App, traces: str, logs: str) -> dict:
    """The dashboard definition for ``app`` (request body for create and update)."""
    mine = f'{NAMESPACE} IN ({app.namespaces})'
    if app.kind == 'job':
        return {'name': f'App: {app.name}', 'tags': [TAG], 'tiles': job_tiles(mine, traces, logs)}
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


def recorded(runner: Runner) -> dict[str, str]:
    """``{app: dashboard id}`` as last recorded; empty when nothing usable is there. Entries that are
    not an app name and a dashboard id are dropped, so a link is only ever built from those."""
    try:
        doc = json.loads(statestore.read_key(runner, **RECORD) or '{}')
    except (CommandError, ValueError):
        return {}
    if not isinstance(doc, dict):
        return {}
    return {app: ident for app, ident in doc.items()
            if isinstance(app, str) and isinstance(ident, str) and NAME_RE.match(app) and DASHBOARD_ID.fullmatch(ident)}


def record(runner: Runner, report: Report, ids: dict[str, str]) -> None:
    """Keep the console's record of each app's dashboard in step, writing only when it changed."""
    raw = json.dumps(ids, sort_keys=True)
    try:
        if statestore.read_key(runner, **RECORD) == raw:
            return
        statestore.write_key(runner, **RECORD, raw=raw, field_manager='dashboard-sync')
        report.ok(f'recorded {len(ids)} dashboard address{"es" if len(ids) != 1 else ""} for the console')
    except CommandError as error:
        report.bad(f'could not record dashboard addresses in {RECORD["namespace"]}/{RECORD["name"]}: {error}')


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
    ids: dict[str, str] = {}
    for app in wanted_apps:
        body = dashboard(app, traces, logs)
        current = live.pop(body['name'], None)
        ids[app.name] = str((current or {}).get('id', ''))
        if current and same(current, body):
            report.ok(f"{body['name']} is up to date")
        elif runner.dry_run:
            report.info(f"would {'update' if current else 'create'} {body['name']}")
        elif current:
            api.call('PUT', f"/dashboards/{current['id']}", body)
            report.ok(f"updated {body['name']}")
        else:
            created = api.call('POST', '/dashboards', body).get('data')
            ids[app.name] = str(created.get('id', '')) if isinstance(created, dict) else ''  # else the next run has it
            report.ok(f"created {body['name']}")
    if not runner.dry_run and (wanted_apps or prune):
        record(runner, report, {app: ident for app, ident in ids.items() if DASHBOARD_ID.fullmatch(ident)})
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
    # Each app's default alerts watch tiles of the dashboard as it is now: read again, since a
    # created or updated dashboard has new tile ids.
    current = {d['name']: d for d in api.call('GET', '/dashboards').get('data', []) if TAG in (d.get('tags') or [])}
    app_alerts.reconcile(api, report, {app.name: current[f'App: {app.name}'] for app in wanted_apps
                                       if f'App: {app.name}' in current}, prune=prune, dry_run=runner.dry_run,
                          specs={app.name: app.alerts for app in wanted_apps})
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    parser = argparse.ArgumentParser(prog='swhurl clickstack-dashboards', description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--cluster', action='store_true',
                        help='discover Flux-managed live app environments; delete only dashboards named for an app with no '
                             'HelmRelease left, and none when no app release is found')
    args = parser.parse_args(argv)
    runner = runner or Runner.from_environment()
    report = report or Report(redact=runner.redact)
    try:
        wanted = cluster_apps(runner) if args.cluster else apps()
    except ValueError as error:
        report.bad(str(error))
        return 1
    return sync(runner, report, wanted,
                prune=bool(wanted) or not args.cluster, cluster=args.cluster)


def check_dashboards(runner: Runner, report: Report, now: dt.datetime | None = None) -> None:
    """A successful minute-by-minute sync of every app's dashboard and default alerts must be recent, and
    scheduling enabled. The job also fails while HyperDX lacks the webhook the alerts need."""
    report.section('App dashboards and alerts')
    try:
        state = kube.cronjob_run(runner, 'console', 'console-dashboards', now)
    except (CommandError, KeyError, ValueError, TypeError):
        report.bad('cannot read dashboard sync status (console/console-dashboards; make reconcile UNIT=platform-console)')
        return
    if state == 'suspended':
        report.bad('dashboard and alert sync is suspended, so app dashboards and alerts do not follow Git: '
                   'kubectl -n console patch cronjob console-dashboards -p \'{"spec":{"suspend":false}}\'')
    elif state == 'stale':
        report.bad('dashboard and alert sync has not succeeded in 5 minutes (kubectl -n console get jobs; kubectl -n '
                   'console logs job/<newest console-dashboards-...>). No "swhurl notify" webhook: make '
                   'clickstack-notify-webhook; no new jobs at all: docs/operations.md#troubleshooting')
    else:
        report.ok('dashboard and alert sync succeeded in the last 5 minutes')
