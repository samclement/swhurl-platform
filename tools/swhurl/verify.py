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

from swhurl import ROOT, clickstack, flux, images, platform, recovery, sqlite_backup
from swhurl.report import Report
from swhurl.run import CommandError, Runner
from swhurl.settings import SettingsError, load_settings

APP_LABEL = 'platform.swhurl.com/app'  # apps.contract.APP; set on each app's ImagePolicy
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


def check_flux_controllers(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """The controllers run the pinned version with the settings in Git (a plain `flux install` drops them)."""
    report.section('Flux Controllers')
    version = flux.pinned_version(root)
    required = flux.required_args(root)
    for name in flux.CONTROLLERS:
        args = required.get(name, [])
        try:
            deployment = runner.json(['kubectl', '-n', 'flux-system', 'get', 'deployment', name, '-o', 'json'])
        except CommandError:
            report.warn(f'{name} is not installed (run: make flux-install)')
            continue
        try:
            live = deployment['metadata'].get('labels', {}).get('app.kubernetes.io/version', '')
            running = [a for c in deployment['spec']['template']['spec']['containers'] for a in c.get('args') or []]
        except (KeyError, TypeError):
            report.bad(f'could not read flux-system/{name}')
            continue
        missing = [a for a in args if a not in running]
        if missing or live != f'v{version}':
            expected = f'v{version}' + (f' with {", ".join(args)}' if args else '')
            report.warn(f'{name} is {live or "unknown"}' + (f' with {len(args) - len(missing)}/{len(args)} Git settings'
                        if args else '') + f'; expected {expected} (run: make flux-install)')
        else:
            report.ok(f'{name} {live}' + (' with the settings in Git' if args else ''))


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

    # A merge that fails is retried at once, burning CPU and logging a stack trace each time.
    try:
        failed, table = (clickhouse(runner, (
            "SELECT count(), any(database || '.' || table) FROM system.part_log WHERE event_type = 'MergeParts' "
            "AND error != 0 AND event_time > now() - INTERVAL 1 HOUR FORMAT TSV")).split('\t') + [''])[:2]
        failed = int(failed)
    except (CommandError, ValueError):
        report.bad('could not read ClickHouse merge results (system.part_log)')
    else:
        if failed == 0:
            report.ok('no ClickHouse merge failed in the last hour')
        else:
            report.bad(f'{failed} ClickHouse merge(s) failed in the last hour (for example {table}); '
                       'see docs/operations.md#troubleshooting')

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


def check_sqlite_backups(runner: Runner, report: Report, env: Mapping[str, str] | None = None,
                         now: dt.datetime | None = None) -> None:
    """Each app SQLite database's newest backup, locally and off-host, is younger than BACKUP_MAX_AGE_HOURS (26)."""
    env = os.environ if env is None else env
    report.section('App SQLite backups')
    try:
        databases = sqlite_backup.find(runner)
    except (CommandError, recovery.RecoveryError, KeyError, TypeError) as error:
        report.bad(f'cannot find app SQLite databases: {error}')
        return
    if not databases:
        report.ok('no app has a SQLite database')
        return
    now = now or dt.datetime.now(dt.UTC)
    limit = dt.timedelta(hours=float(env.get('BACKUP_MAX_AGE_HOURS') or 26))
    backup_dir = Path(env.get('BACKUP_DIR') or recovery.DEFAULT_BACKUP_DIR)
    uri = env.get('SQLITE_S3_URI', platform.SQLITE_S3_URI)

    def newest(names: list[str]) -> dt.datetime | None:
        times = [dt.datetime.strptime(m[1], '%Y%m%dT%H%M%SZ').replace(tzinfo=dt.UTC)
                 for m in map(sqlite_backup.PATTERN.match, names) if m]
        return max(times) if times else None

    for db in databases:
        local = backup_dir / 'sqlite' / db.namespace
        places = [(str(local), lambda local=local: [p.name for p in local.iterdir()] if local.is_dir() else [])]
        if uri:
            places.append((f'{uri}{db.namespace}/', lambda ns=db.namespace: recovery.remote_names(runner, f'{uri}{ns}/')))
        for where, names in places:
            try:
                taken = newest(names())
            except (CommandError, recovery.RecoveryError, TypeError) as error:
                report.bad(f'{db.name}: cannot list backups in {where}: {error}')
                continue
            if taken is None:
                report.bad(f'{db.name}: no SQLite backup in {where} (run: make backup-sqlite)')
            elif now - taken > limit:
                report.bad(f'{db.name}: newest SQLite backup in {where} is {(now - taken).total_seconds() / 3600:.0f} h old; '
                           'check: systemctl status swhurl-backup-mongodb')
            else:
                report.ok(f'{db.name}: newest SQLite backup in {where} is {(now - taken).total_seconds() / 3600:.1f} h old')


def check_dashboards(runner: Runner, report: Report, now: dt.datetime | None = None) -> None:
    """A successful minute-by-minute sync must be recent, and scheduling enabled."""
    report.section('App dashboards')
    now = now or dt.datetime.now(dt.UTC)
    try:
        job = runner.json(['kubectl', '-n', 'console', 'get', 'cronjob', 'console-dashboards', '-o', 'json'])
        last = (job.get('status') or {}).get('lastSuccessfulTime', '')
        taken = dt.datetime.fromisoformat(last.replace('Z', '+00:00')) if last else None
        if job['spec'].get('suspend'):
            report.bad('dashboard sync is suspended')
        elif taken is None or now - taken > dt.timedelta(minutes=5):
            report.bad('dashboard sync has no success in the last 5 minutes; check console-dashboards job logs')
        else:
            report.ok('dashboard sync succeeded in the last 5 minutes')
    except (CommandError, KeyError, ValueError, TypeError):
        report.bad('cannot read dashboard sync status (console/console-dashboards)')


def check_notifications(runner: Runner, report: Report, now: dt.datetime | None = None) -> None:
    """A recent successful check proves the snapshot/outbox pass finished, not subscriber delivery."""
    report.section('Notifications')
    now = now or dt.datetime.now(dt.UTC)
    try:
        job = runner.json(['kubectl', '-n', 'console', 'get', 'cronjob', 'console-notifications', '-o', 'json'])
        last = (job.get('status') or {}).get('lastSuccessfulTime', '')
        taken = dt.datetime.fromisoformat(last.replace('Z', '+00:00')) if last else None
        if job['spec'].get('suspend'):
            report.bad('notification checker is suspended')
        elif taken is None or now - taken > dt.timedelta(minutes=5):
            report.bad('notification checker has no success in the last 5 minutes; check console-notifications job logs')
        else:
            report.ok('notification checker succeeded in the last 5 minutes')
    except (CommandError, KeyError, ValueError, TypeError):
        report.bad('cannot read notification checker status (console/console-notifications)')


def check_console(runner: Runner, report: Report) -> None:
    """Warn if the console image's inputs changed since it was built.

    A ``src-<hash>`` tag (make console-image) is compared with the hash of this
    checkout's inputs; a commit tag with git diff since that commit.
    """
    report.section('Console')
    try:
        release = runner.json(['kubectl', '-n', 'console', 'get', 'helmrelease', 'console', '-o', 'json'])
        tag = release['spec']['values']['controllers']['main']['containers']['main']['image']['tag']
    except (CommandError, KeyError, TypeError):
        report.bad('cannot read the console HelmRelease image tag (console/console)')
        return
    label = tag if tag.startswith('src-') else tag[:7]
    if tag.startswith('src-'):
        try:
            changed = 0 if images.content_tag(runner) == tag else 1
        except CommandError:
            changed = 2
    else:
        changed = runner.run(['git', '-C', str(ROOT), 'diff', '--quiet', tag, 'HEAD', '--',
                              *platform.CONSOLE_IMAGE_INPUTS], check=False).returncode
    if changed == 0:
        report.ok(f'console image {label} is built from the current tooling')
    elif changed == 1:
        report.warn(f'tooling changed since console image {label}; after the publish run: make console-image, '
                    'commit and push')
    else:
        report.warn(f'cannot compare console image {label} with this checkout (git fetch?)')


TOKEN_WARN_DAYS = 14


def check_push_webhook(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """Flux's GitHub Receiver is Ready and GitHub's latest delivery to it succeeded (docs/services.md#push-webhook)."""
    report.section('Push Webhook')
    try:
        receiver = runner.json(['kubectl', '-n', 'flux-system', 'get', 'receivers.notification.toolkit.fluxcd.io',
                                'github', '-o', 'json'])
        status, message = ready_condition(receiver)
    except (CommandError, TypeError):
        status, message = 'Unknown', 'could not read it'
    if status == 'True':
        report.ok('Flux receiver flux-system/github is Ready')
    else:
        report.bad(f'Flux receiver flux-system/github is not Ready: {message} (make reconcile UNIT=platform-flux-webhook)')
    host = f'flux-webhook.{platform.base_domain(root)}'
    repository = platform.github_repository(root)
    try:
        hooks = runner.json(['gh', 'api', f'repos/{repository}/hooks'])
    except CommandError as error:
        report.warn(f'could not list the GitHub webhooks of {repository} with gh: {error}')
        return
    hook = next((h for h in hooks or [] if host in (h.get('config') or {}).get('url', '')), None)
    if not hook:
        report.warn(f'no GitHub webhook on {repository} calls {host}; Flux falls back to polling every minute')
        return
    last = hook.get('last_response') or {}
    if not hook.get('active'):
        report.warn(f'the GitHub webhook to {host} is disabled; Flux falls back to polling every minute')
    elif last.get('code') == 200:
        report.ok(f'GitHub\'s latest delivery to {host} returned 200')
    else:
        report.warn(f'GitHub\'s latest delivery to {host} returned {last.get("code")} {last.get("message", "")}'.rstrip()
                    + '; see Recent Deliveries on the webhook (Flux still polls every minute)')


def check_image_automation(runner: Runner, report: Report) -> None:
    """Automatic staging deploys: every app ImagePolicy and its named writer are Ready."""
    report.section('Image Automation')
    base = ['kubectl', '-n', 'flux-system', 'get']
    try:
        policies = runner.json([*base, 'imagepolicies.image.toolkit.fluxcd.io', '-l', APP_LABEL, '-o', 'json'])['items']
        automations = runner.json([*base, 'imageupdateautomations.image.toolkit.fluxcd.io', '-l', APP_LABEL,
                                   '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad('could not read the apps\' ImagePolicies or ImageUpdateAutomations')
        return
    by_name = {a['metadata']['name']: a for a in automations}
    for policy in sorted(policies, key=lambda p: p['metadata']['name']):
        name = policy['metadata']['name']
        automation = by_name.get(name)
        if automation is None:
            report.bad(f'ImageUpdateAutomation {name} is missing: staging image updates are not automatic')
        else:
            status, message = ready_condition(automation)
            if status == 'True':
                pushed = (automation.get('status') or {}).get('lastPushTime')
                report.ok(f'ImageUpdateAutomation {name} is Ready' + (f'; last pushed {pushed}' if pushed else ''))
            else:
                report.bad(f'ImageUpdateAutomation {name} is not Ready: {message}')
        status, message = ready_condition(policy)
        latest = ((policy.get('status') or {}).get('latestRef') or {}).get('tag', '')
        if status == 'True':
            report.ok(f'{name}: newest image {latest}')
        else:
            report.bad(f'ImagePolicy {name} is not Ready: {message}')


ALERTS = ('failures',)  # platform/alerts/alerts.yaml


def check_alerts(runner: Runner, report: Report) -> None:
    """The ntfy alerts exist and point at existing providers (v1beta3 objects report no Ready status)."""
    report.section('Alerts')
    base = ['kubectl', '-n', 'flux-system', 'get']
    try:
        alerts = {a['metadata']['name']: a for a in runner.json([*base, 'alerts.notification.toolkit.fluxcd.io',
                                                                 '-o', 'json'])['items']}
        providers = {p['metadata']['name'] for p in runner.json([*base, 'providers.notification.toolkit.fluxcd.io',
                                                                 '-o', 'json'])['items']}
    except (CommandError, KeyError, TypeError):
        report.bad('could not read Flux alerts and providers')
        return
    for name in ALERTS:
        alert = alerts.get(name)
        provider = ((alert or {}).get('spec') or {}).get('providerRef', {}).get('name')
        if alert is None:
            report.warn(f'alert {name} is missing: no push notifications for it (make reconcile UNIT=platform-alerts)')
        elif provider not in providers:
            report.bad(f'alert {name} points at provider {provider}, which does not exist')
        else:
            report.ok(f'alert {name} sends to {provider}')


def check_console_token(runner: Runner, report: Report, now: dt.datetime | None = None) -> None:
    """GitHub accepts the console's tokens, and none is about to expire (read from GitHub's reply).

    GITHUB_TOKEN (pull requests here) is required; APP_REPOS_TOKEN (new app repositories) is optional:
    without it only the console's New app and repository tab is off."""
    report.section('Console GitHub Token')
    name = platform.CONSOLE_TOKEN_SECRET
    try:
        secret = runner.json(['kubectl', '-n', 'console', 'get', 'secret', name, '-o', 'json'], secret_output=True)
    except CommandError:
        secret = None
    data = (secret or {}).get('data') or {}
    for key, label, required in ((platform.CONSOLE_TOKEN_KEY, 'console token', True),
                                 (platform.APP_REPOS_TOKEN_KEY, 'console repository token', False)):
        try:
            token = base64.b64decode(data.get(key, '')).decode().strip()
        except (binascii.Error, UnicodeDecodeError):
            token = ''
        runner.add_secret(token)
        if token in ('', 'REPLACE_ME'):
            message = f'console/{name}.{key} is not set; set it with: sops platform/console/secret.sops.yaml'
            if required:
                report.bad(message)
            else:
                report.warn(message + " (the console's New app and repository tab is off until then)")
            continue
        check_github_token(runner, report, token, label, now)


def check_github_token(runner: Runner, report: Report, token: str, label: str, now: dt.datetime | None) -> None:
    reply = runner.run(['curl', '--silent', '--show-error', '--config', '-', '--output', '/dev/null', '--dump-header', '-',
                        f'https://api.github.com/repos/{platform.GITHUB_REPO}'],
                       input=f'header = "Authorization: Bearer {token}"\n', check=False, secret_output=True)
    lines = reply.stdout.splitlines()
    status = lines[0].split()[1] if lines and len(lines[0].split()) > 1 else 'no reply'
    headers = dict(line.split(':', 1) for line in lines[1:] if ':' in line)
    expires = {k.strip().lower(): v.strip() for k, v in headers.items()}.get('github-authentication-token-expiration')
    if status != '200':
        report.bad(f'GitHub did not accept the {label} (HTTP {status}); create a new one and set it with sops')
        return
    if not expires:
        report.ok(f'GitHub accepts the {label} (no expiry date)')
        return
    try:
        when = dt.datetime.strptime(expires[:19], '%Y-%m-%d %H:%M:%S').replace(tzinfo=dt.UTC)
    except ValueError:
        report.warn(f'GitHub accepts the {label}; could not read its expiry {expires!r}')
        return
    days = ((when - (now or dt.datetime.now(dt.UTC))).total_seconds()) / 86400
    message = f'GitHub accepts the {label}; it expires {when:%Y-%m-%d} ({days:.0f} days)'
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
    Check('flux-controllers', frozenset({'cluster', 'host'}), check_flux_controllers),
    Check('ingestion-key', frozenset({'cluster', 'secret', 'exec'}), check_ingestion),
    Check('registration', frozenset({'cluster', 'exec'}), check_registration),
    Check('ingress', frozenset({'cluster'}), check_ingress),
    Check('image-automation', frozenset({'cluster'}), check_image_automation),
    Check('alerts', frozenset({'cluster'}), check_alerts),
    Check('retention', frozenset({'cluster', 'exec'}), check_retention),
    Check('backups', frozenset({'host'}), check_backups),
    Check('sqlite-backups', frozenset({'cluster', 'host'}), check_sqlite_backups),
    Check('push-webhook', frozenset({'cluster', 'host'}), check_push_webhook),
    Check('dashboards', frozenset({'cluster'}), check_dashboards),
    Check('notifications', frozenset({'cluster'}), check_notifications),
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
