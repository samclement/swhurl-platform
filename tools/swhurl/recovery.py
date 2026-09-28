"""Back up ClickStack MongoDB and prove a backup restores.

    backup-mongodb          dump the hyperdx database, encrypted with age, to BACKUP_DIR
    restore-test-mongodb    restore the latest backup into a throwaway namespace and check it

Plaintext never touches disk: the dump streams from ``mongodump`` straight into
``age`` (and on restore from ``age -d`` into ``mongorestore``) through an OS
pipe that Python never reads. Settings come from the environment, as the
Makefile passes them: BACKUP_DIR, DRY_RUN, PRUNE, KEEP_DAILY, KEEP_WEEKLY,
AGE_RECIPIENT, MONGO_NAMESPACE, MONGO_WORKLOAD, MONGO_DATABASE (backup) and
BACKUP_FILE, AGE_KEY_FILE, RECOVERY_NAMESPACE, KEEP, MONGO_IMAGE (restore test).
"""
from __future__ import annotations

import base64
import binascii
import contextlib
import datetime as dt
import hashlib
import json
import os
import re
import sys
import time
from collections.abc import Callable, Iterator, Mapping
from dataclasses import dataclass
from pathlib import Path

import yaml

from swhurl import ROOT, platform, retention
from swhurl.report import Report
from swhurl.run import CommandError, Runner

DEFAULT_BACKUP_DIR = Path.home() / '.local/state/swhurl-platform/backups'
COUNTS_SCRIPT = platform.COLLECTION_COUNTS_SCRIPT
TEAM_KEY_SCRIPT = platform.TEAM_KEY_SCRIPT
INGESTION_SECRET = platform.INGESTION_SECRET
RECOVERY_LABEL = platform.label('recovery-test')


class RecoveryError(Exception):
    pass


def sops_recipient(root: Path = ROOT) -> str:
    """The age recipient every SOPS rule encrypts to."""
    rules = (yaml.safe_load((root / '.sops.yaml').read_text()) or {}).get('creation_rules') or []
    recipients = {rule['age'].strip() for rule in rules if rule.get('age')}
    if len(recipients) != 1:
        raise RecoveryError('expected exactly one age recipient in .sops.yaml (set AGE_RECIPIENT)')
    return recipients.pop()


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open('rb') as handle:
        for block in iter(lambda: handle.read(1 << 20), b''):
            digest.update(block)
    return digest.hexdigest()


@contextlib.contextmanager
def private_files() -> Iterator[None]:
    """Create files and directories readable by the owner only (umask 077)."""
    previous = os.umask(0o077)
    try:
        yield
    finally:
        os.umask(previous)


# Backup ----------------------------------------------------------------------

@dataclass
class BackupSettings:
    backup_dir: Path
    recipient: str
    namespace: str = 'observability'
    workload: str = 'deploy/clickstack-mongodb'
    database: str = 'hyperdx'
    prune: bool = True
    keep_daily: int = 7
    keep_weekly: int = 4

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> BackupSettings:
        return cls(
            backup_dir=Path(env.get('BACKUP_DIR') or DEFAULT_BACKUP_DIR),
            recipient=env.get('AGE_RECIPIENT') or sops_recipient(),
            namespace=env.get('MONGO_NAMESPACE') or 'observability',
            workload=env.get('MONGO_WORKLOAD') or 'deploy/clickstack-mongodb',
            database=env.get('MONGO_DATABASE') or 'hyperdx',
            prune=env.get('PRUNE', 'true') == 'true',
            keep_daily=int(env.get('KEEP_DAILY', 7)),
            keep_weekly=int(env.get('KEEP_WEEKLY', 4)),
        )


def backup(runner: Runner, settings: BackupSettings, *, now: dt.datetime | None = None,
           out: Callable[[str], None] = print) -> Path:
    """Write ``clickstack-mongodb-<UTC>.archive.gz.age`` plus metadata; return the archive path."""
    stamp = (now or dt.datetime.now(dt.UTC)).strftime('%Y%m%dT%H%M%SZ')
    archive = settings.backup_dir / f'clickstack-mongodb-{stamp}.archive.gz.age'
    metadata = archive.with_name(archive.name.replace('.archive.gz.age', '.json'))
    exec_mongo = ['kubectl', '-n', settings.namespace, 'exec', settings.workload, '--']

    if runner.dry_run:
        out('Plan (backup-clickstack-mongodb):')
        out(f'  - mongodump --db {settings.database} from {settings.namespace}/{settings.workload}')
        out(f'  - encrypt to age recipient {settings.recipient}')
        out(f'  - write {archive} and a metadata file; no cluster changes')
        if settings.prune:
            out(f'  - prune {settings.backup_dir} to the newest backup of each of the last {settings.keep_daily} '
                f'backup days and {settings.keep_weekly} ISO weeks')
        return archive

    with private_files():
        settings.backup_dir.mkdir(parents=True, exist_ok=True)
        partial = archive.with_name(archive.name + '.partial')
        try:
            runner.pipe([*exec_mongo, 'mongodump', '--quiet', '--db', settings.database, '--archive', '--gzip'],
                        ['age', '-r', settings.recipient, '-o', partial])
            if not partial.is_file() or partial.stat().st_size == 0:
                raise RecoveryError('Backup archive is empty')
            counts = json.loads(runner.output([*exec_mongo, 'mongosh', settings.database, '--quiet',
                                               '--eval', COUNTS_SCRIPT]))
            version_text = runner.output([*exec_mongo, 'mongod', '--version'])
            match = re.search(r'^db version v(\S+)', version_text, re.M)
            if not match:
                raise RecoveryError('could not read the MongoDB version')
            partial.replace(archive)
        finally:
            partial.unlink(missing_ok=True)
        metadata.write_text(json.dumps({
            'created': stamp, 'source': f'{settings.namespace}/{settings.workload}', 'database': settings.database,
            'mongodb_version': match[1], 'age_recipient': settings.recipient, 'sha256': sha256(archive),
            'collections': counts}) + '\n')
    out(f'[OK] Encrypted backup: {archive}')
    out(f'[OK] Metadata: {metadata}')
    if settings.prune:
        retention.main([str(settings.backup_dir), '--daily', str(settings.keep_daily),
                      '--weekly', str(settings.keep_weekly)])
    out('[INFO] Copy both files off-host; restore needs the age private key '
        '(see docs/operations.md#backups-and-recovery).')
    return archive


