"""App SQLite restore (restore-sqlite): backup checks, stop and freeze, swap, and cleanup on failure; offline."""
import json
import shutil
import tempfile
import unittest
from pathlib import Path

from swhurl import sqlite_backup
from swhurl.backupset import BackupError, sha256
from swhurl.run import FakeRunner, Result
from swhurl.sqlite_restore import IMAGE, Database, database, pod_manifest, restore

DB = Database('notes-staging', 'notes', 'notes', '/data', '/data/app.db', 65532)
NS = ('kubectl', '-n', 'notes-staging')


def deployment(release='notes'):
    labels = {'helm.toolkit.fluxcd.io/name': release} if release else {}
    return {'metadata': {'labels': labels},
            'spec': {'replicas': 1, 'selector': {'matchLabels': {'app.kubernetes.io/name': 'notes'}}}}


class FakeCluster:
    def __init__(self, integrity='ok', tables='3', release='notes', pods_stop=True, digest='ab' * 32):
        self.digest = digest
        self.applied, self.restored = [], []
        r = self.runner = FakeRunner()
        r.on(*NS, 'get', 'deployment', 'notes', stdout=json.dumps(deployment(release)))
        r.on(*NS, 'get', 'pods', stdout='' if pods_stop else 'pod/notes-1\n')
        r.on('flux', stdout='')
        r.on(*NS, 'scale', stdout='scaled\n')
        r.on('kubectl', 'apply', '-f', '-', handler=lambda args, m: self.applied.append(json.loads(m)) or Result(args, 0))
        r.on(*NS, 'wait', stdout='condition met\n')
        r.on('age', '-d', stdout='SQLite format 3\x00...')
        r.on(*NS, 'exec', '-i', handler=lambda args, data: self.restored.append(data) or Result(args, 0))
        r.on(*NS, 'exec', handler=lambda args, _: self.exec(args, integrity, tables))
        r.on(*NS, 'delete', 'pod', stdout='deleted\n')
        r.on(*NS, 'rollout', 'status', stdout='rolled out\n')

    def exec(self, args, integrity, tables):
        command = args[args.index('--') + 1:]
        if command[-1] == 'PRAGMA integrity_check;':
            return Result(args, 0, integrity + '\n')
        if command[-1].startswith('SELECT count(*)'):
            return Result(args, 0, tables + '\n')
        if command[0] in ('wc', 'sha256sum'):
            return Result(args, 0, ('19' if command[0] == 'wc' else self.digest) + f'  {command[-1]}\n')
        return Result(args, 0)

    def mutations(self):
        return [c for c in self.runner.calls
                if c[0] == 'flux' or {'scale', 'apply', 'delete', 'rm', 'sh'} & set(c)]


