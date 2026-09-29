"""Verify the platform: local config (``check-config``) and live state (``verify-platform``).

``verify-platform`` is read-only. Each check reports ``[OK]``/``[BAD]`` under a
section heading; any ``[BAD]`` makes the exit code 1. Key values are
registered with the runner as secrets, so they are redacted from every message
and never printed, even when a comparison fails.
"""
from __future__ import annotations

import base64
import binascii
import datetime as dt
import os
import sys
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path

from swhurl import ROOT, clickstack, platform, recovery
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
        secret = runner.json(['kubectl', '-n', 'logging', 'get', 'secret', platform.INGESTION_SECRET_NAME, '-o', 'json'],
                             secret_output=True)
    except CommandError:
        return ''
    value = ((secret or {}).get('data') or {}).get(platform.INGESTION_KEY, '')
    runner.add_secret(value)
    return value


def check_runtime_secret(stored: str, report: Report) -> None:
    report.section('Runtime Secrets')
    if stored:
        report.ok(f'logging/{platform.INGESTION_SECRET_NAME}.{platform.INGESTION_KEY} present')
    else:
        report.bad(f'logging/{platform.INGESTION_SECRET_NAME}.{platform.INGESTION_KEY} is empty (run: make reconcile UNIT=platform-otel)')


def check_ingestion_key(runner: Runner, report: Report, stored: str) -> None:
    """The collectors' key must be exactly one base64 layer around the ClickStack team key."""
    report.section('Ingestion Key Sync')
    try:
        team_key = clickstack.team_key(runner)
    except (CommandError, ValueError, KeyError):
        team_key = ''
    if not team_key:
        report.bad('cannot read a unique ClickStack team ingestion key; is MongoDB Running and '
                   'make clickstack-bootstrap done?')
        return
    try:
        decoded = base64.b64decode(stored, validate=True)
    except (binascii.Error, ValueError):
        decoded = b''
    runner.add_secret(decoded)
    if not decoded:
        report.bad(f'logging/{platform.INGESTION_SECRET_NAME}.{platform.INGESTION_KEY} is empty or invalid base64')
    elif decoded == team_key.encode() and stored == base64.b64encode(team_key.encode()).decode():
        report.ok('ingestion Secret bytes match ClickStack; verify collector logs and fresh telemetry after a restart')
    else:
        report.bad(f'{platform.INGESTION_KEY} does not match the ClickStack team ingestion key')
        report.detail('Fix: run make clickstack-bootstrap (writes the Git key into the team); if the two')
        report.detail('     SOPS copies differ, make check-secrets says so')


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
                   'check infra/traefik/helmchartconfig.yaml')


