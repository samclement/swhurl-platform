"""The backup jobs' pod commands (backup-job-sqlite, backup-job-mongodb): a real SQLite copy, a fake age and S3; offline."""
import base64
import datetime as dt
import hashlib
import json
import shutil
import sqlite3
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from swhurl import ROOT, backup_job, platform, recovery, sqlite_backup, validate
from swhurl.apps import contract, policy
from swhurl.apps.instance import Instance
from swhurl.apps.model import backup
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


class RunNowTests(unittest.TestCase):
    """make backup-sqlite and make backup-mongodb start a Job from each CronJob and wait for it."""

    def cluster(self, outcomes):
        runner = FakeRunner()
        cronjobs = {'items': [{'metadata': {'namespace': ns, 'name': name}} for ns, name in
                              (('observability', 'backup-mongodb'), ('notes-prod', 'notes-backup'), ('a-staging', 'a-backup'))]}
        runner.on('kubectl', 'get', 'cronjobs', stdout=json.dumps(cronjobs))
        runner.on('kubectl', '-n', 'notes-prod', 'create', 'job', stdout='created')
        runner.on('kubectl', '-n', 'a-staging', 'create', 'job', stdout='created')
        runner.on('kubectl', '-n', 'observability', 'create', 'job', stdout='created')
        polls = {ns: iter(states) for ns, states in outcomes.items()}

        def job(args, _):
            state = next(polls[args[2]], None)
            conditions = [{'type': state, 'status': 'True'}] if state else []
            return Result(args, 0, json.dumps({'status': {'conditions': conditions}}))
        for ns in outcomes:
            runner.on('kubectl', '-n', ns, 'get', 'job', handler=job)
            runner.on('kubectl', '-n', ns, 'logs', stdout=f'[OK] {ns}: published\n')
        return runner

    def test_sqlite_runs_every_app_job_waits_and_fails_if_one_failed(self):
        runner = self.cluster({'a-staging': [None, 'Complete'], 'notes-prod': ['Failed']})
        with mock.patch('time.sleep'), mock.patch('sys.stdout'), mock.patch('sys.stderr') as err:
            self.assertEqual(backup_job.run_sqlite_main([], runner=runner), 1)
        created = [(call[2], call[5].split('-manual-')[0], call[6]) for call in runner.calls if 'create' in call]
        self.assertEqual(created, [('a-staging', 'a-backup', '--from=cronjob/a-backup'),
                                   ('notes-prod', 'notes-backup', '--from=cronjob/notes-backup')], 'not MongoDB')
        self.assertIn('notes-prod/notes-backup-manual', ''.join(call.args[0] for call in err.write.call_args_list))

    def test_mongodb_runs_only_its_job_and_dry_run_creates_nothing(self):
        runner = self.cluster({'observability': ['Complete']})
        out = []
        self.assertEqual(backup_job.run_now(runner, [backup_job.MONGODB], now=NOW, sleep=lambda s: None, out=out.append), [])
        self.assertIn('[OK] observability/backup-mongodb-manual-20261001t033000z', out)
        runner = self.cluster({})
        runner.dry_run = True
        out = []
        backup_job.run_now(runner, [backup_job.MONGODB], now=NOW, out=out.append)
        self.assertEqual([call for call in runner.calls if 'create' in call], [])
        self.assertIn('--from=cronjob/backup-mongodb', out[1])

    def test_a_job_that_never_finishes_is_reported(self):
        runner = self.cluster({'observability': []})
        out = []
        self.assertEqual(backup_job.run_now(runner, [backup_job.MONGODB], now=NOW, sleep=lambda s: None, out=out.append),
                         ['observability/backup-mongodb-manual-20261001t033000z'])
        self.assertIn(f'did not finish in {backup_job.WAIT_SECONDS} s', out[-1])


