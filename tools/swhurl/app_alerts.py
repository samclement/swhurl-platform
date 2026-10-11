"""The default ClickStack alerts every app gets, like its dashboard (docs/apps.md#alerts).

Each alert watches one tile of the app's ``App: <app>`` dashboard and calls the notify service's
webhook, which sends it to the channel of the app's team. They are created, repaired and removed by
the same job that keeps the dashboards (dashboards.py): nothing is declared per app.

An alert is named ``swhurl:<app>:<preset>``. The name is how the notify service knows which app an
alert is about, and how this module knows which alerts are its own: any other alert is left alone.
"""
from __future__ import annotations

import re
from dataclasses import dataclass

PREFIX = 'swhurl:'
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


def wanted(app: str, dashboard: dict, webhook: str) -> list[dict]:
    """The alerts ``app`` should have on its live ``dashboard`` (its tiles carry the server's ids)."""
    tiles = {tile.get('name'): tile.get('id') for tile in dashboard.get('tiles') or []}
    return [{'name': alert_name(app, preset), 'source': 'tile', 'dashboardId': dashboard['id'], 'tileId': tiles[preset.tile],
             'threshold': preset.threshold, 'thresholdType': 'above', 'interval': preset.interval,
             'channel': {'type': 'webhook', 'webhookId': webhook}}
            for preset in PRESETS if tiles.get(preset.tile)]


def same(live: dict, want: dict) -> bool:
    return all(live.get(key) == value for key, value in want.items())


def reconcile(api, report, dashboards: dict[str, dict], *, prune: bool, dry_run: bool) -> None:
    """Create each app's missing alerts, replace the ones that differ (a rebuilt dashboard has new tile
    ids) and, with ``prune``, delete the alerts of apps that are gone. ``dashboards`` maps each app
    to its live dashboard."""
    report.section('ClickStack app alerts')
    webhook = next((w.get('id') for w in api.call('GET', '/webhooks').get('data', []) if w.get('name') == WEBHOOK), None)
    if not webhook:
        report.warn(f'no "{WEBHOOK}" webhook in HyperDX, so apps have no default alerts: make clickstack-notify-webhook')
        return
    live: dict[str, list[dict]] = {}
    for alert in api.call('GET', '/alerts').get('data', []):
        if str(alert.get('name', '')).startswith(PREFIX):
            live.setdefault(alert['name'], []).append(alert)
    changed = 0
    for app, dashboard in sorted(dashboards.items()):
        for want in wanted(app, dashboard, webhook):
            found = live.pop(want['name'], [])
            if len(found) == 1 and same(found[0], want):
                continue
            changed += 1
            if dry_run:
                report.info(f"would {'replace' if found else 'create'} {want['name']}")
                continue
            for stale in found:
                api.call('DELETE', f"/alerts/{stale['id']}")
            api.call('POST', '/alerts', want)
            report.ok(f"{'replaced' if found else 'created'} {want['name']}")
    for name, found in sorted(live.items()) if prune else []:
        changed += 1
        if dry_run:
            report.info(f'would delete {name}')
            continue
        for stale in found:
            api.call('DELETE', f"/alerts/{stale['id']}")
        report.ok(f'deleted {name}: its app or tile is gone')
    if not changed:
        report.ok(f'{len(dashboards)} app(s) have their default alerts')
