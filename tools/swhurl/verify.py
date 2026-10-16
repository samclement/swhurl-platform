"""Verify the live platform (``verify-platform``): the registry of checks and what runs them.

``verify-platform`` is read-only. Each check reports ``[OK]``/``[BAD]`` under a
section heading; any ``[BAD]`` makes the exit code 1. Key values are
registered with the runner as secrets, so they are redacted from every message
and never printed, even when a comparison fails.

A check lives beside what it checks (``flux.py``, ``clickstack.py``, ``apps/checks.py``,
``notifications/checks.py``, ``console/checks.py``, the backup modules) and is listed in ``CHECKS``.
"""
from __future__ import annotations

import functools
import io
import sys
from collections.abc import Callable
from dataclasses import dataclass

from swhurl import clickstack, dashboards, flux, recovery, sqlite_backup
from swhurl.apps import checks as apps
from swhurl.console import checks as console
from swhurl.notifications import checks as notifications
from swhurl.report import Report
from swhurl.run import CommandError, Runner, SharedReads


def check_ingress(runner: Runner, report: Report) -> None:
    """Traefik comes with k3s and has no module of its own, so its one check is here."""
    report.section('Ingress')
    try:
        deploy = runner.json(['kubectl', '-n', 'kube-system', 'get', 'deploy', 'traefik', '-o', 'json'])
        args = deploy['spec']['template']['spec']['containers'][0].get('args') or []
    except (CommandError, KeyError, IndexError, TypeError):
        args = []
    if any('entryPoints.web.http.redirections.entryPoint.scheme=https' in a for a in args):
        report.ok('Traefik redirects HTTP to HTTPS')
    else:
        report.bad('Traefik does not redirect HTTP to HTTPS (plain-HTTP sign-in fails with 403); '
                   'check infra/traefik/helmchartconfig.yaml')


@dataclass(frozen=True)
class Check:
    """One group of checks, what it needs beyond reading the cluster, and what passing means.

    ``cluster``: read non-Secret objects; ``secret``: read Secrets; ``exec``: run
    commands in pods; ``host``: this machine's files or AWS credentials. The
    console runs only the checks its read-only account can (``{'cluster'}``).
    ``what`` is the check's row in the operations page (``table``; a test keeps the two the same).
    """
    name: str
    needs: frozenset[str]
    run: Callable[[Runner, Report], None]
    what: str

    @classmethod
    def of(cls, name: str, needs: str, run: Callable[[Runner, Report], None], what: str) -> Check:
        return cls(name, frozenset(needs.split()), run, what)


