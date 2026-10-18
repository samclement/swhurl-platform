"""Backups taken in the cluster, one database per job (docs/plan.md section 25).

    backup-job-sqlite    copy the app's SQLite database (DATABASE_PATH) with SQLite's online backup,
                         check it, encrypt it and publish it to BACKUP_S3_URI
    backup-job-mongodb   dump MONGO_DATABASE over MONGODB_URI, encrypt it and publish it

A job pod runs these, not the operator. The pod mounts what it backs up and has a private scratch
directory (BACKUP_DIR, an emptyDir), the age public key (AGE_RECIPIENT), the upload-only key
(AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY) and no Kubernetes token. BACKUP_SOURCE names the
database in the metadata (``<namespace>/<app>``). The archives and their metadata belong to the same
backup sets the host timer writes (``sqlite_backup.backups_in``, ``recovery.backups``), so a restore
reads either. A failure exits 1 before the metadata is published, so a partial backup never counts
(``backupset.publish``); retention is the bucket's lifecycle rule.
"""
from __future__ import annotations

import contextlib
import datetime as dt
import json
import os
import sqlite3
import sys
import urllib.parse
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from pathlib import Path

from swhurl import backupset, recovery, sqlite_backup
from swhurl.apps.contract import DATABASE_PATH_VARIABLE
from swhurl.backupset import BackupError
from swhurl.run import CommandError, Runner

SCRATCH = Path('/scratch')
COPY = 'copy.db'


@dataclass(frozen=True)
class JobSettings:
    directory: Path  # private scratch; nothing in it outlives the pod
    recipient: str
    uri: str  # s3://bucket/prefix/ of this one database
    source: str

    @classmethod
    def from_env(cls, env: Mapping[str, str]) -> JobSettings:
        missing = [name for name in ('AGE_RECIPIENT', 'BACKUP_S3_URI', 'BACKUP_SOURCE') if not env.get(name)]
        if missing:
            raise BackupError(f'{", ".join(missing)} must be set')
        backupset.s3_location(env['BACKUP_S3_URI'])
        return cls(Path(env.get('BACKUP_DIR') or SCRATCH), env['AGE_RECIPIENT'], env['BACKUP_S3_URI'], env['BACKUP_SOURCE'])


def copy_sqlite(source: Path, copy: Path) -> dict:
    """SQLite's online backup of ``source`` into ``copy`` (safe while the app writes), checked; its facts."""
    if not source.is_file():
        raise BackupError(f'{source} does not exist')
    # mode=rw: never create a database that is missing; a WAL database's readers need write access to its -shm.
    opened = sqlite3.connect(f'file:{urllib.parse.quote(str(source))}?mode=rw', uri=True)
    with contextlib.closing(opened) as database, contextlib.closing(sqlite3.connect(copy)) as copied:
        database.backup(copied)
        check = '\n'.join(row[0] for row in copied.execute('PRAGMA integrity_check'))
        tables = copied.execute("SELECT count(*) FROM sqlite_master WHERE type = 'table'").fetchone()[0]
    if check != 'ok':
        raise BackupError(f'the copy failed integrity_check: {check[:200]}')
    return {'tables': tables, 'integrity_check': check, 'bytes': copy.stat().st_size,
            'plain_sha256': backupset.sha256(copy)}


