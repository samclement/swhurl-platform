"""App SQLite databases' backups (the ``--database sqlite`` capability): their names and freshness.

Each instance's backup CronJob (``apps/model/backup.py``, ``backup_job``) publishes
``sqlite-<UTC>.db.age`` and its metadata to SQLITE_S3_URI<namespace>/; ``sqlite_restore`` fetches one back.
"""
from __future__ import annotations

import datetime as dt
import json
import os
from collections.abc import Mapping
from pathlib import Path

from swhurl import ROOT, backupset, interface, platform
from swhurl.apps.instance import Instance
from swhurl.backupset import BackupSet
from swhurl.report import Report
from swhurl.run import Runner


def backups_in(directory: Path) -> BackupSet:
    """The database backups in one directory: ``sqlite-<UTC>.db.age``."""
    return BackupSet(directory, 'sqlite', '.db.age')


def uri(namespace: str, env: Mapping[str, str] | None = None) -> str:
    """Where one instance's backups are published; empty when SQLITE_S3_URI is set empty."""
    base = (os.environ if env is None else env).get('SQLITE_S3_URI', platform.SQLITE_S3_URI)
    return f'{base}{namespace}/' if base else ''


def expected(root: Path = ROOT) -> list[Instance]:
    """The instances whose release declares a SQLite database (v2's records): each must have backups."""
    found: set[Instance] = set()
    for record in sorted((root / 'v2-records').glob('*/*/release.json')):
        try:
            database = json.loads(record.read_text()).get('database')
        except (OSError, ValueError) as error:
            raise ValueError(f'{record.relative_to(root)}: cannot read it: {error}') from error
        if database == 'sqlite':
            found.add(Instance(record.parent.parent.name, record.parent.name))
    return sorted(found, key=str)


def check_sqlite_backups(runner: Runner, report: Report, env: Mapping[str, str] | None = None,
                         now: dt.datetime | None = None, root: Path = ROOT) -> None:
    """Each instance with a SQLite database (its release.yaml in Git, not what runs) has a backup in S3 younger
    than BACKUP_MAX_AGE_HOURS (26)."""
    env = os.environ if env is None else env
    report.section('App SQLite backups')
    try:
        databases = expected(root)
    except ValueError as error:
        report.bad(str(error))
        return
    if not databases:
        report.ok('no app has a SQLite database')
        return
    for each in databases:
        backupset.check_backup_set(runner, report, backups_in(Path()), uri(each.namespace, env), env=env, now=now,
                                   what='SQLite', run='make backup-sqlite', prefix=f'{each.namespace}: ',
                                   hint=f'kubectl -n {each.namespace} get jobs -l {interface.label("backup")}=true')
