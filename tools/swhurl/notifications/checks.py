"""Live checks of notifications: the channels, the notify service, the checker and the host heartbeat.

Each takes a Runner and a Report and only reads; ``verify.CHECKS`` lists them (make verify-platform).
"""
from __future__ import annotations

import datetime as dt
import json
import time
from pathlib import Path

from swhurl import ROOT
from swhurl.apps import channels
from swhurl.report import Report
from swhurl.run import CommandError, Runner


def check_channels(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """The team routes in the cluster are Git's and every channel has its Secret (names only, never an address)."""
    report.section('Notification channels')
    wanted = channels.routes(root)
    if wanted is None:
        return
    get = ['kubectl', '-n', channels.NAMESPACE, 'get']
    try:
        configmaps = runner.json([*get, 'configmaps', '-o', 'json'])['items']
        secrets = runner.output([*get, 'secrets', '-o', 'name']).split()
        live = next((json.loads(c['data'][channels.ROUTES_KEY]) for c in configmaps
                     if c['metadata']['name'] == channels.ROUTES_NAME), None)
    except (CommandError, KeyError, TypeError, ValueError):
        report.bad('could not read the notification routes and channels (make reconcile UNIT=platform-notify)')
        return
    routes = wanted.get('teams') or {}
    if live != wanted:
        report.bad(f'the team routes in the cluster are not those of {channels.ROUTES_FILE}: a team or channel change '
                   'rewrites them, so commit, push and make flux-reconcile')
    else:
        report.ok(f'{len(routes)} team(s) notify a channel, as in Git')
    known = wanted.get('channels') or {}
    missing = [name for name in known if f'secret/{channels.SECRET_PREFIX}{name}' not in secrets]
    if missing:
        report.bad(f'channel {", ".join(missing)} has no Secret in the cluster: its teams get no notifications '
                   '(make reconcile UNIT=platform-notify)')
    elif known:
        report.ok(f'{len(known)} channel(s) have their Secret')
    owned: dict[str, set[str]] = {}  # each team's apps, from v2's app records
    for record in sorted((root / 'v2-records').glob('*/app.json')):
        try:
            name, team = record.parent.name, json.loads(record.read_text()).get('team')
        except (OSError, ValueError, KeyError, TypeError):
            continue  # an app record that cannot be read fails check-apps; here it adds no warning
        owned.setdefault(team, set()).add(name)
    for team, apps in sorted(owned.items()):
        if team and not routes.get(team):
            report.warn(f'team {team} owns {len(apps)} app(s) and has no notification channel: their alerts and messages '
                        f'reach nobody (make team-channel NAME={team})')


def check_notification_heartbeat(runner: Runner, report: Report,
                                 now: dt.datetime | None = None) -> None:
    """The host timer is active and its last heartbeat service run succeeded within 15 minutes."""
    report.section('Notification heartbeat')
    now = now or dt.datetime.now(dt.UTC)
    timer = 'swhurl-notification-heartbeat.timer'
    service = 'swhurl-notification-heartbeat.service'
    try:
        active = runner.output(['systemctl', 'is-active', timer], check=False).strip()
        details = runner.output(['systemctl', 'show', service, '--property=Result', '--property=ExecMainStatus',
                                 '--property=ExecMainExitTimestamp'])
    except CommandError:
        report.bad(f'cannot read the host notification heartbeat (systemctl status {timer})')
        return
    if active != 'active':
        report.bad(f'{timer} is not active, so nothing on this host watches the notification checker: '
                   'make host-heartbeat in your own terminal')
    props = dict(line.split('=', 1) for line in details.splitlines() if '=' in line)
    if props.get('Result') != 'success' or props.get('ExecMainStatus') != '0':
        report.bad(f'{service} last failed (journalctl -u {service}; '
                   'tail /var/log/swhurl-platform/swhurl-notification-heartbeat.log)')
        return
    raw_timestamp = props.get('ExecMainExitTimestamp', '')
    try:
        # systemctl renders this timestamp in the host's local timezone and abbreviation.
        parsed = time.strptime(raw_timestamp, '%a %Y-%m-%d %H:%M:%S %Z')
        taken = time.mktime(parsed)
    except (TypeError, ValueError, OverflowError):
        report.bad(f'{service} has no readable time of its last success (systemctl show {service})')
        return
    age = now.timestamp() - taken
    if age < -60 or age > 15 * 60:
        report.bad(f'{service} last succeeded {age / 60:.1f} minutes ago (systemctl status {timer}; '
                   'tail /var/log/swhurl-platform/swhurl-notification-heartbeat.log)')
    elif active == 'active':
        report.ok('host notification heartbeat timer is active and its service succeeded within 15 minutes')
