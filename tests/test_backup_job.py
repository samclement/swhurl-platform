"""The backup jobs' pod commands (backup-job-sqlite, backup-job-mongodb): a real SQLite copy, a fake age and S3; offline."""
import base64
import datetime as dt
import hashlib
import shutil
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from swhurl import backup_job, recovery, sqlite_backup
from swhurl.backupset import BackupError
from swhurl.run import FakeRunner, Result

NOW = dt.datetime(2026, 10, 1, 3, 30, tzinfo=dt.UTC)
URI = 's3://bucket/app-sqlite/notes-prod/'
STAMP = '20261001T033000Z'


class FakeJob:
    """age (a header, then the input) and S3 put-object for one job; records what was published, in order."""

    def __init__(self, fail_upload=False, short=False):
        self.published: list[tuple[str, str]] = []
        self.runner = FakeRunner()
        self.runner.on('age', handler=lambda args, stream: self.age(args, stream, short))
        self.runner.on('mongodump', stdout='dump bytes')
        self.runner.on('aws', 's3api', 'put-object', handler=lambda args, _: self.put(args, fail_upload))

    def age(self, args, stream, short):
        output = Path(args[args.index('-o') + 1])
        plain = stream.encode() if stream is not None else Path(args[-1]).read_bytes()
        output.write_bytes(b'' if short else b'age-encryption.org/v1\n' + plain)
        return Result(args, 0)

    def put(self, args, fail):
        if fail:
            return Result(args, 1, '', 'AccessDenied')
        body = Path(args[args.index('--body') + 1])
        expected = base64.b64encode(hashlib.sha256(body.read_bytes()).digest()).decode()
        assert args[args.index('--checksum-sha256') + 1] == expected, 'S3 is given the SHA-256 of what is sent'
        assert args[args.index('--if-none-match') + 1] == '*', 'never replaces a backup'
        self.published.append((args[args.index('--bucket') + 1], args[args.index('--key') + 1]))
        return Result(args, 0, '{}')


class SqliteJobTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        self.database = self.tmp / 'data/app.db'
        self.database.parent.mkdir()
        with sqlite3.connect(self.database) as db:
            db.execute('PRAGMA journal_mode=WAL')
            db.execute('CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)')
            db.execute('CREATE TABLE tags (name TEXT)')
            db.executemany('INSERT INTO notes (body) VALUES (?)', [(f'note {n}',) for n in range(100)])
        self.settings = backup_job.JobSettings(self.tmp / 'scratch', 'age1example', URI, 'notes-prod/notes')

    def test_copies_checks_encrypts_and_publishes_the_metadata_last(self):
        job = FakeJob()
        out = []
        archive = backup_job.backup_sqlite(job.runner, self.settings, self.database, now=NOW, out=out.append)
        self.assertEqual(job.published, [('bucket', f'app-sqlite/notes-prod/sqlite-{STAMP}.db.age'),
                                         ('bucket', f'app-sqlite/notes-prod/sqlite-{STAMP}.json')])
        self.assertFalse((self.settings.directory / backup_job.COPY).exists(), 'no plaintext copy is left')
        self.assertEqual(oct(archive.stat().st_mode & 0o777), '0o600')
        # What sqlite-restore reads: the set's checksum, the source, the table count and the plaintext's hash.
        backups = sqlite_backup.backups_in(self.settings.directory)
        metadata = backups.read(archive)
        plain = archive.read_bytes().removeprefix(b'age-encryption.org/v1\n')
        self.assertEqual((metadata['source'], metadata['tables'], metadata['integrity_check'], metadata['plain_sha256']),
                         ('notes-prod/notes', 2, 'ok', hashlib.sha256(plain).hexdigest()))
        restored = self.tmp / 'restored.db'
        restored.write_bytes(plain)
        with sqlite3.connect(restored) as db:
            self.assertEqual(db.execute('SELECT count(*) FROM notes').fetchone()[0], 100, 'committed WAL rows are in the copy')
        self.assertIn('[OK] Published', out[-1])

    def test_a_failed_archive_upload_never_publishes_the_metadata(self):
        job = FakeJob(fail_upload=True)
        with mock.patch.dict('os.environ', self.environment(), clear=True):
            self.assertEqual(backup_job.sqlite_main([], runner=job.runner), 1)
        (tried,) = [call for call in job.runner.calls if call[:3] == ('aws', 's3api', 'put-object')]
        self.assertTrue(tried[tried.index('--key') + 1].endswith('.db.age'), 'the archive was tried; the metadata never was')

    def test_a_database_that_is_not_one_or_is_missing_publishes_nothing(self):
        self.database.write_bytes(b'not a database' * 100)
        Path(f'{self.database}-wal').unlink(missing_ok=True)
        for env in (self.environment(), self.environment(DATABASE_PATH=str(self.tmp / 'missing.db'))):
            job = FakeJob()
            with mock.patch.dict('os.environ', env, clear=True):
                self.assertEqual(backup_job.sqlite_main([], runner=job.runner), 1)
            self.assertEqual(job.published, [])
            self.assertFalse(list(self.settings.directory.rglob('*.age')))
        self.assertFalse((self.tmp / 'missing.db').exists(), 'a missing database is never created')

    def test_a_short_archive_is_refused(self):
        job = FakeJob(short=True)
        with self.assertRaisesRegex(BackupError, 'smaller than the database'):
            backup_job.backup_sqlite(job.runner, self.settings, self.database, now=NOW, out=lambda line: None)
        self.assertEqual(job.published, [])
        self.assertFalse(list(self.settings.directory.rglob('*.age')))

    def test_settings_must_be_complete_and_dry_run_touches_nothing(self):
        job = FakeJob()
        for missing in ('AGE_RECIPIENT', 'BACKUP_S3_URI', 'BACKUP_SOURCE', 'DATABASE_PATH'):
            env = {k: v for k, v in self.environment().items() if k != missing}
            with self.subTest(missing=missing), mock.patch.dict('os.environ', env, clear=True), \
                    mock.patch('sys.stderr') as err:
                self.assertEqual(backup_job.sqlite_main([], runner=job.runner), 1)
                self.assertIn(missing, ''.join(call.args[0] for call in err.write.call_args_list))
        with mock.patch.dict('os.environ', self.environment(BACKUP_S3_URI='bucket/x'), clear=True), mock.patch('sys.stderr'):
            self.assertEqual(backup_job.sqlite_main([], runner=job.runner), 1)
        job.runner.dry_run = True
        out = []
        backup_job.backup_sqlite(job.runner, self.settings, self.database, now=NOW, out=out.append)
        self.assertEqual(job.runner.calls, [])
        self.assertIn('Plan (backup-job-sqlite)', out[0])

    def environment(self, **changes):
        return {'AGE_RECIPIENT': 'age1example', 'BACKUP_S3_URI': URI, 'BACKUP_SOURCE': 'notes-prod/notes',
                'BACKUP_DIR': str(self.settings.directory), 'DATABASE_PATH': str(self.database), **changes}


class MongoJobTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        self.settings = backup_job.JobSettings(self.tmp, 'age1example', 's3://bucket/clickstack-mongodb/',
                                               'observability/clickstack-mongodb')
        self.uri = 'mongodb://hyperdx:s3cret@clickstack-mongodb-svc.observability.svc:27017/hyperdx'

    def test_dumps_into_age_with_the_login_on_stdin_and_publishes_the_metadata_last(self):
        job = FakeJob()
        facts = mock.Mock(return_value=('7.0.14', {'alerts': 2, 'users': 1}))
        archive = backup_job.backup_mongodb(job.runner, self.settings, self.uri, 'hyperdx', facts=facts, now=NOW,
                                            out=lambda line: None)
        dump = next(call for call in job.runner.calls if call[0] == 'mongodump')
        self.assertNotIn('s3cret', ' '.join(dump), 'the login never reaches a command line')
        self.assertEqual(job.published, [('bucket', f'clickstack-mongodb/clickstack-mongodb-{STAMP}.archive.gz.age'),
                                         ('bucket', f'clickstack-mongodb/clickstack-mongodb-{STAMP}.json')])
        # What live-test-restore-mongodb reads: the server version (its image) and the counts it compares.
        metadata = recovery.backups(self.tmp).read(archive)
        self.assertEqual((metadata['mongodb_version'], metadata['collections'], metadata['database']),
                         ('7.0.14', {'alerts': 2, 'users': 1}, 'hyperdx'))
        facts.assert_called_once_with(self.uri, 'hyperdx')

    def test_an_empty_dump_or_unreadable_counts_publish_nothing(self):
        job = FakeJob(short=True)
        with self.assertRaisesRegex(BackupError, 'empty'):
            backup_job.backup_mongodb(job.runner, self.settings, self.uri, 'hyperdx', facts=mock.Mock(), now=NOW)
        job = FakeJob()
        unreadable = mock.Mock(side_effect=BackupError('could not read the collection counts: ServerSelectionTimeoutError'))
        with mock.patch.dict('os.environ', {'AGE_RECIPIENT': 'age1example', 'BACKUP_S3_URI': self.settings.uri,
                                            'BACKUP_SOURCE': self.settings.source, 'BACKUP_DIR': str(self.tmp),
                                            'MONGODB_URI': self.uri}, clear=True), \
                mock.patch.object(backup_job, 'mongo_facts', unreadable), mock.patch('sys.stderr') as err:
            self.assertEqual(backup_job.mongodb_main([], runner=job.runner), 1)
        self.assertEqual(job.published, [])
        self.assertNotIn('s3cret', ''.join(call.args[0] for call in err.write.call_args_list))


if __name__ == '__main__':
    unittest.main()
