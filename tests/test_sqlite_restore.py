"""App SQLite restore (restore-sqlite): backup checks, stop and freeze, swap, and cleanup on failure; offline."""
import json
import shutil
import tempfile
import unittest
from pathlib import Path

from swhurl.recovery import RecoveryError, sha256
from swhurl.run import FakeRunner, Result
from swhurl.sqlite_backup import Database
from swhurl.sqlite_restore import RestoreSettings, restore

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
                with self.assertRaisesRegex(RecoveryError, 'the database is unchanged'):
                    self.restore(cluster)
                joined = [' '.join(c) for c in cluster.runner.calls]
                self.assertFalse(any('mv "$1.restore"' in c for c in joined))
                self.assertTrue(any(c.endswith('rm -f /data/app.db.restore') for c in joined))
                self.assertTrue(any('--replicas=1' in c for c in joined), 'scaled back up')
                self.assertTrue(any(c.startswith('flux resume') for c in joined), 'resumed')
                self.assertTrue(any('delete pod sqlite-restore-notes' in c for c in joined))

    def test_pods_that_never_stop_abort_before_touching_the_claim(self):
        cluster = FakeCluster(pods_stop=False)
        with self.assertRaisesRegex(RecoveryError, 'did not stop'):
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
                with self.assertRaisesRegex(RecoveryError, message):
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

    def test_picks_the_newest_backup_unless_one_is_named(self):
        self.backup('20260930T033000Z')
        settings = RestoreSettings(backup_dir=self.dir, age_key=self.key)
        self.assertEqual(settings.archive('notes-staging'), self.archive)
        named = RestoreSettings(backup_dir=self.dir, age_key=self.key, backup_file=Path('/x/sqlite-1.db.age'))
        self.assertEqual(named.archive('notes-staging'), Path('/x/sqlite-1.db.age'))
        with self.assertRaisesRegex(RecoveryError, 'no backup in'):
            RestoreSettings(backup_dir=self.dir, age_key=self.key).archive('other-prod')


if __name__ == '__main__':
    unittest.main()
