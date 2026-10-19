"""App SQLite databases' backups (the ``--database sqlite`` capability): their names and freshness.

Each instance's backup CronJob (``apps/model/backup.py``, ``backup_job``) publishes
``sqlite-<UTC>.db.age`` and its metadata to SQLITE_S3_URI<namespace>/; ``sqlite_restore`` fetches one back.
"""
from __future__ import annotations

import datetime as dt
import os
from collections.abc import Mapping
from pathlib import Path

from swhurl import ROOT, backupset, platform, yamlio
from swhurl.apps.instance import Instance, instances
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
    """The instances whose release declares a SQLite database: each must have backups."""
    return [each for each in instances(root / 'apps')
            if (yamlio.load((each.folder(root) / 'release.yaml').read_text()) or {}).get('database') == 'sqlite']


def check_sqlite_backups(runner: Runner, report: Report, env: Mapping[str, str] | None = None,
                         now: dt.datetime | None = None, root: Path = ROOT) -> None:
    """Each instance with a SQLite database (its release.yaml in Git, not what runs) has a backup in S3 younger
    than BACKUP_MAX_AGE_HOURS (26)."""
    env = os.environ if env is None else env
    report.section('App SQLite backups')
    databases = expected(root)
    if not databases:
        report.ok('no app has a SQLite database')
        return
    for each in databases:
        backupset.check_backup_set(runner, report, backups_in(Path()), uri(each.namespace, env), env=env, now=now,
                                   what='SQLite', run='make backup-sqlite', prefix=f'{each.namespace}: ',
                                   hint=f'kubectl -n {each.namespace} get jobs -l platform.swhurl.com/backup=true')
