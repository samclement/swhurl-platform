"""Verify the platform: local config (``check-config``) and live state (``verify-platform``).

``verify-platform`` is read-only. Each check reports ``[OK]``/``[BAD]`` under a
section heading; any ``[BAD]`` makes the exit code 1. Key values are
registered with the runner as secrets, so they are redacted from every message
and never printed, even when a comparison fails.
"""
from __future__ import annotations

import base64
import binascii
import sys
from pathlib import Path

from swhurl import ROOT, platform
from swhurl.report import Report
from swhurl.run import CommandError, Runner
from swhurl.settings import SettingsError, load_settings

SYSTEM_LOGS = ('query_log', 'metric_log', 'asynchronous_metric_log', 'crash_log', 'processors_profile_log',
               'part_log', 'trace_log', 'query_thread_log', 'query_views_log', 'opentelemetry_span_log')


def verify_config(argv: list[str] | None = None, root: Path = ROOT) -> int:
    """Local checks that need no cluster.

    Every Flux unit that decrypts SOPS has at least one encrypted Secret in its
    path (derived from the unit definitions, not a hand-kept list), and the
    settings the platform substitutes exist.
    """
    for unit, files in platform.decrypted_secret_files(root).items():
        if not files:
            print(f'{unit} decrypts SOPS but its path has no *.sops.yaml Secret')
            return 1
    try:
        values = load_settings((root / platform.SETTINGS).read_text())
    except (OSError, SettingsError) as error:
        print(f'platform-settings unreadable: {error}')
        return 1
    for key in ('BASE_DOMAIN', 'CERT_ISSUER'):
        if not values.get(key):
            print(f'{key} missing from platform-settings')
            return 1
    return 0


def ready_condition(obj: dict) -> tuple[str, str]:
    for condition in (obj.get('status') or {}).get('conditions') or []:
        if condition.get('type') == 'Ready':
            return condition.get('status', 'Unknown'), condition.get('message', '')
    return 'Unknown', 'no Ready condition'


