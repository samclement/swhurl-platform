"""A backup set (backupset): names, newest, age, the metadata file, writing and fetching from S3; offline."""
import datetime as dt
import json
import shutil
import stat
import tempfile
import unittest
from pathlib import Path

from swhurl import recovery, sqlite_backup
from swhurl.backupset import BackupError, BackupSet, stamp
from swhurl.run import FakeRunner, Result

NOW = dt.datetime(2026, 10, 2, 12, 0, 0, tzinfo=dt.UTC)


class BackupSetTests(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir)
        # The two producers: every rule below holds for both.
        self.sets = {'mongodb': recovery.backups(self.dir / 'backups'),
                     'sqlite': sqlite_backup.backups_in(self.dir / 'backups/sqlite/notes-staging')}

    def each(self):
        for name, backups in self.sets.items():
            with self.subTest(name):
                yield backups

    def write(self, backups, *stamps):
        backups.directory.mkdir(parents=True, exist_ok=True)
        for taken in stamps:
            backups.path(taken).write_text(f'encrypted {taken}')
            backups.record(backups.path(taken), {'source': 'fixture'})
        return [backups.path(taken) for taken in stamps]

    def test_names(self):
        mongodb, sqlite = self.sets['mongodb'], self.sets['sqlite']
        self.assertEqual(mongodb.path('20261002T033000Z'),
                         self.dir / 'backups/clickstack-mongodb-20261002T033000Z.archive.gz.age')
        self.assertEqual(sqlite.path('20261002T033000Z'),
                         self.dir / 'backups/sqlite/notes-staging/sqlite-20261002T033000Z.db.age')
        self.assertEqual(mongodb.metadata_path(mongodb.path('20261002T033000Z')).name,
                         'clickstack-mongodb-20261002T033000Z.json')
        self.assertEqual(sqlite.metadata_path(sqlite.path('20261002T033000Z')).name, 'sqlite-20261002T033000Z.json')
        self.assertEqual(stamp(NOW), '20261002T120000Z')

    def test_only_an_archive_name_has_a_time(self):
        for backups in self.each():
            archive = backups.path('20261002T033000Z')
            self.assertEqual(backups.taken(archive.name), dt.datetime(2026, 10, 2, 3, 30, tzinfo=dt.UTC))
            for other in (backups.metadata_path(archive).name, archive.name + '.partial', 'x' + archive.name,
                          'notes.txt'):
                self.assertIsNone(backups.taken(other), other)

    def test_newest_is_the_newest_complete_backup_by_stamp(self):
        for backups in self.each():
            old, new, half = (backups.path(t) for t in ('20260930T033000Z', '20261001T033000Z', '20261002T033000Z'))
            names = [half.name, old.name, backups.metadata_path(old).name, new.name, backups.metadata_path(new).name,
                     backups.metadata_path(half).name.replace('.json', '.txt')]
            self.assertEqual(backups.newest(names), new.name, 'an archive without its metadata is no backup')
            self.assertIsNone(backups.newest([]))
            self.assertIsNone(backups.age(NOW, ['notes.txt']))

    def test_age_of_a_listing(self):
        for backups in self.each():
            done, half = backups.path('20261001T120000Z'), backups.path('20261002T115900Z')
            remote = [done.name, backups.metadata_path(done).name, half.name]
            self.assertEqual(backups.age(NOW, remote), dt.timedelta(days=1), 'an archive without its metadata is no backup')
            self.assertIsNone(backups.age(NOW, [backups.metadata_path(half).name]), 'nor is a metadata file alone')

    def test_writing_keeps_the_archive_only_when_the_block_succeeds(self):
        for backups in self.each():
            with backups.writing('20261002T033000Z') as partial:
                self.assertFalse(backups.path('20261002T033000Z').exists())
                partial.write_text('encrypted')
            archive = backups.path('20261002T033000Z')
            self.assertEqual(archive.read_text(), 'encrypted')
            self.assertEqual(stat.S_IMODE(archive.stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(backups.directory.stat().st_mode), 0o700)
            with self.assertRaises(BackupError), backups.writing('20261003T033000Z') as partial:
                partial.write_text('half')
                raise BackupError('the stream was cut short')
            self.assertEqual([p.name for p in backups.directory.iterdir()], [archive.name])

    def test_metadata_records_the_stamp_and_checksum_and_read_checks_them(self):
        for backups in self.each():
            (archive,) = self.write(backups, '20261002T033000Z')
            metadata = backups.read(archive)
            self.assertEqual(metadata['created'], '20261002T033000Z')
            self.assertEqual(metadata['source'], 'fixture')
            self.assertEqual(stat.S_IMODE(backups.metadata_path(archive).stat().st_mode), 0o600)
            archive.write_text('tampered')
            with self.assertRaisesRegex(BackupError, 'checksum does not match'):
                backups.read(archive)
            backups.metadata_path(archive).unlink()
            with self.assertRaisesRegex(BackupError, 'missing backup metadata'):
                backups.read(archive)
            archive.unlink()
            with self.assertRaisesRegex(BackupError, 'does not exist'):
                backups.read(archive)
            with self.assertRaisesRegex(BackupError, 'not named like a backup'):
                backups.record(backups.directory / 'notes.txt', {})

    def test_fetch_downloads_the_newest_complete_backup_privately(self):
        for backups in self.each():
            source = self.dir / 'bucket'
            source.mkdir(exist_ok=True)
            old, new = (backups.path(t).name for t in ('20260930T033000Z', '20261002T033000Z'))
            meta = backups.metadata_path(backups.directory / old).name
            for name in (old, meta, new):
                (source / name).write_text(name)
            runner = FakeRunner().on('aws', 's3api', stdout=json.dumps([f'set/{n}' for n in (old, meta, new)]))
            runner.on('aws', 's3', 'cp', handler=lambda args, _in, source=source: (
                shutil.copyfile(source / Path(args[-1]).name, args[-1]), Result(args, 0))[1])
            target = BackupSet(self.dir / 'fetched' / backups.prefix, backups.prefix, backups.suffix)
            archive = target.fetch(runner, 's3://bucket/set/')
            self.assertEqual(archive, target.directory / old, 'the newer archive has no metadata yet')
            self.assertEqual(stat.S_IMODE(target.directory.stat().st_mode), 0o700)
            self.assertEqual([c[-2] for c in runner.calls if c[:3] == ('aws', 's3', 'cp')],
                             [f's3://bucket/set/{old}', f's3://bucket/set/{meta}'])
            with self.assertRaisesRegex(BackupError, 'no complete backup in s3://bucket/other/'):
                target.fetch(FakeRunner().on('aws', 's3api', stdout='[]'), 's3://bucket/other/')

if __name__ == '__main__':
    unittest.main()
