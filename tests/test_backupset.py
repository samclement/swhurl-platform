"""A backup set (backupset): names, newest, the metadata file and fetching from S3; offline."""
import datetime as dt
import json
import shutil
import stat
import tempfile
import unittest
from pathlib import Path

from swhurl import ROOT, platform, recovery
from swhurl.backupset import BackupError, BackupSet, sha256
from swhurl.run import FakeRunner, Result


class PlatformLocationTests(unittest.TestCase):
    """The platform states each backup location once, where the cluster reads it; no environment overrides it."""
    def test_each_location_is_read_where_the_cluster_reads_it(self):
        self.assertEqual(platform.backup_s3_uri(), 's3://swhurl-platform-backups-110927251694/clickstack-mongodb/')
        self.assertEqual(platform.sqlite_s3_uri(), 's3://swhurl-platform-backups-110927251694/app-sqlite/')

    def test_both_locations_name_one_bucket(self):
        buckets = {uri.removeprefix('s3://').split('/')[0] for uri in (platform.backup_s3_uri(), platform.sqlite_s3_uri())}
        self.assertEqual(len(buckets), 1, f'{platform.BACKUP_JOB} and {platform.PROFILE} name different buckets')

    def test_a_missing_or_wrong_location_names_its_file(self):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root)
        for read, path in ((platform.backup_s3_uri, platform.BACKUP_JOB), (platform.sqlite_s3_uri, platform.PROFILE)):
            with self.subTest(str(path)), self.assertRaisesRegex(ValueError, f'^{path}: cannot read'):
                read(root)
        (root / platform.PROFILE).parent.mkdir(parents=True)
        profile = json.loads((ROOT / platform.PROFILE).read_text())
        profile['backup']['s3Uri'] = 's3://bucket/no-trailing-slash'
        (root / platform.PROFILE).write_text(json.dumps(profile))
        with self.assertRaisesRegex(ValueError, f'^{platform.PROFILE}: the backup location must be'):
            platform.sqlite_s3_uri(root)


class BackupSetTests(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir)
        # The two producers: every rule below holds for both.
        self.sets = {'mongodb': recovery.backups(self.dir / 'backups'),
                     'sqlite': BackupSet(self.dir / 'backups/sqlite/notes-staging', 'sqlite', '.db.age')}

    def each(self):
        for name, backups in self.sets.items():
            with self.subTest(name):
                yield backups

    def write(self, backups, *stamps):
        backups.directory.mkdir(parents=True, exist_ok=True)
        for taken in stamps:
            backups.path(taken).write_text(f'encrypted {taken}')
            backups.metadata_path(backups.path(taken)).write_text(
                json.dumps({'source': 'fixture', 'sha256': sha256(backups.path(taken))}))
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
            self.assertIsNone(backups.newest(['notes.txt', backups.metadata_path(half).name]), 'nor is a metadata file alone')

    def test_read_checks_the_archive_against_its_metadata(self):
        for backups in self.each():
            (archive,) = self.write(backups, '20261002T033000Z')
            self.assertEqual(backups.read(archive)['source'], 'fixture')
            archive.write_text('tampered')
            with self.assertRaisesRegex(BackupError, 'checksum does not match'):
                backups.read(archive)
            backups.metadata_path(archive).unlink()
            with self.assertRaisesRegex(BackupError, 'missing backup metadata'):
                backups.read(archive)
            archive.unlink()
            with self.assertRaisesRegex(BackupError, 'does not exist'):
                backups.read(archive)

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
