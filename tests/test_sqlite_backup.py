"""App SQLite backups (backup-sqlite): discovery, the backup pod, encryption, retention and upload; offline."""
import datetime as dt
import io
import json
import shutil
import tempfile
import unittest
from pathlib import Path

from swhurl import retention, sqlite_backup
from swhurl.recovery import BackupSettings, RecoveryError
from swhurl.run import FakeRunner, Result

NOW = dt.datetime(2026, 10, 1, 3, 30, tzinfo=dt.UTC)


def deployment(ns='notes-staging', name='notes', path='/data/app.db', mount='/data', uid=65532):
    return {'metadata': {'namespace': ns, 'name': name}, 'spec': {'template': {'spec': {
        'securityContext': {'runAsUser': uid},
        'volumes': [{'name': 'data', 'persistentVolumeClaim': {'claimName': name}}, {'name': 'tmp', 'emptyDir': {}}],
        'containers': [{'name': 'main', 'env': [{'name': 'DATABASE_PATH', 'value': path}],
                        'volumeMounts': [{'name': 'data', 'mountPath': mount}, {'name': 'tmp', 'mountPath': '/tmp'}]}]}}}}


def plain(ns='web-prod', name='web'):
    return {'metadata': {'namespace': ns, 'name': name}, 'spec': {'template': {'spec': {
        'containers': [{'name': 'main', 'env': [{'name': 'OTHER', 'value': 'x'}]}]}}}}


class FakeCluster:
    """kubectl and age for one backup run; records what was applied and run."""

    def __init__(self, deployments, integrity='ok', remote=()):
        self.applied, self.uploaded = [], []
        self.runner = FakeRunner()
        self.runner.on('kubectl', 'get', 'deployments', stdout=json.dumps({'items': deployments}))
        self.runner.on('kubectl', 'apply', '-f', '-', handler=self.apply)
        self.runner.on('kubectl', '-n', 'notes-staging', 'wait', stdout='condition met\n')
        self.runner.on('kubectl', '-n', 'notes-staging', 'exec', handler=lambda args, _: self.exec(args, integrity))
        self.runner.on('kubectl', '-n', 'notes-staging', 'delete', 'pod', stdout='deleted\n')
        self.runner.on('age', handler=self.age)
        self.runner.on('aws', 's3api', 'list-objects-v2', stdout=json.dumps(list(remote)))
        self.runner.on('aws', 's3', 'cp', handler=lambda args, _: self.uploaded.append(args[-1]) or Result(args, 0))

    def apply(self, args, manifest):
        self.applied.append(json.loads(manifest))
        return Result(args, 0, 'pod/sqlite-backup-notes created\n')

    def exec(self, args, integrity):
        command = args[args.index('--') + 1:]
        if command[-1] == 'PRAGMA integrity_check;':
            return Result(args, 0, integrity + '\n')
        if command[-1].startswith('SELECT count(*)'):
            return Result(args, 0, '3\n')
        if command[0] == 'cat':
            return Result(args, 0, 'SQLite format 3\x00...')
        return Result(args, 0)

    def age(self, args, stream):
        Path(args[args.index('-o') + 1]).write_text(f'age({len(stream or "")})')
        return Result(args, 0)


