"""Verify the live platform (``verify-platform``): the registry of checks and what runs them.

``verify-platform`` is read-only. Each check reports ``[OK]``/``[BAD]`` under a
section heading; any ``[BAD]`` makes the exit code 1. Key values are
registered with the runner as secrets, so they are redacted from every message
and never printed, even when a comparison fails.

The checks that read the cluster, this checkout, the registry, S3 and GitHub are ``swhurl-v2 verify --scope platform``'s,
run first (``CLUSTER_CHECKS``); the make recipe builds the binary and names it, the caller's kubeconfig and this
checkout's console content tag in ``SWHURL_V2``, ``SWHURL_V2_KUBECONFIG`` and ``SWHURL_V2_CONSOLE_TAG``, and exports
the AWS credentials it lists S3 with and GH_TOKEN for hook listings. The five others run commands in pods or
need this host, live beside what they check (``flux.py``, ``clickstack.py``, ``notifications/checks.py``) and are
listed in ``CHECKS``.
"""
from __future__ import annotations

import os
import sys
from collections.abc import Callable
from dataclasses import dataclass

from swhurl import ROOT, clickstack, flux, platform_checks
from swhurl.notifications import checks as notifications
from swhurl.report import Report
from swhurl.run import CommandError, Runner

CLUSTER_CHECKS = (
    ('flux-units', 'cluster', 'Every Flux unit is Ready'),
    ('ingress', 'cluster', 'Traefik redirects HTTP to HTTPS'),
    ('alerts', 'cluster', 'The Flux failure Alert exists and its provider does (a missing Alert warns)'),
    ('notify-service', 'cluster', "The notify service runs, on the console's image (another image warns)"),
    ('dashboards', 'cluster', 'The platform webhook, dashboard and alert sync succeeded within five minutes'),
    ('notifications', 'cluster', 'The notification checker succeeded within five minutes'),
    ('image-automation', 'cluster checkout registry',
     "Every app's ImagePolicy is Ready and staging runs the image its record states (warns otherwise)"),
    ('isolation', 'cluster checkout',
     'Every instance v2 compiled has its namespace, a registered team, the platform quota and a NetworkPolicy'),
    ('signing-keys', 'cluster checkout', "The signing keys and token issuer apps were given are the cluster's"),
    ('events', 'cluster checkout', 'NATS and its controller run, and every stream and consumer v2 rendered is Ready'),
    ('egress', 'cluster checkout', 'The egress proxy runs both replicas, with a port of its own for every instance v2 compiled'),
    ('channels', 'cluster checkout secret',
     "The team routes are Git's and every channel has its Secret (a team without a channel warns)"),
    ('backups', 'cluster s3',
     'The newest MongoDB backup in S3, archive and metadata, is younger than `BACKUP_MAX_AGE_HOURS` (26)'),
    ('sqlite-backups', 'checkout s3', 'Each instance whose release declares a SQLite database has a backup in S3 as young'),
    ('console', 'cluster checkout',
     "The console image is built from this checkout's tooling (warns otherwise) and its job record exists"),
    ('push-webhook', 'cluster checkout github',
     "Flux's GitHub receiver is Ready; GitHub's latest push delivery returned 200 (warns otherwise)"),
    ('image-webhooks', 'cluster checkout github',
     "The image receiver is Ready; each auto-deploy app's repository calls it (warns otherwise)"),
    ('console-token', 'cluster checkout secret github',
     "GitHub accepts the console's tokens and none expires within 14 days (warns)"),
)
"""``swhurl-v2 verify --scope platform``'s checks, in its order, for the operations page: Go decides them
(``v2/observe/platform.go`` and ``platform_scope.go``). Each names what it reads besides the cluster and its passing."""
READS = {'cluster': 'Cluster (read)', 'checkout': 'this checkout', 'registry': 'registry (read)', 's3': 'S3 (list)',
         'secret': 'Secrets (read)', 'github': 'GitHub (read)'}