class SqliteRestoreTests(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir)
        self.key = self.dir / 'age.agekey'
        self.key.write_text('AGE-SECRET-KEY-TEST')
        self.archive = self.backup('20261002T033000Z')

    def backup(self, stamp, source='notes-staging/notes', checksum=None):
        directory = self.dir / 'sqlite/notes-staging'
        directory.mkdir(parents=True, exist_ok=True)
        archive = directory / f'sqlite-{stamp}.db.age'
        archive.write_text(f'encrypted {stamp}')
        (directory / f'sqlite-{stamp}.json').write_text(json.dumps(
            {'source': source, 'tables': 3, 'sha256': checksum or sha256(archive), 'plain_sha256': 'ab' * 32}))
        return archive

    def restore(self, cluster, archive=None):
        lines = []
        aside = restore(cluster.runner, DB, archive or self.archive, self.key, stamp='20261002T100000Z',
                        out=lines.append, sleep=lambda _: None)
        return aside, lines

    def test_stops_the_app_swaps_the_file_and_starts_it_again(self):
        cluster = FakeCluster()
        aside, _ = self.restore(cluster)
        self.assertEqual(aside, '/data/before-restore-20261002T100000Z')
        self.assertEqual(cluster.restored, ['SQLite format 3\x00...'], 'the decrypted copy streams into the pod')
        (pod,) = cluster.applied
        self.assertEqual((pod['metadata']['name'], pod['spec']['securityContext']['runAsUser']), ('sqlite-restore-notes', 65532))
        verbs = ('suspend', 'resume', 'scale', 'apply', 'delete', 'sh')
        steps = [next(v for v in verbs if v in c) for c in cluster.mutations()]
        self.assertEqual(steps, ['suspend', 'scale', 'apply', 'sh', 'sh', 'delete', 'scale', 'resume'],
                         'freeze, stop, restore pod, receive, swap; then cleanup in reverse')
        swap = next(c for c in cluster.runner.calls if 'mv "$1.restore" "$1"' in ' '.join(c))
        self.assertEqual(swap[-2:], ('/data/app.db', '/data/before-restore-20261002T100000Z'))
        self.assertIn('--replicas=1', cluster.runner.calls[-3])
        self.assertEqual(cluster.runner.calls[-1][3:5], ('rollout', 'status'))

    def test_a_copy_that_fails_its_checks_never_replaces_the_database(self):
        for integrity, tables, digest in (('*** corrupt ***', '3', 'ab' * 32), ('ok', '2', 'ab' * 32),
                                          ('ok', '3', 'cd' * 32)):
            with self.subTest(integrity=integrity, tables=tables, digest=digest[:2]):
                cluster = FakeCluster(integrity=integrity, tables=tables, digest=digest)
                with self.assertRaisesRegex(BackupError, 'the database is unchanged'):
                    self.restore(cluster)
                joined = [' '.join(c) for c in cluster.runner.calls]
                self.assertFalse(any('mv "$1.restore"' in c for c in joined))
                self.assertTrue(any(c.endswith('rm -f /data/app.db.restore') for c in joined))
                self.assertTrue(any('--replicas=1' in c for c in joined), 'scaled back up')
                self.assertTrue(any(c.startswith('flux resume') for c in joined), 'resumed')
                self.assertTrue(any('delete pod sqlite-restore-notes' in c for c in joined))

    def test_pods_that_never_stop_abort_before_touching_the_claim(self):
        cluster = FakeCluster(pods_stop=False)
        with self.assertRaisesRegex(BackupError, 'did not stop'):
            self.restore(cluster)
        self.assertEqual(cluster.applied, [])
        self.assertTrue(any(c[0] == 'flux' and c[1] == 'resume' for c in cluster.runner.calls))

    def test_refuses_a_damaged_or_foreign_backup_before_any_change(self):
        cases = ((self.backup('20261001T033000Z', checksum='0' * 64), 'checksum'),
                 (self.backup('20260930T033000Z', source='notes-prod/notes'), 'is a backup of notes-prod/notes'),
                 (self.dir / 'missing.db.age', 'does not exist'))
        for archive, message in cases:
            with self.subTest(message=message):
                cluster = FakeCluster()
                with self.assertRaisesRegex(BackupError, message):
                    self.restore(cluster, archive)
                self.assertEqual(cluster.mutations(), [])

    def test_without_a_helmrelease_only_the_deployment_is_scaled(self):
        cluster = FakeCluster(release=None)
        self.restore(cluster)
        self.assertFalse(any(c[0] == 'flux' for c in cluster.runner.calls))

    def test_dry_run_changes_nothing(self):
        cluster = FakeCluster()
        cluster.runner.dry_run = True
        _, lines = self.restore(cluster)
        self.assertEqual(cluster.mutations(), [])
        self.assertIn('Plan (restore-sqlite notes-staging/notes):', lines)

    def test_the_restore_pod_runs_as_the_app_on_its_claim_with_no_token(self):
        spec = pod_manifest(DB, 'sqlite-restore-notes')['spec']
        self.assertEqual((spec['securityContext']['runAsUser'], spec['volumes'][0]['persistentVolumeClaim']['claimName']),
                         (65532, 'notes'))
        self.assertFalse(spec['automountServiceAccountToken'])
        self.assertEqual(spec['containers'][0]['image'], IMAGE)
        self.assertIn('@sha256:', IMAGE)

    def test_backups_are_fetched_from_the_instances_prefix(self):
        self.assertEqual(sqlite_backup.uri('notes-staging', {'SQLITE_S3_URI': 's3://b/app-sqlite/'}),
                         's3://b/app-sqlite/notes-staging/')
        self.assertEqual(sqlite_backup.uri('notes-staging', {'SQLITE_S3_URI': ''}), '')


def app_deployment(name='notes', path='/data/app.db', mount='/data', uid=65532):
    return {'metadata': {'name': name}, 'spec': {'template': {'spec': {
        'securityContext': {'runAsUser': uid},
        'volumes': [{'name': 'data', 'persistentVolumeClaim': {'claimName': name}}, {'name': 'tmp', 'emptyDir': {}}],
        'containers': [{'name': 'main', 'env': [{'name': 'DATABASE_PATH', 'value': path}],
                        'volumeMounts': [{'name': 'data', 'mountPath': mount}, {'name': 'tmp', 'mountPath': '/tmp'}]}]}}}}


class DatabaseTests(unittest.TestCase):
    def found(self, *deployments, ns='notes-staging'):
        runner = FakeRunner().on('kubectl', '-n', ns, 'get', 'deployments', stdout=json.dumps({'items': list(deployments)}))
        return database(runner, ns)

    def test_the_namespaces_deployment_with_database_path(self):
        plain = {'metadata': {'name': 'web'}, 'spec': {'template': {'spec': {'containers': [{'name': 'main'}]}}}}
        self.assertEqual(self.found(app_deployment(), plain), DB)
        db = self.found(app_deployment('x', path='/srv/db/main.db', mount='/srv/db', uid=101), ns='x-prod')
        self.assertEqual((db.name, db.claim, db.in_pod, db.uid), ('x-prod/x', 'x', '/data/main.db', 101))

    def test_refuses_none_two_or_a_path_off_its_claim(self):
        for deployments, message in (((), 'found 0'), ((app_deployment(), app_deployment('other')), 'found 2'),
                                     ((app_deployment(path='/tmp/app.db'),), 'not on a mounted claim')):
            with self.subTest(message), self.assertRaisesRegex(BackupError, message):
                self.found(*deployments)

if __name__ == '__main__':
    unittest.main()