def check_flux(runner: Runner, report: Report) -> None:
    report.section('Flux Kustomizations')
    try:
        units = runner.json(['kubectl', '-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io',
                             '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad('could not read Flux kustomizations')
        return
    if not units:
        report.bad('no Flux kustomizations found in flux-system')
        return
    for unit in sorted(units, key=lambda u: u['metadata']['name']):
        name = unit['metadata']['name']
        status, message = ready_condition(unit)
        if status == 'True':
            report.ok(name)
        else:
            report.bad(f'{name} is not Ready' + (f': {message}' if message else ''))


def read_ingestion_secret(runner: Runner) -> str:
    """The Secret's ``data`` value: base64 text, exactly as Kubernetes stores it."""
    try:
        secret = runner.json(['kubectl', '-n', 'logging', 'get', 'secret', 'hyperdx-secret', '-o', 'json'],
                             secret_output=True)
    except CommandError:
        return ''
    value = ((secret or {}).get('data') or {}).get('HYPERDX_API_KEY', '')
    runner.add_secret(value)
    return value


def check_runtime_secret(stored: str, report: Report) -> None:
    report.section('Runtime Secrets')
    if stored:
        report.ok('logging/hyperdx-secret.HYPERDX_API_KEY present')
    else:
        report.bad('logging/hyperdx-secret.HYPERDX_API_KEY is empty (run: make runtime-inputs-refresh-otel)')


def check_ingestion_key(runner: Runner, report: Report, stored: str) -> None:
    """The collectors' key must be exactly one base64 layer around the ClickStack team key."""
    report.section('Ingestion Key Sync')
    try:
        team_key = runner.output(['kubectl', '-n', 'observability', 'exec', 'deploy/clickstack-mongodb', '--',
                                  'mongosh', 'hyperdx', '--quiet', '--eval', platform.TEAM_KEY_SCRIPT],
                                 secret_output=True).rstrip('\n')
    except CommandError:
        team_key = ''
    runner.add_secret(team_key)
    if not team_key:
        report.bad('cannot read a unique ClickStack team ingestion key; check MongoDB availability and team configuration')
        return
    try:
        decoded = base64.b64decode(stored, validate=True)
    except (binascii.Error, ValueError):
        decoded = b''
    runner.add_secret(decoded)
    if not decoded:
        report.bad('logging/hyperdx-secret.HYPERDX_API_KEY is empty or invalid base64')
    elif decoded == team_key.encode() and stored == base64.b64encode(team_key.encode()).decode():
        report.ok('ingestion Secret bytes match ClickStack; verify collector logs and fresh telemetry after a restart')
    else:
        report.bad('HYPERDX_API_KEY does not match the ClickStack ingestion key')
        report.detail('Fix: update HYPERDX_API_KEY in platform-services/otel/base/secret-hyperdx.sops.yaml')
        report.detail('     using exactly one base64 layer in data; commit+push, then run: make runtime-inputs-refresh-otel')


def check_ingress(runner: Runner, report: Report) -> None:
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
                   'check helmchartconfig-traefik.yaml')


def clickhouse(runner: Runner, query: str) -> str:
    return runner.output(['kubectl', '-n', 'observability', 'exec', 'deploy/clickstack-clickhouse', '--',
                          'clickhouse-client', '-q', query]).strip()


def check_retention(runner: Runner, report: Report) -> None:
    report.section('Retention')
    try:
        with_ttl, total = map(int, clickhouse(runner, (
            "SELECT countIf(position(engine_full, 'toIntervalDay(30)') > 0), count() FROM system.tables "
            "WHERE database = 'default' AND engine LIKE '%MergeTree' FORMAT TSV")).split())
    except (CommandError, ValueError):
        report.bad('could not read ClickHouse telemetry table TTLs')
    else:
        if total > 0 and with_ttl == total:
            report.ok(f'all {total} telemetry tables expire after 30 days')
        else:
            report.bad(f'telemetry tables without a 30-day TTL ({with_ttl} of {total} have it); '
                       'the collector image default may have changed')

    names = "', '".join(SYSTEM_LOGS)
    try:
        untimed = int(clickhouse(runner, (
            f"SELECT count() FROM system.tables WHERE database = 'system' AND name IN ('{names}') "
            "AND position(engine_full, 'TTL ') = 0 FORMAT TSV")))
    except (CommandError, ValueError):
        report.bad('could not read ClickHouse system log TTLs')
    else:
        if untimed == 0:
            report.ok('ClickHouse system logs expire after 7 days')
        else:
            report.bad(f'{untimed} ClickHouse system log table(s) have no TTL; '
                       'restart clickstack-clickhouse after config changes')

    try:
        pvc = runner.json(['kubectl', '-n', 'observability', 'get', 'pvc', 'clickstack-mongodb', '-o', 'json'])
    except CommandError:
        pvc = {}
    volume = (pvc.get('spec') or {}).get('volumeName', '')
    policy = ''
    if volume:
        try:
            policy = runner.json(['kubectl', 'get', 'pv', volume, '-o', 'json'])['spec'].get(
                'persistentVolumeReclaimPolicy', '')
        except (CommandError, KeyError, TypeError):
            policy = ''
    if policy == 'Retain':
        report.ok('ClickStack MongoDB PV reclaim policy is Retain')
    else:
        report.bad('ClickStack MongoDB PV is not Retain; see docs/operations.md#backups-and-recovery')
    if ((pvc.get('metadata') or {}).get('annotations') or {}).get('helm.sh/resource-policy') == 'keep':
        report.ok('ClickStack MongoDB PVC survives Helm uninstall')
    else:
        report.bad('ClickStack MongoDB PVC lacks helm.sh/resource-policy=keep')


def verify_platform(runner: Runner, report: Report) -> int:
    report.redact = runner.redact
    try:
        runner.run(['kubectl', 'get', '--raw=/version'])
    except CommandError as error:
        detail = 'kubectl cannot reach a cluster; ensure kubeconfig is set'
        if 'missing required command' in str(error):
            detail = str(error)
        print(f'[ERROR] {detail}', file=sys.stderr)
        return 1
    check_flux(runner, report)
    stored = read_ingestion_secret(runner)
    check_runtime_secret(stored, report)
    check_ingestion_key(runner, report, stored)
    check_ingress(runner, report)
    check_retention(runner, report)
    if report.passed:
        report.line('\nValidation passed.')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    return verify_platform(runner or Runner(), report or Report())