class SqliteBackupTests(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir)
        self.settings = BackupSettings(backup_dir=self.dir, recipient='age1example', keep_daily=2, keep_weekly=1)

    def test_finds_databases_from_deployments(self):
        cluster = FakeCluster([deployment(), plain(), deployment('x-prod', 'x', path='/srv/db/main.db', mount='/srv/db', uid=101)])
        found = sqlite_backup.find(cluster.runner)
        self.assertEqual([(d.name, d.claim, d.in_pod, d.uid) for d in found],
                         [('notes-staging/notes', 'notes', '/data/app.db', 65532), ('x-prod/x', 'x', '/data/main.db', 101)])
        with self.assertRaisesRegex(RecoveryError, 'not on a mounted claim'):
            sqlite_backup.find(FakeCluster([deployment(path='/tmp/app.db')]).runner)

    def test_backs_up_through_a_pod_running_as_the_app_and_uploads(self):
        cluster = FakeCluster([deployment()])
        out = io.StringIO()
        (archive,) = sqlite_backup.backup(cluster.runner, self.settings, 's3://b/app-sqlite/', now=NOW, out=lambda s: out.write(s + '\n'))
        self.assertEqual(archive, self.dir / 'sqlite/notes-staging/sqlite-20261001T033000Z.db.age')
        (pod,) = cluster.applied
        spec = pod['spec']
        self.assertEqual((spec['securityContext']['runAsUser'], spec['volumes'][0]['persistentVolumeClaim']['claimName']), (65532, 'notes'))
        self.assertFalse(spec['automountServiceAccountToken'])
        self.assertEqual(spec['containers'][0]['image'], sqlite_backup.IMAGE)
        self.assertIn('@sha256:', sqlite_backup.IMAGE)
        meta = json.loads(archive.with_name('sqlite-20261001T033000Z.json').read_text())
        self.assertEqual((meta['source'], meta['tables'], meta['integrity_check']), ('notes-staging/notes', 3, 'ok'))
        self.assertTrue(any(c[:5] == ('kubectl', '-n', 'notes-staging', 'delete', 'pod') for c in cluster.runner.calls))
        self.assertEqual(sorted(cluster.uploaded), ['s3://b/app-sqlite/notes-staging/sqlite-20261001T033000Z.db.age',
                                                    's3://b/app-sqlite/notes-staging/sqlite-20261001T033000Z.json'])
        self.assertEqual(oct(archive.stat().st_mode & 0o777), '0o600', 'owner-only')

    def test_a_corrupt_copy_fails_writes_nothing_and_still_deletes_the_pod(self):
        cluster = FakeCluster([deployment()], integrity='*** in database main ***')
        with self.assertRaisesRegex(RecoveryError, 'backup failed for notes-staging/notes'):
            sqlite_backup.backup(cluster.runner, self.settings, '', now=NOW, out=lambda s: None)
        self.assertFalse(list(self.dir.rglob('*.age')))
        self.assertTrue(any(c[:5] == ('kubectl', '-n', 'notes-staging', 'delete', 'pod') for c in cluster.runner.calls))

    def test_dry_run_applies_nothing(self):
        cluster = FakeCluster([deployment()])
        cluster.runner.dry_run = True
        lines = []
        sqlite_backup.backup(cluster.runner, self.settings, 's3://b/app-sqlite/', now=NOW, out=lines.append)
        self.assertEqual(cluster.applied, [])
        self.assertTrue(any('UID 65532' in line for line in lines))

    def test_no_databases_is_fine(self):
        self.assertEqual(sqlite_backup.backup(FakeCluster([plain()]).runner, self.settings, '', now=NOW, out=lambda s: None), [])

    def test_retention_keeps_the_newest_per_day_and_week(self):
        directory = self.dir / 'sqlite/notes-staging'
        directory.mkdir(parents=True)
        for stamp in ('20261001T033000Z', '20260930T033000Z', '20260929T033000Z', '20260929T120000Z'):
            (directory / f'sqlite-{stamp}.db.age').write_text('x')
            (directory / f'sqlite-{stamp}.json').write_text('{}')
        retention.prune(directory, 2, 1, pattern=sqlite_backup.PATTERN, out=lambda s: None)
        self.assertEqual(sorted(p.name for p in directory.iterdir()),
                         ['sqlite-20260930T033000Z.db.age', 'sqlite-20260930T033000Z.json',
                          'sqlite-20261001T033000Z.db.age', 'sqlite-20261001T033000Z.json'])


if __name__ == '__main__':
    unittest.main()
