"""
Prove an app's SQLite database restores from its backup job's archive in S3 (restore-sqlite):
  - a throwaway HelmRelease of app-template in namespace sqlite-restore-test (label
    platform.swhurl.com/sqlite-restore-test=true) holds a WAL-mode database with rows one, two
  - the same backup job an app gets (suspended CronJob, started once as make backup-sqlite does) publishes
    it to SQLITE_S3_URI sqlite-restore-test/ with the upload-only key from flux-system/backup-writer
  - the live data then changes (row three added, row one deleted)
  - restore-sqlite downloads the backup from S3 and brings back one, two; the replaced files are kept beside
    it and still read two, three
  - the HelmRelease is resumed and Ready, and the app runs again
Needs the age key (AGE_KEY_FILE, default ./age.agekey) and S3 read and delete access through the AWS CLI's
own credentials. Deletes the namespace, its volume and the test's S3 prefix afterwards (KEEP=true leaves
them for inspection).
"""
from __future__ import annotations

import base64
import json
import os
import tempfile
from pathlib import Path

from swhurl import backup_job, backupset, interface
from swhurl.apps.contract import BACKUP_WRITER
from swhurl.apps.model import backup
from swhurl.backupset import BackupError
from swhurl.livetests import LiveTest, Preflight, run_live_test
from swhurl.sqlite_backup import backups_in, uri
from swhurl.sqlite_restore import IMAGE, database, restore

NS = 'sqlite-restore-test'
LABEL = interface.label('sqlite-restore-test')
APP = 'notes'
DB = interface.SQLITE_PATH
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
                             'env': {interface.DATABASE_PATH_VARIABLE: DB},
                             'resources': {'requests': {'cpu': '10m', 'memory': '32Mi'}, 'limits': {'memory': '128Mi'}},
                             'securityContext': {'allowPrivilegeEscalation': False, 'readOnlyRootFilesystem': True,
                                                 'capabilities': {'drop': ['ALL']}}}}}},
                         'persistence': {
                             'tmp': {'type': 'emptyDir', 'globalMounts': [{'path': '/tmp'}]},
                             'data': {'type': 'persistentVolumeClaim', 'storageClass': 'local-path',
                                      'accessMode': 'ReadWriteOnce', 'size': '64Mi',
                                      'globalMounts': [{'path': interface.DATA_MOUNT}]}}}}}


def sql(t: LiveTest, statement: str, path: str = DB) -> str:
    return t.kubectl('-n', NS, 'exec', f'deploy/{APP}', '--', 'sqlite3', path, statement).strip()


def body(t: LiveTest) -> None:
    age_key = backupset.age_key(os.environ)
    if not os.access(age_key, os.R_OK):
        raise Preflight(f'Cannot read the age key {age_key} (set AGE_KEY_FILE)')
    if t.get('get', 'namespace', NS):
        raise Preflight(f'Namespace {NS} already exists; delete it first')
    published = uri(NS)
    scratch = t.cleanup.enter_context(tempfile.TemporaryDirectory(prefix='sqlite-restore-test-'))

    def cleanup() -> None:
        if os.environ.get('KEEP') == 'true':
            t.report.info(f'KEEP=true: leaving namespace {NS} and {published}')
            return
        if t.delete_namespace_if_labelled(NS, LABEL, wait=True):
            t.report.info(f'Deleted namespace {NS} and its volume')
        t.runner.run(['aws', 's3', 'rm', '--recursive', '--only-show-errors', published], check=False)
        t.report.info(f'Deleted the test backups in {published}')
    t.cleanup.callback(cleanup)

    t.step('Throwaway app with a SQLite database')
    t.apply({'apiVersion': 'v1', 'kind': 'Namespace', 'metadata': {'name': NS, 'labels': {LABEL: 'true'}}})  # labelled from the start, so cleanup can always delete it
    t.apply(helmrelease(), namespace=NS)
    t.kubectl('-n', NS, 'wait', f'helmrelease/{APP}', '--for=condition=Ready', '--timeout=5m')
    t.kubectl('-n', NS, 'rollout', 'status', f'deploy/{APP}', '--timeout=3m')
    sql(t, "PRAGMA journal_mode=WAL; CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT); "
           "INSERT INTO notes(body) VALUES ('one'), ('two');")
    t.check(sql(t, ROWS) == 'one,two', 'database holds one, two', 'seeding the database failed')

    t.step('Backup job, then change the live data')
    db = database(t.runner, NS)
    writer = (t.runner.json(['kubectl', '-n', 'flux-system', 'get', 'secret', BACKUP_WRITER, '-o', 'json'],
                            secret_output=True) or {}).get('data') or {}
    key = tuple(base64.b64decode(writer.get(name, '')).decode()
                for name in ('BACKUP_AWS_ACCESS_KEY_ID', 'BACKUP_AWS_SECRET_ACCESS_KEY'))
    for value in key:
        t.runner.add_secret(value)
    if not all(key):
        raise Preflight(f'flux-system/{BACKUP_WRITER} has no upload key')
    secret, cronjob = backup.manifests(APP, NS, db.uid, claim=db.claim, proxy={}, path=DB, key=key)
    cronjob['spec']['suspend'] = True  # started once below, never on its schedule
    t.runner.run(['kubectl', '-n', NS, 'apply', '-f', '-'], input=json.dumps(secret), secret_output=True)
    t.apply(cronjob, namespace=NS)
    failed = backup_job.run_now(t.runner, [(NS, cronjob['metadata']['name'])], sleep=t.sleep, out=t.report.line)
    if not t.check(not failed, f'backup job published to {published}', 'the backup job failed'):
        return
    archive = backups_in(Path(scratch)).fetch(t.runner, published)
    stamp = backupset.stamp()
    sql(t, "INSERT INTO notes(body) VALUES ('three'); DELETE FROM notes WHERE body = 'one';")
    t.check(sql(t, ROWS) == 'two,three', 'live data changed to two, three', 'changing the live data failed')

    t.step('Restore')
    try:
        aside = restore(t.runner, db, archive, age_key, stamp=stamp, out=t.report.line, sleep=t.sleep)
    except BackupError as error:
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
