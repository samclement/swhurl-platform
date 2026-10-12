"""Live checks of notifications: the channels, the notify service, the checker and the host heartbeat.

Each takes a Runner and a Report and only reads; ``verify.CHECKS`` lists them (make verify-platform).
"""
from __future__ import annotations

import datetime as dt
import json
import time
from pathlib import Path

import yaml

from swhurl import ROOT, kube
from swhurl.apps import channels, contract, helmrelease
from swhurl.apps.instance import INSTANCE_ROOTS, instances
from swhurl.report import Report
from swhurl.run import CommandError, Runner

from .heartbeat import CRONJOB_NAME, CRONJOB_NAMESPACE


def check_channels(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """The team routes in the cluster are Git's and every channel has its Secret (names only, never an address)."""
    report.section('Notification channels')
    wanted = channels.routes_file(root)
    if not wanted:
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
    routes = channels.team_channels(root)
    if live != channels.routes(root):
        report.bad('the team routes in the cluster are not those of apps/teams.yaml: make app-sync, commit, push '
                   'and make flux-reconcile')
    else:
        report.ok(f'{len(routes)} team(s) notify a channel, as in Git')
    known = channels.channels(root)
    missing = [name for name in known if f'secret/{channels.SECRET_PREFIX}{name}' not in secrets]
    if missing:
        report.bad(f'channel {", ".join(missing)} has no Secret in the cluster: its teams get no notifications '
                   '(make reconcile UNIT=platform-notify)')
    elif known:
        report.ok(f'{len(known)} channel(s) have their Secret')
    owned: dict[str, set[str]] = {}  # each team's apps, from the instances in Git
    for instance in instances(root / INSTANCE_ROOTS[0]):
        path = instance.folder(root) / 'namespace.yaml'
        labels = ((yaml.safe_load(path.read_text()) or {}).get('metadata') or {}).get('labels') or {} if path.is_file() else {}
        owned.setdefault(str(labels.get(contract.TEAM, '')), set()).add(instance.app)
    for team, apps in sorted(owned.items()):
        if team and not routes.get(team):
            report.warn(f'team {team} owns {len(apps)} app(s) and has no notification channel: their alerts and messages '
                        f'reach nobody (make team-channel NAME={team})')


def check_notify_service(runner: Runner, report: Report) -> None:
    """The notify service is running, on the image the console runs (one pin moves both)."""
    report.section('Notify service')
    try:
        deployment = runner.json(['kubectl', '-n', channels.NAMESPACE, 'get', 'deployment', 'notify', '-o', 'json'])
        running = deployment['spec']['template']['spec']['containers'][0]['image']
        ready = (deployment.get('status') or {}).get('readyReplicas', 0)
        console = helmrelease.image(runner.json(['kubectl', '-n', 'console', 'get', 'helmrelease', 'console', '-o', 'json']))
    except (CommandError, KeyError, TypeError, IndexError):
        report.bad('cannot read the notify service (notify/notify): teams get no copies of their apps\' messages '
                   '(make reconcile UNIT=platform-notify)')
        return
    if not ready:
        report.bad('the notify service is not running: teams get no copies of their apps\' messages '
                   '(kubectl -n notify get pods; make reconcile UNIT=platform-notify)')
    elif f":{console.get('tag')}" not in running:
        report.warn(f"the notify service runs {running.split('@')[0].rsplit(':', 1)[-1]}, not the console's "
                    f"{console.get('tag')}: make console-image pins both, then commit and push")
    else:
        report.ok(f"the notify service is running {console.get('tag')}, as the console")


def check_notifications(runner: Runner, report: Report, now: dt.datetime | None = None) -> None:
    """A recent successful check proves the snapshot/outbox pass finished, not subscriber delivery."""
    report.section('Notifications')
    try:
        state = kube.cronjob_run(runner, CRONJOB_NAMESPACE, CRONJOB_NAME, now)
    except (CommandError, KeyError, ValueError, TypeError):
        report.bad(f'cannot read notification checker status ({CRONJOB_NAMESPACE}/{CRONJOB_NAME})')
        return
    if state == 'suspended':
        report.bad('notification checker is suspended')
    elif state == 'stale':
        report.bad(f'notification checker has no success in the last 5 minutes; check {CRONJOB_NAME} job logs')
    else:
        report.ok('notification checker succeeded in the last 5 minutes')


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
        report.bad('cannot read host notification heartbeat timer/service status')
        return
    if active != 'active':
        report.bad(f'{timer} is not active')
    props = dict(line.split('=', 1) for line in details.splitlines() if '=' in line)
    if props.get('Result') != 'success' or props.get('ExecMainStatus') != '0':
        report.bad(f'{service} has not completed successfully')
        return
    raw_timestamp = props.get('ExecMainExitTimestamp', '')
    try:
        # systemctl renders this timestamp in the host's local timezone and abbreviation.
        parsed = time.strptime(raw_timestamp, '%a %Y-%m-%d %H:%M:%S %Z')
        taken = time.mktime(parsed)
    except (TypeError, ValueError, OverflowError):
        report.bad(f'{service} has an invalid successful run timestamp')
        return
    age = now.timestamp() - taken
    if age < -60 or age > 15 * 60:
        report.bad(f'{service} last succeeded {age / 60:.1f} minutes ago; check the host timer and log')
    elif active == 'active':
        report.ok('host notification heartbeat timer is active and its service succeeded within 15 minutes')