CHECKS = (
    Check.of('flux', 'cluster', flux.check_units, 'Every Flux unit is Ready'),
    Check.of('flux-controllers', 'cluster host', flux.check_controllers,
             'The Flux controllers run the pinned version with the settings in Git (warns otherwise)'),
    Check.of('ingestion-key', 'cluster secret exec', clickstack.check_ingestion,
             "The collectors' ingestion key is the ClickStack team's, compared by bytes and never printed"),
    Check.of('registration', 'cluster exec', clickstack.check_registration, 'A HyperDX team exists, so registration is closed'),
    Check.of('ingress', 'cluster', check_ingress, 'Traefik redirects HTTP to HTTPS'),
    Check.of('image-automation', 'cluster', apps.check_image_automation,
             "Every app's ImagePolicy is Ready and staging runs the image it picked (warns otherwise)"),
    Check.of('isolation', 'cluster host', apps.check_isolation,  # reads apps/teams.yaml
             'Every app instance in Git has a registered team, the platform quota and a NetworkPolicy on its pods'),
    Check.of('signing-keys', 'cluster host', apps.check_signing_keys,  # reads apps/signing-keys.json
             "The signing keys and token issuer apps were given are the cluster's"),
    Check.of('secrets', 'host', apps.check_secrets,  # reads the instances in Git, never a value
             'Every secret name an app\'s image needs has a value in Git (a missing one warns)'),
    Check.of('events', 'cluster host', apps.check_events,  # reads platform/nats/users.yaml
             'NATS and its controller run, and every declared stream and consumer exists'),
    Check.of('alerts', 'cluster', flux.check_alerts, 'The Flux failure Alert exists and its provider does (a missing Alert warns)'),
    Check.of('channels', 'cluster host', notifications.check_channels,  # reads apps/teams.yaml and platform/notify
             "The team routes are Git's and every channel has its Secret (a team without a channel warns)"),
    Check.of('notify-service', 'cluster', notifications.check_notify_service,
             "The notify service runs, on the console's image (another image warns)"),
    Check.of('retention', 'cluster exec', clickstack.check_retention,
             'Telemetry expires after 30 days and ClickHouse system logs after 7, no merge failed in the last hour, '
             'and the MongoDB volume is retained'),
    Check.of('backups', 'host', recovery.check_backups,
             'The newest MongoDB backup, on this host and in S3, is younger than `BACKUP_MAX_AGE_HOURS` (26)'),
    Check.of('sqlite-backups', 'cluster host', sqlite_backup.check_sqlite_backups,
             "Each app SQLite database's newest backup, on this host and in S3, is as young"),
    Check.of('push-webhook', 'cluster host', flux.check_push_webhook,
             "Flux's GitHub receiver is Ready; GitHub's latest push delivery returned 200 (warns otherwise)"),
    Check.of('image-webhooks', 'cluster host', apps.check_image_webhooks,
             "The image receiver is Ready; each auto-deploy app's repository calls it (warns otherwise)"),
    Check.of('dashboards', 'cluster', dashboards.check_dashboards, 'The dashboard and alert sync succeeded within five minutes'),
    Check.of('notifications', 'cluster', notifications.check_notifications, 'The notification checker succeeded within five minutes'),
    Check.of('notification-heartbeat', 'host', notifications.check_notification_heartbeat,
             "The host heartbeat timer is active and its service's last run succeeded within 15 minutes"),
    Check.of('console', 'cluster host', console.check_console,
             'The console image is built from this checkout\'s tooling (warns otherwise) and its job record exists'),
    Check.of('console-token', 'cluster secret host', console.check_console_token,
             "GitHub accepts the console's tokens and none expires within 14 days (warns)"),
)
NEEDS = frozenset().union(*(check.needs for check in CHECKS))
WORDS = {'cluster': 'Cluster (read)', 'secret': 'Secrets (read)', 'exec': 'commands in pods', 'host': 'this host'}


def table() -> str:
    """The checks as the Markdown table in the operations page, in the order they run."""
    rows = [f"| `{check.name}` | {check.what} | {', '.join(WORDS[need] for need in WORDS if need in check.needs)} |"
            for check in CHECKS]
    return '\n'.join(['| Check | Passes when | Reads |', '| --- | --- | --- |', *rows])


def verify_platform(runner: Runner, report: Report, *, allowed: frozenset[str] = NEEDS) -> int:
    """Run every check whose needs are within ``allowed``; name the skipped ones."""
    report.redact = runner.redact
    try:
        runner.run(['kubectl', 'get', '--raw=/version'])
    except CommandError as error:
        detail = 'kubectl cannot reach a cluster; ensure kubeconfig is set'
        if 'missing required command' in str(error):
            detail = str(error)
        print(f'[ERROR] {detail}', file=sys.stderr)
        return 1
    skipped = [check.name for check in CHECKS if not check.needs <= allowed]
    def alone(check: Check) -> Report:
        part = Report(io.StringIO(), redact=runner.redact)
        check.run(runner, part)
        return part
    wanted = [check for check in CHECKS if check.needs <= allowed]
    if isinstance(runner, SharedReads):  # a console page: the checks at once, reported in their order
        for part in runner.together(*(functools.partial(alone, check) for check in wanted)):
            report.merge(part)
    else:  # a terminal: each line as its check finishes
        for check in wanted:
            check.run(runner, report)
    if skipped:
        report.line()
        report.info(f"skipped (need more than {', '.join(sorted(allowed))}): {', '.join(skipped)}")
    if report.passed:
        report.line('\nValidation passed.')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    return verify_platform(runner or Runner(), report or Report())