def backup_sqlite(runner: Runner, settings: JobSettings, database: Path, *, now: dt.datetime | None = None,
                  out: Callable[[str], None] = print) -> Path:
    """Copy, check, encrypt and publish one SQLite database; the plaintext copy is gone before publishing."""
    archives = sqlite_backup.backups_in(settings.directory)
    stamp = backupset.stamp(now)
    archive = archives.path(stamp)
    if runner.dry_run:
        out(f'Plan (backup-job-sqlite): copy {database} with SQLite\'s online backup, check it, encrypt it to '
            f'{settings.recipient} and publish {archive.name} and its metadata to {settings.uri}')
        return archive
    copy = settings.directory / COPY
    with backupset.private_files():
        settings.directory.mkdir(parents=True, exist_ok=True)
        try:
            facts = copy_sqlite(database, copy)
            with archives.writing(stamp) as partial:
                runner.run(['age', '-r', settings.recipient, '-o', partial, copy])
                # age adds a header and 16 bytes per 64 KiB: an archive smaller than the copy is cut short.
                if not partial.is_file() or partial.stat().st_size < max(facts['bytes'], 1):
                    raise BackupError(f'the encrypted copy is smaller than the database ({facts["bytes"]} bytes)')
        finally:
            copy.unlink(missing_ok=True)
    archives.record(archive, {'source': settings.source, 'path': str(database), **facts,
                              'age_recipient': settings.recipient})
    out(f'[OK] {settings.source}: {archive.name} ({facts["tables"]} tables, integrity ok)')
    backupset.publish(runner, archives, archive, settings.uri, out)
    return archive


def mongo_facts(uri: str, database: str) -> tuple[str, dict[str, int]]:
    """The server's version and each collection's document count, which a restore test compares."""
    from pymongo import MongoClient
    from pymongo.errors import PyMongoError
    try:
        with MongoClient(uri, serverSelectionTimeoutMS=30_000) as client:
            version = client.server_info()['version']
            db = client[database]
            return version, {name: db[name].count_documents({}) for name in sorted(db.list_collection_names())}
    except PyMongoError as error:
        raise BackupError(f'could not read the collection counts: {type(error).__name__}') from None


def backup_mongodb(runner: Runner, settings: JobSettings, uri: str, database: str, *,
                   facts: Callable[[str, str], tuple[str, dict[str, int]]] = mongo_facts,
                   now: dt.datetime | None = None, out: Callable[[str], None] = print) -> Path:
    """Dump one MongoDB database straight into age (no plaintext on disk), record its counts and publish it."""
    archives = recovery.backups(settings.directory)
    stamp = backupset.stamp(now)
    archive = archives.path(stamp)
    if runner.dry_run:
        out(f'Plan (backup-job-mongodb): mongodump --db {database}, encrypt it to {settings.recipient} and publish '
            f'{archive.name} and its metadata to {settings.uri}')
        return archive
    runner.add_secret(uri)
    with archives.writing(stamp) as partial:
        # The connection string reaches mongodump as a config file on stdin, never in a command line.
        runner.pipe(['mongodump', '--quiet', '--config=/dev/stdin', '--db', database, '--archive', '--gzip'],
                    ['age', '-r', settings.recipient, '-o', partial], input=f'uri: {json.dumps(uri)}\n')
        if not partial.is_file() or partial.stat().st_size == 0:
            raise BackupError('the backup archive is empty')
    version, counts = facts(uri, database)
    archives.record(archive, {'source': settings.source, 'database': database, 'mongodb_version': version,
                              'age_recipient': settings.recipient, 'collections': counts})
    out(f'[OK] {settings.source}: {archive.name} ({len(counts)} collections)')
    backupset.publish(runner, archives, archive, settings.uri, out)
    return archive