class RenderedJobTests(unittest.TestCase):
    """The CronJob and Secret the sqlite capability renders, and the checks that read them."""

    @classmethod
    def setUpClass(cls):
        from swhurl.apps.model import render, sync
        cls.world, cls.platform = sync.read(ROOT)
        cls.render = staticmethod(lambda app, env: render.instance(Instance(app, env), cls.world, cls.platform))

    def test_a_sqlite_instance_gets_a_nightly_job_as_its_app_with_the_key_substituted(self):
        files = self.render('demo-orders', 'prod')
        secret, job = files[Path('apps/demo-orders/prod') / contract.BACKUP_FILE]
        self.assertEqual(set(secret['stringData'].values()),
                         {'${BACKUP_AWS_ACCESS_KEY_ID}', '${BACKUP_AWS_SECRET_ACCESS_KEY}'})
        self.assertTrue(validate.substituted(secret))
        self.assertFalse(validate.substituted({**secret, 'stringData': {'AWS_ACCESS_KEY_ID': 'AKIA...'}}))
        spec = job['spec']
        self.assertEqual((spec['suspend'], spec['concurrencyPolicy'], spec['timeZone']), (False, 'Forbid', 'Etc/UTC'))
        pod = spec['jobTemplate']['spec']['template']['spec']
        uid = self.world[Instance('demo-orders', 'prod')].release.uid
        self.assertEqual((pod['securityContext']['runAsUser'], pod['automountServiceAccountToken']), (uid, False))
        self.assertEqual(pod['volumes'][0], {'name': 'data', 'persistentVolumeClaim': {'claimName': 'demo-orders'}})
        env = {var['name']: var.get('value') for var in pod['containers'][0]['env']}
        self.assertEqual(env['BACKUP_S3_URI'], contract.SQLITE_BACKUP_URI + 'demo-orders-prod/')
        self.assertTrue(env['HTTPS_PROXY'].startswith('http://egress.'), 'S3 is reached through the egress proxy')
        unit = files[Path('clusters/home/app-demo-orders-prod.yaml')][0]['spec']
        self.assertIn({'name': contract.BACKUP_UNIT}, unit['dependsOn'])
        self.assertEqual(unit['postBuild']['substituteFrom'], [{'kind': 'Secret', 'name': contract.BACKUP_WRITER, 'optional': False}])
        shop = self.render('demo-shop', 'prod')
        self.assertNotIn(Path('apps/demo-shop/prod') / contract.BACKUP_FILE, shop, 'no database, no job')
        self.assertNotIn('postBuild', shop[Path('clusters/home/app-demo-shop-prod.yaml')][0]['spec'])

    def test_schedules_are_stable_and_after_mongodb(self):
        times = {namespace: backup.schedule(namespace) for namespace in ('a-prod', 'b-staging', 'demo-orders-prod')}
        self.assertEqual(times, {namespace: backup.schedule(namespace) for namespace in times})
        for line in times.values():
            minute, hour = map(int, line.split()[:2])
            self.assertTrue((3, 35) <= (hour, minute) <= (4, 24), line)

    def test_a_backup_job_must_carry_no_token(self):
        files = self.render('demo-orders', 'prod')
        docs = [doc for documents in files.values() for doc in documents if doc.get('kind') in ('HelmRelease', 'CronJob')]
        job = next(doc for doc in docs if doc['kind'] == 'CronJob')
        self.assertEqual([v for v in policy.check_identity(docs, {contract.ENVIRONMENT: 'prod'}, '') if v[0] == 'call-tokens'], [])
        job['spec']['jobTemplate']['spec']['template']['spec']['volumes'].append(
            {'name': 'tokens', 'projected': {'sources': [{'serviceAccountToken': {'audience': 'x', 'path': 'x/token'}}]}})
        self.assertIn(('call-tokens', 'CronJob/demo-orders-backup is a backup job and carries no service-account token'),
                      policy.check_identity(docs, {contract.ENVIRONMENT: 'prod'}, ''))

    def test_the_mongodb_job_and_the_constants_agree_with_the_repository(self):
        import yaml
        (job,) = [doc for doc in yaml.safe_load_all((ROOT / 'platform/backup/mongodb.yaml').read_text()) if doc]
        container = job['spec']['jobTemplate']['spec']['template']['spec']['containers'][0]
        env = {var['name']: var.get('value') for var in container['env']}
        self.assertEqual((container['image'], env['AGE_RECIPIENT']), (contract.BACKUP_IMAGE, contract.BACKUP_RECIPIENT))
        self.assertEqual(recovery.sops_recipient(), contract.BACKUP_RECIPIENT)
        self.assertEqual(contract.SQLITE_BACKUP_URI, platform.SQLITE_S3_URI)
        self.assertEqual(env['BACKUP_S3_URI'], platform.BACKUP_S3_URI)


if __name__ == '__main__':
    unittest.main()
