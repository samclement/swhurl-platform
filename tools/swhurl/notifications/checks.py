"""Live checks of notifications that Go does not run yet: the host heartbeat.

The channels, the notify service and the checker are ``swhurl-v2 verify``'s. Each takes a Runner and a Report and
only reads; ``verify.CHECKS`` lists them (make verify-platform).
"""
from __future__ import annotations

import datetime as dt
import time

from swhurl.report import Report
from swhurl.run import CommandError, Runner


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