def _main(take: Callable[[Runner, JobSettings, Mapping[str, str]], Path], runner: Runner | None) -> int:
    try:
        take(runner or Runner.from_environment(), JobSettings.from_env(os.environ), os.environ)
    except (BackupError, CommandError, sqlite3.Error, ValueError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    return 0


def sqlite_main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    def take(runner: Runner, settings: JobSettings, env: Mapping[str, str]) -> Path:
        if not env.get(DATABASE_PATH_VARIABLE):
            raise BackupError(f'{DATABASE_PATH_VARIABLE} must be set')
        return backup_sqlite(runner, settings, Path(env[DATABASE_PATH_VARIABLE]))
    return _main(take, runner)


def mongodb_main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    def take(runner: Runner, settings: JobSettings, env: Mapping[str, str]) -> Path:
        if not env.get('MONGODB_URI'):
            raise BackupError('MONGODB_URI must be set')
        return backup_mongodb(runner, settings, env['MONGODB_URI'], env.get('MONGO_DATABASE') or 'hyperdx',
                              facts=mongo_facts)
    return _main(take, runner)


# Running them now (make backup-sqlite, make backup-mongodb) ----------------------------------------------

LABEL_SELECTOR = 'platform.swhurl.com/backup=true'
MONGODB = ('observability', 'backup-mongodb')
WAIT_SECONDS = 900  # the jobs' own activeDeadlineSeconds


def cronjobs(runner: Runner) -> list[tuple[str, str]]:
    """Every backup CronJob, as (namespace, name)."""
    items = runner.json(['kubectl', 'get', 'cronjobs', '--all-namespaces', '-l', LABEL_SELECTOR, '-o', 'json']).get('items') or []
    return sorted((item['metadata']['namespace'], item['metadata']['name']) for item in items)


def outcome(job: dict) -> str | None:
    """'Complete' or 'Failed' once the Job has finished; None while it runs."""
    for condition in (job.get('status') or {}).get('conditions') or []:
        if condition.get('type') in ('Complete', 'Failed') and condition.get('status') == 'True':
            return condition['type']
    return None


def run_now(runner: Runner, targets: list[tuple[str, str]], *, now: dt.datetime | None = None,
            sleep: Callable[[float], None] | None = None, out: Callable[[str], None] = print) -> list[str]:
    """Start a Job from each CronJob, wait for all of them and show each one's log; return the ones that failed."""
    import time
    sleep = sleep or time.sleep
    name = f'manual-{backupset.stamp(now).lower()}'
    if runner.dry_run:
        out('Plan (backup jobs):')
        for namespace, cronjob in targets:
            out(f'  - kubectl -n {namespace} create job {cronjob}-{name} --from=cronjob/{cronjob}, wait, show its log')
        return []
    for namespace, cronjob in targets:
        runner.run(['kubectl', '-n', namespace, 'create', 'job', f'{cronjob}-{name}'[:63], f'--from=cronjob/{cronjob}'],
                   mutating=True)
    failed, waiting, waited = [], list(targets), 0.0
    while waiting:
        for namespace, cronjob in list(waiting):
            job = f'{cronjob}-{name}'[:63]
            state = outcome(runner.json(['kubectl', '-n', namespace, 'get', 'job', job, '-o', 'json']))
            if state is None and waited < WAIT_SECONDS:
                continue
            waiting.remove((namespace, cronjob))
            log = runner.output(['kubectl', '-n', namespace, 'logs', f'job/{job}', '--tail=20'], check=False).strip()
            for line in log.splitlines():
                out(f'  {namespace}: {line}')
            if state == 'Complete':
                out(f'[OK] {namespace}/{job}')
            else:
                failed.append(f'{namespace}/{job}')
                out(f'[ERROR] {namespace}/{job} {"failed" if state else f"did not finish in {WAIT_SECONDS} s"}')
        if waiting:
            sleep(5)
            waited += 5
    return failed


def _run_main(pick: Callable[[Runner], list[tuple[str, str]]], what: str, runner: Runner | None) -> int:
    runner = runner or Runner.from_environment()
    try:
        targets = pick(runner)
        if not targets:
            print(f'[INFO] No {what} backup job: nothing to run.')
            return 0
        failed = run_now(runner, targets)
    except CommandError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    if failed:
        print(f'[ERROR] backup failed for {", ".join(failed)}', file=sys.stderr)
        return 1
    return 0


def run_sqlite_main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    """make backup-sqlite: every app SQLite database's backup job, now."""
    return _run_main(lambda runner: [each for each in cronjobs(runner) if each != MONGODB], 'app SQLite', runner)


def run_mongodb_main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    """make backup-mongodb: ClickStack MongoDB's backup job, now."""
    return _run_main(lambda runner: [MONGODB], 'MongoDB', runner)