def clickhouse(runner: Runner, query: str) -> str:
    return runner.output(['kubectl', '-n', 'observability', 'exec', clickstack.CLICKHOUSE_POD, '--',
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
                       'the chart sets them in clickhouse.cluster.spec.settings.extraConfig')

    try:
        pvc = runner.json(['kubectl', '-n', 'observability', 'get', 'pvc', clickstack.MONGO_DATA_CLAIM, '-o', 'json'])
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
        report.ok('ClickStack MongoDB data volume is Retain (outlives its claim)')
    else:
        report.bad('ClickStack MongoDB data volume is not Retain; its storage class must be local-path-retain')


def check_registration(runner: Runner, report: Report) -> None:
    report.section('ClickStack Sign-up')
    try:
        closed = clickstack.api(runner, 'GET', '/installation')['body'].get('isTeamExisting')
    except (CommandError, ValueError, KeyError, AttributeError):
        report.bad('could not ask the HyperDX API whether a team exists')
        return
    if closed:
        report.ok('a team exists, so HyperDX registration is closed')
    else:
        report.bad('no team yet: HyperDX registration is open (run: make clickstack-bootstrap)')

def check_backups(runner: Runner, report: Report, env: Mapping[str, str] | None = None,
                  now: dt.datetime | None = None) -> None:
    """The newest MongoDB backup, locally and off-host, must be younger than BACKUP_MAX_AGE_HOURS (26)."""
    env = os.environ if env is None else env
    report.section('Backups')
    now = now or dt.datetime.now(dt.UTC)
    limit = dt.timedelta(hours=float(env.get('BACKUP_MAX_AGE_HOURS') or 26))
    backup_dir = Path(env.get('BACKUP_DIR') or recovery.DEFAULT_BACKUP_DIR)
    places = [(str(backup_dir), lambda: [p.name for p in backup_dir.iterdir()] if backup_dir.is_dir() else [])]
    uri = env.get('BACKUP_S3_URI', platform.BACKUP_S3_URI)
    if uri:
        places.append((uri, lambda: recovery.remote_names(runner, uri)))
    for where, names in places:
        try:
            taken = recovery.newest(names())
        except (CommandError, recovery.RecoveryError, TypeError) as error:
            report.bad(f'cannot list backups in {where}: {error}')
            continue
        if taken is None:
            report.bad(f'no MongoDB backup in {where} (run: make backup-mongodb)')
        elif now - taken > limit:
            report.bad(f'newest MongoDB backup in {where} is {(now - taken).total_seconds() / 3600:.0f} h old; '
                       'check: systemctl status swhurl-backup-mongodb')
        else:
            report.ok(f'newest MongoDB backup in {where} is {(now - taken).total_seconds() / 3600:.1f} h old')


def check_console(runner: Runner, report: Report) -> None:
    """The console image's tag is the commit it was built from; warn if its inputs changed since then."""
    report.section('Console')
    try:
        release = runner.json(['kubectl', '-n', 'console', 'get', 'helmrelease', 'console', '-o', 'json'])
        tag = release['spec']['values']['controllers']['main']['containers']['main']['image']['tag']
    except (CommandError, KeyError, TypeError):
        report.bad('cannot read the console HelmRelease image tag (console/console)')
        return
    changed = runner.run(['git', '-C', str(ROOT), 'diff', '--quiet', tag, 'HEAD', '--',
                          *platform.CONSOLE_IMAGE_INPUTS], check=False).returncode
    if changed == 0:
        report.ok(f'console image {tag[:7]} is built from the current tooling')
    elif changed == 1:
        report.warn(f'tooling changed since console image {tag[:7]}; after the publish run, '
                    'copy its tag and digest into platform/console/helmrelease.yaml')
    else:
        report.warn(f'cannot compare console image {tag[:7]} with this checkout (git fetch?)')


TOKEN_WARN_DAYS = 14


def check_console_token(runner: Runner, report: Report, now: dt.datetime | None = None) -> None:
    """GitHub accepts the console's token, and it is not about to expire (read from GitHub's reply)."""
    report.section('Console GitHub Token')
    name, key = platform.CONSOLE_TOKEN_SECRET, platform.CONSOLE_TOKEN_KEY
    try:
        secret = runner.json(['kubectl', '-n', 'console', 'get', 'secret', name, '-o', 'json'], secret_output=True)
        token = base64.b64decode(((secret or {}).get('data') or {}).get(key, '')).decode().strip()
    except (CommandError, binascii.Error, UnicodeDecodeError):
        token = ''
    runner.add_secret(token)
    if token in ('', 'REPLACE_ME'):
        report.bad(f'console/{name}.{key} is not set; set it with: sops platform/console/secret.sops.yaml')
        return
    reply = runner.run(['curl', '--silent', '--show-error', '--config', '-', '--output', '/dev/null', '--dump-header', '-',
                        f'https://api.github.com/repos/{platform.GITHUB_REPO}'],
                       input=f'header = "Authorization: Bearer {token}"\n', check=False, secret_output=True)
    lines = reply.stdout.splitlines()
    status = lines[0].split()[1] if lines and len(lines[0].split()) > 1 else 'no reply'
    headers = dict(line.split(':', 1) for line in lines[1:] if ':' in line)
    expires = {k.strip().lower(): v.strip() for k, v in headers.items()}.get('github-authentication-token-expiration')
    if status != '200':
        report.bad(f'GitHub did not accept the console token (HTTP {status}); create a new one and set it with sops')
        return
    if not expires:
        report.ok('GitHub accepts the console token (no expiry date)')
        return
    try:
        when = dt.datetime.strptime(expires[:19], '%Y-%m-%d %H:%M:%S').replace(tzinfo=dt.UTC)
    except ValueError:
        report.warn(f'GitHub accepts the console token; could not read its expiry {expires!r}')
        return
    days = ((when - (now or dt.datetime.now(dt.UTC))).total_seconds()) / 86400
    message = f'GitHub accepts the console token; it expires {when:%Y-%m-%d} ({days:.0f} days)'
    if days < TOKEN_WARN_DAYS:
        report.warn(message + '; create a new one and set it with sops platform/console/secret.sops.yaml')
    else:
        report.ok(message)


def check_ingestion(runner: Runner, report: Report) -> None:
    stored = read_ingestion_secret(runner)
    check_runtime_secret(stored, report)
    check_ingestion_key(runner, report, stored)


@dataclass(frozen=True)
class Check:
    """One group of checks and what it needs beyond reading the cluster.

    ``cluster``: read non-Secret objects; ``secret``: read Secrets; ``exec``: run
    commands in pods; ``host``: this machine's files or AWS credentials. The
    console runs only the checks its read-only account can (``{'cluster'}``).
    """
    name: str
    needs: frozenset[str]
    run: Callable[[Runner, Report], None]


CHECKS = (
    Check('flux', frozenset({'cluster'}), check_flux),
    Check('ingestion-key', frozenset({'cluster', 'secret', 'exec'}), check_ingestion),
    Check('registration', frozenset({'cluster', 'exec'}), check_registration),
    Check('ingress', frozenset({'cluster'}), check_ingress),
    Check('retention', frozenset({'cluster', 'exec'}), check_retention),
    Check('backups', frozenset({'host'}), check_backups),
    Check('console', frozenset({'cluster', 'host'}), check_console),
    Check('console-token', frozenset({'cluster', 'secret', 'host'}), check_console_token),
)
NEEDS = frozenset().union(*(check.needs for check in CHECKS))


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
    for check in CHECKS:
        if check.needs <= allowed:
            check.run(runner, report)
    if skipped:
        report.line()
        report.info(f"skipped (need more than {', '.join(sorted(allowed))}): {', '.join(skipped)}")
    if report.passed:
        report.line('\nValidation passed.')
    return report.exit_code()


def main(argv: list[str] | None = None, runner: Runner | None = None, report: Report | None = None) -> int:
    return verify_platform(runner or Runner(), report or Report())
