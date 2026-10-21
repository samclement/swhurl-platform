"""The default ClickStack alerts every app gets, like its dashboard (docs/apps.md#alerts).

Each alert watches one tile of the app's ``App: <app>`` dashboard and calls the notify service's
webhook, which sends it to the channel of the app's team. They are created, repaired and removed by
the same job that keeps the dashboards (dashboards.py). Nothing has to be declared per app; an app
that wants other thresholds, or none, says so in ``interface.ALERTS_ANNOTATION`` on its namespaces (make app-alerts).

An alert is named ``swhurl:<app>:<preset>``. The name is how the notify service knows which app an
alert is about, and how this module knows which alerts are its own: any other alert is left alone.
"""
from __future__ import annotations

import re
from dataclasses import dataclass

PREFIX = 'swhurl:'
TAG = 'swhurl-app'
"""On every app dashboard and alert the platform keeps; an alert also carries its app's name as a tag,
so the HyperDX alerts page filters to one app."""
OFF = 'none'  # not "off": YAML reads that as false, and an annotation or a swhurl.yaml field would stop being text
MAX_THRESHOLD = 1_000_000
NAME = re.compile(r'swhurl:(?P<app>[a-z][a-z0-9-]*):(?P<preset>[a-z][a-z0-9-]*)')
WEBHOOK = 'swhurl notify'
"""The HyperDX webhook that reaches the notify service (make clickstack-notify-webhook creates it)."""


@dataclass(frozen=True)
class Preset:
    key: str
    tile: str  # the dashboard tile it watches, by name (dashboards.dashboard)
    label: str  # how a notification names it: "<app>/<env> alert: <label>"
    threshold: int  # fires when the tile's value in one interval meets or exceeds this
    interval: str


PRESETS = (
    Preset('error-logs', 'Error logs', 'error logs', 5, '5m'),
    Preset('errors', 'Errors', 'failed requests or runs', 5, '5m'),
)
LABELS = {preset.key: preset.label for preset in PRESETS}


def alert_name(app: str, preset: Preset) -> str:
    return f'{PREFIX}{app}:{preset.key}'


def thresholds(spec: str) -> dict[str, int | None]:
    """Each preset's threshold under an app's settings; None for one that is switched off.

    Raises ValueError naming what is wrong: the commands and the app policy show it. The job that
    keeps the alerts falls back to the defaults instead (``lenient``), so a bad value on the cluster
    never leaves an app without alerts."""
    found: dict[str, int | None] = {preset.key: preset.threshold for preset in PRESETS}
    spec = (spec or '').strip()
    if spec == OFF:
        return dict.fromkeys(found)
    for part in [p.strip() for p in spec.split(',') if p.strip()]:
        key, _, value = part.partition('=')
        if key not in found:
            raise ValueError(f'{key!r} is not an alert: choose from {", ".join(found)}')
        if value == OFF:
            found[key] = None
        elif value.isdigit() and 1 <= int(value) <= MAX_THRESHOLD:
            found[key] = int(value)
        else:
            raise ValueError(f'{key}={value!r}: give a number from 1 to {MAX_THRESHOLD}, or {OFF}')
    return found


def lenient(spec: str) -> dict[str, int | None]:
    try:
        return thresholds(spec)
    except ValueError:
        return thresholds('')


def spec_of(settings: dict[str, int | None]) -> str:
    """The shortest setting that says ``settings``: '' for the defaults, ``none`` for no alert."""
    if all(value is None for value in settings.values()):
        return OFF
    return ','.join(f'{preset.key}={OFF if settings[preset.key] is None else settings[preset.key]}'
                    for preset in PRESETS if settings[preset.key] != preset.threshold)


def wanted(app: str, dashboard: dict, webhook: str, spec: str = '') -> list[dict]:
    """The alerts ``app`` should have on its live ``dashboard`` (its tiles carry the server's ids)."""
    tiles = {tile.get('name'): tile.get('id') for tile in dashboard.get('tiles') or []}
    settings = lenient(spec)
    return [{'name': alert_name(app, preset), 'tags': [TAG, app], 'source': 'tile', 'dashboardId': dashboard['id'],
             'tileId': tiles[preset.tile], 'threshold': settings[preset.key], 'thresholdType': 'above',
             'interval': preset.interval, 'channel': {'type': 'webhook', 'webhookId': webhook}}
            for preset in PRESETS if tiles.get(preset.tile) and settings[preset.key] is not None]


def same(live: dict, want: dict) -> bool:
    return all(live.get(key) == value for key, value in want.items())


def reconcile(api, report, dashboards: dict[str, dict], *, prune: bool, dry_run: bool,
              specs: dict[str, str] | None = None) -> None:
    """Create each app's missing alerts, update the ones that differ in place (a rebuilt dashboard has
    new tile ids, an app changed its thresholds), and, with ``prune``, delete the alerts of apps that
    are gone or switched theirs off. ``dashboards`` maps each app to its live dashboard, ``specs`` to its settings."""
    report.section('ClickStack app alerts')
    webhook = next((w.get('id') for w in api.call('GET', '/webhooks').get('data', []) if w.get('name') == WEBHOOK), None)
    if not webhook:
        # A failure, so the job's check (make verify-platform, the console) shows it: without the webhook no app has alerts.
        report.bad(f'no "{WEBHOOK}" webhook in HyperDX, so apps have no default alerts: make clickstack-notify-webhook')
        return
    live: dict[str, list[dict]] = {}
    for alert in api.call('GET', '/alerts').get('data', []):
        if str(alert.get('name', '')).startswith(PREFIX):
            live.setdefault(alert['name'], []).append(alert)
    changed = 0
    for app, dashboard in sorted(dashboards.items()):
        for want in wanted(app, dashboard, webhook, (specs or {}).get(app, '')):
            found = live.pop(want['name'], [])
            if len(found) == 1 and same(found[0], want):
                continue
            changed += 1
            if dry_run:
                report.info(f"would {'update' if found else 'create'} {want['name']}")
                continue
            for extra in found[1:]:  # two with one name: keep the first
                api.call('DELETE', f"/alerts/{extra['id']}")
            if found:
                api.call('PUT', f"/alerts/{found[0]['id']}", want)
            else:
                api.call('POST', '/alerts', want)
            report.ok(f"{'updated' if found else 'created'} {want['name']}")
    for name, found in sorted(live.items()) if prune else []:
        changed += 1
        if dry_run:
            report.info(f'would delete {name}')
            continue
        for stale in found:
            api.call('DELETE', f"/alerts/{stale['id']}")
        report.ok(f'deleted {name}: its app or tile is gone, or the app switched it off')
    if not changed:
        report.ok(f'{len(dashboards)} app(s) have their default alerts')