def check_cluster(runner: Runner, report: Report) -> None:
    """Show what ``swhurl-v2 verify --scope platform`` reported. A run that gave no complete report is a failed check,
    not a pass. Without ``SWHURL_V2_CONSOLE_TAG`` no tag is passed, and Go reports it was not supplied."""
    binary, kubeconfig = os.environ.get('SWHURL_V2', ''), os.environ.get('SWHURL_V2_KUBECONFIG', '')
    if not binary or not kubeconfig:
        report.section('Cluster checks')
        report.bad('swhurl-v2 verify did not run: SWHURL_V2 and SWHURL_V2_KUBECONFIG name no binary and kubeconfig '
                   '(run make verify-platform, which builds both)')
        return
    tag = os.environ.get('SWHURL_V2_CONSOLE_TAG', '')
    command = [binary, 'verify', '--scope', 'platform', '--kubeconfig', kubeconfig, '--root', str(ROOT),
               *(['--console-tag', tag] if tag else []), '--source', 'verify-platform', '--json']
    try:
        result = runner.run(command, check=False)
    except CommandError as error:
        report.section('Cluster checks')
        report.bad(f'swhurl-v2 verify did not run: {error}')
        return
    try:
        found = platform_checks.entries(result.stdout)
    except ValueError as error:
        report.section('Cluster checks')
        report.bad(f'swhurl-v2 verify gave no report ({error}): {(result.stderr or "").strip() or f"exit {result.returncode}"}')
        return
    section = None
    for entry in found:
        if entry.section != section:
            section = entry.section
            report.section(section)
        getattr(report, entry.level)(entry.message)


@dataclass(frozen=True)
class Check:
    """One group of checks, what it needs beyond reading the cluster, and what passing means.

    ``cluster``: read non-Secret objects; ``secret``: read Secrets; ``exec``: run
    commands in pods; ``host``: this machine's files or services, or GitHub.
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
    Check.of('flux-controllers', 'cluster host', flux.check_controllers,
             'The Flux controllers run the pinned version with the settings in Git (warns otherwise)'),
    Check.of('ingestion-key', 'cluster secret exec', clickstack.check_ingestion,
             "The collectors' ingestion key is the ClickStack team's, compared by bytes and never printed"),
    Check.of('registration', 'cluster exec', clickstack.check_registration, 'A HyperDX team exists, so registration is closed'),
    Check.of('retention', 'cluster exec', clickstack.check_retention,
             'Telemetry expires after 30 days and ClickHouse system logs after 7, no merge failed in the last hour, '
             'and the MongoDB volume is retained'),
    Check.of('notification-heartbeat', 'host', notifications.check_notification_heartbeat,
             "The host heartbeat timer is active and its service's last run succeeded within 15 minutes"),

)
WORDS = {'cluster': 'Cluster (read)', 'secret': 'Secrets (read)', 'exec': 'commands in pods', 'host': 'this host'}


def table() -> str:
    """The checks as the Markdown table in the operations page, in the order they run."""
    rows = [f"| `{name}` | {what} | {', '.join(READS[read] for read in READS if read in reads.split())}, `swhurl-v2 verify` |"
            for name, reads, what in CLUSTER_CHECKS]
    rows += [f"| `{check.name}` | {check.what} | {', '.join(WORDS[need] for need in WORDS if need in check.needs)} |"
             for check in CHECKS]
    return '\n'.join(['| Check | Passes when | Reads |', '| --- | --- | --- |', *rows])


def verify_platform(runner: Runner, report: Report) -> int:
    """Run ``swhurl-v2 verify``'s checks, then every other one, each line as its check finishes."""
    report.redact = runner.redact
    try:
        runner.run(['kubectl', 'get', '--raw=/version'])
    except CommandError as error:
        detail = 'kubectl cannot reach a cluster; ensure kubeconfig is set'
        if 'missing required command' in str(error):
            detail = str(error)
        print(f'[ERROR] {detail}', file=sys.stderr)
        return 1
    check_cluster(runner, report)
    for check in CHECKS:
        check.run(runner, report)
    if report.passed:
        report.line('\nValidation passed.')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    return verify_platform(runner or Runner(), report or Report())
