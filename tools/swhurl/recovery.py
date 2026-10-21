"""Prove ClickStack MongoDB's backup restores, and check its backups are fresh.

    live-test-restore-mongodb  restore the newest backup into a throwaway namespace and check it

The backup itself is the ``backup-mongodb`` CronJob (``backup_job``). The restore test downloads the
newest complete backup from BACKUP_S3_URI into a private temporary directory with the AWS CLI's own
credentials (or takes BACKUP_FILE, with its metadata beside it), then streams ``age -d`` straight
into ``mongorestore`` through an OS pipe that Python never reads, so plaintext never touches disk.
Settings come from the environment: BACKUP_S3_URI, BACKUP_FILE, AGE_KEY_FILE, RECOVERY_NAMESPACE,
KEEP, MONGO_IMAGE.
"""
from __future__ import annotations

import base64
import binascii
import contextlib
import datetime as dt
import json
import os
import sys
import tempfile
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path

import yaml

from swhurl import ROOT, backupset, clickstack, interface, platform, yamlio
from swhurl.backupset import BackupError, BackupSet
from swhurl.report import Report
from swhurl.run import CommandError, Runner

INGESTION_SECRET = platform.INGESTION_SECRET
RECOVERY_LABEL = interface.label('recovery-test')


def backups(backup_dir: Path) -> BackupSet:
    """The MongoDB backups in ``backup_dir``: ``clickstack-mongodb-<UTC>.archive.gz.age``."""
    return BackupSet(backup_dir, 'clickstack-mongodb', '.archive.gz.age')


def sops_recipient(root: Path = ROOT) -> str:
    """The age recipient every SOPS rule encrypts to."""
    rules = (yamlio.load((root / '.sops.yaml').read_text()) or {}).get('creation_rules') or []
    recipients = {rule['age'].strip() for rule in rules if rule.get('age')}
    if len(recipients) != 1:
        raise BackupError('expected exactly one age recipient in .sops.yaml (set AGE_RECIPIENT)')
    return recipients.pop()


# Restore test ----------------------------------------------------------------

@dataclass
class RestoreSettings:
    uri: str
    age_key: Path
    backup_file: Path | None = None
    namespace: str = 'recovery-test'
    keep: bool = False
    image: str | None = None

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> RestoreSettings:
        return cls(
            uri=env.get('BACKUP_S3_URI', platform.BACKUP_S3_URI),
            age_key=backupset.age_key(env),
            backup_file=Path(env['BACKUP_FILE']) if env.get('BACKUP_FILE') else None,
            namespace=env.get('RECOVERY_NAMESPACE') or 'recovery-test',
            keep=env.get('KEEP', 'false') == 'true',
            image=env.get('MONGO_IMAGE') or None,
        )


def recovery_pod(image: str) -> str:
    return yaml.safe_dump({
        'apiVersion': 'v1', 'kind': 'Pod',
        'metadata': {'name': 'mongodb', 'labels': {RECOVERY_LABEL: 'true'}},
        'spec': {'automountServiceAccountToken': False,
                 'containers': [{'name': 'mongodb', 'image': image,
                                 'volumeMounts': [{'name': 'data', 'mountPath': '/data/db'}]}],
                 'volumes': [{'name': 'data', 'emptyDir': {}}]},
    })


def restore_test(runner: Runner, settings: RestoreSettings, report: Report, *,
                 sleep: Callable[[float], None] = time.sleep) -> int:
    report.redact = runner.redact
    ns = settings.namespace
    if runner.dry_run:
        report.line('Plan (live-test-restore-mongodb):')
        report.line(f'  - create disposable namespace {ns} (label {RECOVERY_LABEL}=true) with a throwaway MongoDB pod')
        report.line(f'  - download {settings.backup_file or f"the newest complete backup in {settings.uri}"}, '
                    f'decrypt it with {settings.age_key} and mongorestore into it')
        report.line(f'  - decrypt {ROOT / INGESTION_SECRET} into {ns} and compare with the restored team ingestion key')
        report.line(f'  - compare restored collection counts with the backup metadata, then delete {ns}')
        return 0

    if not os.access(settings.age_key, os.R_OK):
        raise BackupError(f'Cannot read age key: {settings.age_key}')
    with tempfile.TemporaryDirectory(prefix='restore-mongodb-') as scratch:
        archive = settings.backup_file or backups(Path(scratch)).fetch(runner, settings.uri)
        return restored(runner, settings, report, archive, sleep=sleep)