def backup_mongodb(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    try:
        settings = BackupSettings.from_env(os.environ)
        backup(runner or Runner.from_environment(), settings)
    except (RecoveryError, CommandError, ValueError) as error:
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
            backup_dir=Path(env.get('BACKUP_DIR') or DEFAULT_BACKUP_DIR),
            age_key=Path(env.get('AGE_KEY_FILE') or env.get('SOPS_AGE_KEY_FILE') or ROOT / platform.AGE_KEY),
            backup_file=Path(env['BACKUP_FILE']) if env.get('BACKUP_FILE') else None,
            namespace=env.get('RECOVERY_NAMESPACE') or 'recovery-test',
            keep=env.get('KEEP', 'false') == 'true',
            image=env.get('MONGO_IMAGE') or None,
        )

    def archive(self) -> Path | None:
        if self.backup_file:
            return self.backup_file
        candidates = sorted(self.backup_dir.glob('clickstack-mongodb-*.archive.gz.age'),
                            key=lambda p: p.stat().st_mtime) if self.backup_dir.is_dir() else []
        return candidates[-1] if candidates else None


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
        report.line('Plan (restore-test-clickstack-mongodb):')
        report.line(f'  - create disposable namespace {ns} (label {RECOVERY_LABEL}=true) with a throwaway MongoDB pod')
        report.line(f'  - decrypt {archive or f"<latest backup in {settings.backup_dir}>"} with {settings.age_key} '
                    'and mongorestore into it')
        report.line(f'  - decrypt {ROOT / INGESTION_SECRET} into {ns} and compare with the restored team ingestion key')
        report.line(f'  - compare restored collection counts with the backup metadata, then delete {ns}')
        return 0

    if not archive or not archive.is_file():
        raise RecoveryError('No backup archive found (set BACKUP_FILE or BACKUP_DIR)')
    metadata_path = archive.with_name(archive.name.replace('.archive.gz.age', '.json'))
    if not metadata_path.is_file():
        raise RecoveryError(f'Missing backup metadata: {metadata_path}')
    if not os.access(settings.age_key, os.R_OK):
        raise RecoveryError(f'Cannot read age key: {settings.age_key}')
    metadata = json.loads(metadata_path.read_text())
    if sha256(archive) != metadata['sha256']:
        raise RecoveryError('Archive checksum does not match metadata')

    existing = runner.run(['kubectl', 'get', 'namespace', ns, '-o', 'json'], check=False)
    if existing.returncode == 0:
        labels = (json.loads(existing.stdout).get('metadata') or {}).get('labels') or {}
        if labels.get(RECOVERY_LABEL) != 'true':
            raise RecoveryError(f'Namespace {ns} exists and is not a recovery-test namespace; refusing to use it')
        raise RecoveryError(f'Namespace {ns} already exists; delete it or set RECOVERY_NAMESPACE')

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
        for value in (secret.get('data') or {}).values():
            runner.add_secret(value)
            with contextlib.suppress(binascii.Error, ValueError):
                runner.add_secret(base64.b64decode(value))
        secret['metadata']['namespace'] = ns
        runner.run(['kubectl', 'apply', '-f', '-'], input=yaml.safe_dump(secret), secret_output=True, mutating=True)
        report.ok(f'Restored hyperdx-secret from Git into {ns}')

        restored = json.loads(runner.output(['kubectl', '-n', ns, 'exec', 'mongodb', '--', 'mongosh', 'hyperdx',
                                             '--quiet', '--eval', COUNTS_SCRIPT]))
        if restored == metadata['collections']:
            report.ok('Restored collection counts match backup metadata')
        else:
            report.bad('Restored collection counts differ from backup metadata')

        team = runner.run(['kubectl', '-n', ns, 'exec', 'mongodb', '--', 'mongosh', 'hyperdx', '--quiet', '--eval',
                           TEAM_KEY_SCRIPT], check=False, secret_output=True)
        team_key = team.stdout.strip() if team.returncode == 0 else ''
        runner.add_secret(team_key)
        stored = runner.json(['kubectl', '-n', ns, 'get', 'secret', 'hyperdx-secret', '-o', 'json'],
                             secret_output=True)
        try:
            secret_key = base64.b64decode(((stored or {}).get('data') or {}).get('HYPERDX_API_KEY', '')).decode()
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
    except (RecoveryError, CommandError, KeyError, ValueError) as error:
        print(f'[ERROR] {runner.redact(str(error))}', file=sys.stderr)
        return 1
