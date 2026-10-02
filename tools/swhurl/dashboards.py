"""ClickStack dashboards for apps, from Git: one dashboard per app in ``apps/``, kept in step by this command.

Each app gets the dashboard ``App: <app>`` (tag ``swhurl-app``) with its environments as separate lines:

- web: requests per minute, server errors and p95 latency (server spans);
- worker: runs per minute, errors and p95 duration (root spans);
- both: error logs and all log lines per minute, and the latest log lines.

Every tile filters on the app's namespaces (``<app>-staging``, ``<app>-prod``) rather than its service name,
so an app without an OpenTelemetry SDK still gets its log tiles. The definitions below are the source of
truth: the command creates missing dashboards, overwrites changed ones (edits made in the HyperDX UI are
lost) and deletes ``swhurl-app`` dashboards whose app is gone from Git.

HyperDX's external API (``/api/v2``) authenticates with a user's access key; the command reads the admin
account's key from MongoDB, as ``make clickstack-bootstrap`` reads the team key, and never prints it.
"""
from __future__ import annotations

import argparse
import json
from dataclasses import dataclass
from pathlib import Path

import yaml

from swhurl import clickstack
from swhurl.platform import ROOT
from swhurl.report import Report
from swhurl.run import CommandError, Runner

TAG = 'swhurl-app'
NAMESPACE = "ResourceAttributes['k8s.namespace.name']"
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


def _line(name: str, x: int, y: int, w: int, source: str, select: dict) -> dict:
    return {'name': name, 'x': x, 'y': y, 'w': w, 'h': 3,
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
        _line('Error logs', 0, 3, 12, logs, select(f"{mine} AND lower(SeverityText) IN ('error', 'fatal')", alias='errors')),
        _line('Log lines', 12, 3, 12, logs, select(mine, alias='lines')),
        {'name': 'Latest logs', 'x': 0, 'y': 6, 'w': 24, 'h': 6,
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


def sync(runner: Runner, report: Report, wanted_apps: list[App]) -> int:
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
    for name, stale in sorted(live.items()):
        if runner.dry_run:
            report.info(f'would delete {name} (its app is not in Git)')
        else:
            api.call('DELETE', f"/dashboards/{stale['id']}")
            report.ok(f'deleted {name} (its app is not in Git)')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    argparse.ArgumentParser(prog='swhurl clickstack-dashboards', description=__doc__,
                            formatter_class=argparse.RawDescriptionHelpFormatter).parse_args(argv)
    runner = runner or Runner.from_environment()
    return sync(runner, report or Report(redact=runner.redact), apps())
