"""
Prove an app's SQLite database restores from its backup (backup-sqlite, restore-sqlite):
  - a throwaway HelmRelease of app-template in namespace sqlite-restore-test (label
    platform.swhurl.com/sqlite-restore-test=true) holds a WAL-mode database with rows one, two
  - backup-sqlite's backup of it goes to a scratch directory (no S3 upload)
  - the live data then changes (row three added, row one deleted)
  - restore-sqlite brings back one, two; the replaced files are kept beside it and still read two, three
  - the HelmRelease is resumed and Ready, and the app runs again
Needs the age key (AGE_KEY_FILE, default ./age.agekey). Deletes the namespace and its volume afterwards
(KEEP=true leaves them for inspection).
"""
from __future__ import annotations

import datetime as dt
import os
import shutil
import tempfile
from pathlib import Path

from swhurl import ROOT, platform
from swhurl.livetests import LiveTest, Preflight, run_live_test
from swhurl.recovery import BackupSettings, RecoveryError, sops_recipient
from swhurl.sqlite_backup import IMAGE, backup_one
from swhurl.sqlite_restore import database, restore

NS = 'sqlite-restore-test'
LABEL = platform.label('sqlite-restore-test')
APP = 'notes'
DB = '/data/app.db'
ROWS = 'SELECT group_concat(body) FROM (SELECT body FROM notes ORDER BY id);'


def helmrelease() -> dict:
    repository, _, digest = IMAGE.partition('@')
    repository, _, tag = repository.rpartition(':')
    return {'apiVersion': 'helm.toolkit.fluxcd.io/v2', 'kind': 'HelmRelease',
            'metadata': {'name': APP, 'labels': {LABEL: 'true'}},
            'spec': {'interval': '30m', 'timeout': '3m',
                     'chart': {'spec': {'chart': 'app-template', 'version': '5.2.1',
                                        'sourceRef': {'kind': 'HelmRepository', 'name': 'bjw-s',
                                                      'namespace': 'flux-system'}}},
                     'values': {
                         'defaultPodOptions': {'automountServiceAccountToken': False, 'securityContext': {
                             'runAsNonRoot': True, 'runAsUser': 65532, 'runAsGroup': 65532, 'fsGroup': 65532,
                             'seccompProfile': {'type': 'RuntimeDefault'}}},
                         'controllers': {'main': {'replicas': 1, 'strategy': 'Recreate', 'containers': {'main': {
                             'image': {'repository': repository, 'tag': tag, 'digest': digest},
                             'command': ['sleep', '3600000'],
                             'env': {'DATABASE_PATH': DB},
                             'resources': {'requests': {'cpu': '10m', 'memory': '32Mi'}, 'limits': {'memory': '128Mi'}},
                             'securityContext': {'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True,
                                                 'capabilities': {'drop': ['ALL']}}}}}},
                         'persistence': {
                             'tmp': {'type': 'emptyDir', 'globalMounts': [{'path': '/tmp'}]},
                             'data': {'type': 'persistentVolumeClaim', 'storageClass': 'local-path',
                                      'accessMode': 'ReadWriteOnce', 'size': '64Mi',
                                      'globalMounts': [{'path': '/data'}]}}}}}


def sql(t: LiveTest, statement: str, path: str = DB) -> str:
    return t.kubectl('-n', NS, 'exec', f'deploy/{APP}', '--', 'sqlite3', path, statement).strip()


def body(t: LiveTest) -> None:
    age_key = Path(os.environ.get('AGE_KEY_FILE') or ROOT / platform.AGE_KEY)
    if not os.access(age_key, os.R_OK):
        raise Preflight(f'Cannot read the age key {age_key} (set AGE_KEY_FILE)')
    if t.get('get', 'namespace', NS):
        raise Preflight(f'Namespace {NS} already exists; delete it first')
    scratch = Path(tempfile.mkdtemp(prefix='sqlite-restore-test-'))

    def cleanup() -> None:
        shutil.rmtree(scratch, ignore_errors=True)
        if os.environ.get('KEEP') == 'true':
            t.report.info(f'KEEP=true: leaving namespace {NS}')
        elif t.delete_namespace_if_labelled(NS, LABEL, wait=True):
            t.report.info(f'Deleted namespace {NS} and its volume')
    t.cleanup.callback(cleanup)

    t.step('Throwaway app with a SQLite database')
    t.kubectl('create', 'namespace', NS)
    t.kubectl('label', 'namespace', NS, f'{LABEL}=true')
    t.apply(helmrelease(), namespace=NS)
    t.kubectl('-n', NS, 'wait', f'helmrelease/{APP}', '--for=condition=Ready', '--timeout=5m')
    t.kubectl('-n', NS, 'rollout', 'status', f'deploy/{APP}', '--timeout=3m')
    sql(t, "PRAGMA journal_mode=WAL; CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT); "
           "INSERT INTO notes(body) VALUES ('one'), ('two');")
    t.check(sql(t, ROWS) == 'one,two', 'database holds one, two', 'seeding the database failed')

    t.step('Backup, then change the live data')
    db = database(t.runner, NS)
    stamp = dt.datetime.now(dt.UTC).strftime('%Y%m%dT%H%M%SZ')
    archive = backup_one(t.runner, db, BackupSettings(backup_dir=scratch, recipient=sops_recipient()),
                         stamp=stamp, out=t.report.line)
    sql(t, "INSERT INTO notes(body) VALUES ('three'); DELETE FROM notes WHERE body = 'one';")
    t.check(sql(t, ROWS) == 'two,three', 'live data changed to two, three', 'changing the live data failed')

    t.step('Restore')
    try:
        aside = restore(t.runner, db, archive, age_key, stamp=stamp, out=t.report.line, sleep=t.sleep)
    except RecoveryError as error:
        t.report.bad(str(error))
        return
    t.check(sql(t, ROWS) == 'one,two', 'restored database holds one, two',
            f'restored database holds {sql(t, ROWS)!r}')
    t.check(sql(t, 'PRAGMA integrity_check;') == 'ok', 'restored database passes integrity_check',
            'restored database fails integrity_check')
    kept = sql(t, ROWS, f'{aside}/app.db')
    t.check(kept == 'two,three', f'replaced database kept in {aside}/ (two, three)',
            f'replaced database in {aside}/ holds {kept!r}')
    release = t.get('-n', NS, 'get', 'helmrelease', APP) or {}
    ready = [c for c in (release.get('status') or {}).get('conditions') or [] if c.get('type') == 'Ready']
    t.check(not (release.get('spec') or {}).get('suspend') and bool(ready) and ready[0].get('status') == 'True',
            'HelmRelease resumed and Ready', 'HelmRelease left suspended or not Ready')


def main(argv: list[str] | None = None, runner=None, report=None, sleep=None) -> int:
    kwargs = {'sleep': sleep} if sleep else {}
    return run_live_test(__doc__, body, passed='SQLite restore test passed.', runner=runner, report=report, **kwargs)