def restored(runner: Runner, settings: RestoreSettings, report: Report, archive: Path, *,
             sleep: Callable[[float], None]) -> int:
    ns = settings.namespace
    metadata = backups(archive.parent).read(archive)

    existing = runner.run(['kubectl', 'get', 'namespace', ns, '-o', 'json'], check=False)
    if existing.returncode == 0:
        labels = (json.loads(existing.stdout).get('metadata') or {}).get('labels') or {}
        if labels.get(RECOVERY_LABEL) != 'true':
            raise BackupError(f'Namespace {ns} exists and is not a recovery-test namespace; refusing to use it')
        raise BackupError(f'Namespace {ns} already exists; delete it or set RECOVERY_NAMESPACE')

    with contextlib.ExitStack() as cleanup:
        runner.run(['kubectl', 'create', 'namespace', ns], mutating=True)
        cleanup.callback(delete_namespace, runner, settings, report)
        runner.run(['kubectl', 'label', 'namespace', ns, f'{RECOVERY_LABEL}=true'], mutating=True)

        image = settings.image or f"mongo:{metadata['mongodb_version']}-focal"
        runner.run(['kubectl', '-n', ns, 'apply', '-f', '-'], input=recovery_pod(image), mutating=True)
        runner.run(['kubectl', '-n', ns, 'wait', '--for=condition=Ready', 'pod/mongodb', '--timeout=300s'])
        for _ in range(30):
            if runner.run(['kubectl', '-n', ns, 'exec', 'mongodb', '--', 'mongosh', '--quiet', '--eval',
                           'db.runCommand({ping: 1}).ok'], check=False).returncode == 0:
                break
            sleep(2)
        report.ok(f'Disposable MongoDB ({image}) ready in {ns}')

        runner.pipe(['age', '-d', '-i', settings.age_key, archive],
                    ['kubectl', '-n', ns, 'exec', '-i', 'mongodb', '--', 'mongorestore', '--quiet', '--archive',
                     '--gzip', '--drop'], mutating=True)
        report.ok(f'Restored {archive.name}')

        secret = yamlio.load(runner.output(['sops', 'decrypt', ROOT / INGESTION_SECRET], secret_output=True,
                                              env={'SOPS_AGE_KEY_FILE': str(settings.age_key)}))
        for value in (secret.get('stringData') or {}).values():
            runner.add_secret(str(value))
        for value in (secret.get('data') or {}).values():
            runner.add_secret(value)
            with contextlib.suppress(binascii.Error, ValueError):
                runner.add_secret(base64.b64decode(value))
        secret['metadata']['namespace'] = ns
        runner.run(['kubectl', 'apply', '-f', '-'], input=yaml.safe_dump(secret), secret_output=True, mutating=True)
        report.ok(f'Restored {platform.INGESTION_SECRET_NAME} from Git into {ns}')

        scratch = clickstack.MongoPod(ns, 'mongodb', 'mongodb')
        local = 'mongodb://127.0.0.1:27017/hyperdx'  # the throwaway MongoDB has no login
        restored = clickstack.mongo(runner, local, clickstack.COLLECTION_COUNTS, pod=scratch)
        if restored == metadata['collections']:
            report.ok('Restored collection counts match backup metadata')
        else:
            report.bad('Restored collection counts differ from backup metadata')

        try:
            keys = clickstack.mongo(runner, local, clickstack.TEAM_KEYS, pod=scratch)['keys']
        except (CommandError, ValueError, KeyError):
            keys = []
        for key in keys:
            runner.add_secret(key)
        team_key = keys[0] if len(keys) == 1 else ''
        runner.add_secret(team_key)
        stored = runner.json(['kubectl', '-n', ns, 'get', 'secret', platform.INGESTION_SECRET_NAME, '-o', 'json'],
                             secret_output=True)
        try:
            secret_key = base64.b64decode(((stored or {}).get('data') or {}).get(platform.INGESTION_KEY, '')).decode()
        except (binascii.Error, ValueError):
            secret_key = ''
        if team_key and team_key == secret_key:
            report.ok('Restored team ingestion key matches the restored Git Secret')
        else:
            report.bad('Restored team ingestion key does not match the restored Git Secret')
        if report.passed:
            report.line('Restore test passed.')
    return report.exit_code()


def delete_namespace(runner: Runner, settings: RestoreSettings, report: Report) -> None:
    if settings.keep:
        report.info(f'KEEP=true: leaving namespace {settings.namespace}')
        return
    runner.run(['kubectl', 'delete', 'namespace', settings.namespace, '--wait=false'], check=False, mutating=True)
    report.info(f'Deleted disposable namespace {settings.namespace}')


def restore_test_mongodb(argv: list[str] | None = None, runner: Runner | None = None,
                         report: Report | None = None) -> int:
    runner = runner or Runner.from_environment()
    report = report or Report()
    try:
        return restore_test(runner, RestoreSettings.from_env(os.environ), report)
    except (BackupError, CommandError, KeyError, ValueError) as error:
        print(f'[ERROR] {runner.redact(str(error))}', file=sys.stderr)
        return 1


def check_backups(runner: Runner, report: Report, env: Mapping[str, str] | None = None,
                  now: dt.datetime | None = None) -> None:
    """The newest MongoDB backup in S3 must be younger than BACKUP_MAX_AGE_HOURS (26)."""
    env = os.environ if env is None else env
    report.section('Backups')
    backupset.check_backup_set(runner, report, backups(Path()),
                     env.get('BACKUP_S3_URI', platform.BACKUP_S3_URI), env=env, now=now, what='MongoDB',
                     run='make backup-mongodb', hint='kubectl -n observability get jobs -l platform.swhurl.com/backup=true')
