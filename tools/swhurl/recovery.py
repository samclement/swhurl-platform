"""Back up ClickStack MongoDB and prove a backup restores.

    backup-mongodb             dump the hyperdx database, encrypted with age, to BACKUP_DIR
    live-test-restore-mongodb  restore the newest backup into a throwaway namespace and check it
    prune-backups              prune a directory of MongoDB backups to the retention set

Plaintext never touches disk: the dump streams from ``mongodump`` straight into
``age`` (and on restore from ``age -d`` into ``mongorestore``) through an OS
pipe that Python never reads. MongoDB requires a login: the connection string
the MongoDB operator writes (``clickstack-mongodb-hyperdx-hyperdx``) reaches
``mongodump`` as a config file on stdin, never in a command line. The archives
are a backup set (``backupset``), which names, orders, prunes and uploads them. Settings come
from the environment, as the Makefile passes them: BACKUP_DIR, DRY_RUN, PRUNE,
KEEP_DAILY, KEEP_WEEKLY, AGE_RECIPIENT, MONGO_DATABASE, BACKUP_S3_URI (backup; empty
skips the upload, which uses the AWS CLI's default credentials or AWS_PROFILE) and
BACKUP_FILE, AGE_KEY_FILE, RECOVERY_NAMESPACE, KEEP, MONGO_IMAGE (restore test).
"""
from __future__ import annotations

import argparse
import base64
import binascii
import contextlib
import datetime as dt
import json
import os
import re
import sys
import time
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path

import yaml

from swhurl import ROOT, backupset, clickstack, platform
from swhurl.backupset import BackupError, BackupSet
from swhurl.report import Report
from swhurl.run import CommandError, Runner

INGESTION_SECRET = platform.INGESTION_SECRET
RECOVERY_LABEL = platform.label('recovery-test')


def backups(backup_dir: Path) -> BackupSet:
    """The MongoDB backups in ``backup_dir``: ``clickstack-mongodb-<UTC>.archive.gz.age``."""
    return BackupSet(backup_dir, 'clickstack-mongodb', '.archive.gz.age')


def sops_recipient(root: Path = ROOT) -> str:
    """The age recipient every SOPS rule encrypts to."""
    rules = (yaml.safe_load((root / '.sops.yaml').read_text()) or {}).get('creation_rules') or []
    recipients = {rule['age'].strip() for rule in rules if rule.get('age')}
    if len(recipients) != 1:
        raise BackupError('expected exactly one age recipient in .sops.yaml (set AGE_RECIPIENT)')
    return recipients.pop()


# Backup ----------------------------------------------------------------------

@dataclass
class BackupSettings:
    backup_dir: Path
    recipient: str
    namespace: str = clickstack.NS
    pod: str = clickstack.MONGO_POD
    database: str = 'hyperdx'
    prune: bool = True
    keep_daily: int = 7
    keep_weekly: int = 4
    s3_uri: str = ''

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> BackupSettings:
        return cls(
            backup_dir=backupset.backup_dir(env),
            recipient=env.get('AGE_RECIPIENT') or sops_recipient(),
            database=env.get('MONGO_DATABASE') or 'hyperdx',
            prune=env.get('PRUNE', 'true') == 'true',
            keep_daily=int(env.get('KEEP_DAILY', 7)),
            keep_weekly=int(env.get('KEEP_WEEKLY', 4)),
            s3_uri=env.get('BACKUP_S3_URI', platform.BACKUP_S3_URI),
        )


def backup(runner: Runner, settings: BackupSettings, *, now: dt.datetime | None = None,
           out: Callable[[str], None] = print) -> Path:
    """Write ``clickstack-mongodb-<UTC>.archive.gz.age`` plus metadata; return the archive path."""
    stamp = backupset.stamp(now)
    archives = backups(settings.backup_dir)
    archive = archives.path(stamp)
    exec_mongo = ['kubectl', '-n', settings.namespace, 'exec', '-i', settings.pod, '-c', 'mongod', '--']

    if runner.dry_run:
        out('Plan (backup-mongodb):')
        out(f'  - mongodump --db {settings.database} from {settings.namespace}/{settings.pod}, '
            f'logging in with {clickstack.MONGO_URI_SECRET}')
        out(f'  - encrypt to age recipient {settings.recipient}')
        out(f'  - write {archive} and a metadata file; no cluster changes')
        if settings.prune:
            out(f'  - prune {settings.backup_dir} to the newest backup of each of the last {settings.keep_daily} '
                f'backup days and {settings.keep_weekly} ISO weeks')
        if settings.s3_uri:
            out(f'  - upload local backups not yet in {settings.s3_uri}')
        return archive

    uri = clickstack.read_secret(runner, clickstack.MONGO_URI_SECRET).get('connectionString.standard', '')
    if not uri:
        raise BackupError(f'{settings.namespace}/{clickstack.MONGO_URI_SECRET} has no connection string')
    with archives.writing(stamp) as partial:
        runner.pipe([*exec_mongo, 'mongodump', '--quiet', '--config=/dev/stdin', '--db', settings.database,
                     '--archive', '--gzip'],
                    ['age', '-r', settings.recipient, '-o', partial], input=f'uri: {json.dumps(uri)}\n')
        if not partial.is_file() or partial.stat().st_size == 0:
            raise BackupError('Backup archive is empty')
        counts = clickstack.mongo(runner, uri, clickstack.COLLECTION_COUNTS)
        version_text = runner.output([*exec_mongo, 'mongod', '--version'])
        match = re.search(r'^db version v(\S+)', version_text, re.M)
        if not match:
            raise BackupError('could not read the MongoDB version')
    metadata = archives.record(archive, {
        'source': f'{settings.namespace}/{settings.pod}', 'database': settings.database,
        'mongodb_version': match[1], 'age_recipient': settings.recipient, 'collections': counts})
    out(f'[OK] Encrypted backup: {archive}')
    out(f'[OK] Metadata: {metadata}')
    if settings.prune:
        archives.prune(settings.keep_daily, settings.keep_weekly, out=out)
    if settings.s3_uri:
        archives.upload(runner, settings.s3_uri, out)
    else:
        out('[INFO] BACKUP_S3_URI is empty: nothing copied off-host.')
    return archive


def backup_mongodb(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    try:
        settings = BackupSettings.from_env(os.environ)
        backup(runner or Runner.from_environment(), settings)
    except (BackupError, CommandError, ValueError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    return 0


# Restore test ----------------------------------------------------------------

@dataclass
class RestoreSettings:
    backup_dir: Path
    age_key: Path
    backup_file: Path | None = None
    namespace: str = 'recovery-test'
    keep: bool = False
    image: str | None = None

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> RestoreSettings:
        return cls(
            backup_dir=backupset.backup_dir(env),
            age_key=backupset.age_key(env),
            backup_file=Path(env['BACKUP_FILE']) if env.get('BACKUP_FILE') else None,
            namespace=env.get('RECOVERY_NAMESPACE') or 'recovery-test',
            keep=env.get('KEEP', 'false') == 'true',
            image=env.get('MONGO_IMAGE') or None,
        )

    def archive(self) -> Path | None:
        return self.backup_file or backups(self.backup_dir).newest()


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
    archive = settings.archive()
    ns = settings.namespace
    if runner.dry_run:
        report.line('Plan (live-test-restore-mongodb):')
        report.line(f'  - create disposable namespace {ns} (label {RECOVERY_LABEL}=true) with a throwaway MongoDB pod')
        report.line(f'  - decrypt {archive or f"<latest backup in {settings.backup_dir}>"} with {settings.age_key} '
                    'and mongorestore into it')
        report.line(f'  - decrypt {ROOT / INGESTION_SECRET} into {ns} and compare with the restored team ingestion key')
        report.line(f'  - compare restored collection counts with the backup metadata, then delete {ns}')
        return 0

    if not archive:
        raise BackupError('No backup archive found (set BACKUP_FILE or BACKUP_DIR)')
    if not os.access(settings.age_key, os.R_OK):
        raise BackupError(f'Cannot read age key: {settings.age_key}')
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

        secret = yaml.safe_load(runner.output(['sops', 'decrypt', ROOT / INGESTION_SECRET], secret_output=True,
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


def prune_backups(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description='Prune ClickStack MongoDB backups to the newest of each of the '
                                     'last --daily backup days and --weekly ISO weeks.')
    parser.add_argument('directory', type=Path)
    parser.add_argument('--daily', type=int, default=7)
    parser.add_argument('--weekly', type=int, default=4)
    parser.add_argument('--dry-run', action='store_true')
    args = parser.parse_args(argv)
    backups(args.directory).prune(args.daily, args.weekly, dry_run=args.dry_run)
    return 0


def check_backups(runner: Runner, report: Report, env: Mapping[str, str] | None = None,
                  now: dt.datetime | None = None) -> None:
    """The newest MongoDB backup, locally and off-host, must be younger than BACKUP_MAX_AGE_HOURS (26)."""
    env = os.environ if env is None else env
    report.section('Backups')
    backupset.check_backup_set(runner, report, backups(backupset.backup_dir(env)),
                     env.get('BACKUP_S3_URI', platform.BACKUP_S3_URI), env=env, now=now, what='MongoDB',
                     run='make backup-mongodb', hint='systemctl status swhurl-backup-mongodb')
