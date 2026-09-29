"""backup-mongodb and live-test-restore-mongodb: every branch offline, with FakeRunner."""
import base64
import datetime as dt
import hashlib
import io
import json
import shutil
import stat
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

import yaml

from swhurl import recovery
from swhurl.report import Report
from swhurl.run import CommandError, FakeRunner, Result

DUMP = 'PLAINTEXT-MONGODUMP-fixture-contents'
TEAM_KEY = 'fixture-team-key-0000'
COUNTS = {'sources': 4, 'teams': 1, 'users': 1}
NOW = dt.datetime(2026, 9, 28, 12, 0, 0, tzinfo=dt.UTC)
URI = 'mongodb://hyperdx:fixture-mongo-password@clickstack-mongodb-svc:27017/hyperdx'


def age_encrypt(args, stdin):
    """Stand-in for `age -r R -o FILE`: writes a fake ciphertext, never the plaintext."""
    out = Path(args[args.index('-o') + 1])
    out.write_bytes(b'age-encryption.org/v1\n' + hashlib.sha256(stdin.encode()).digest())
    return Result(args, 0)


class BackupTests(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp()) / 'backups'
        self.addCleanup(shutil.rmtree, self.dir.parent)
        self.settings = recovery.BackupSettings(backup_dir=self.dir, recipient='age1fixture', prune=False)

    def runner(self, **overrides):
        answers = {'dump': Result((), 0, DUMP), 'age': age_encrypt,
                   'counts': Result((), 0, 'RESULT ' + json.dumps(COUNTS)), 'version': Result((), 0, 'db version v5.0.32\n')}
        answers.update(overrides)

        def respond(key):
            value = answers[key]
            return value if callable(value) else (lambda args, _in: Result(args, value.returncode, value.stdout,
                                                                           value.stderr))
        exec_mongo = ('kubectl', '-n', 'observability', 'exec', '-i', 'clickstack-mongodb-0', '-c', 'mongod', '--')
        self.dump_input = []

        def dump(args, stdin):
            self.dump_input.append(stdin)
            return respond('dump')(args, stdin)
        uri_secret = {'data': {'connectionString.standard': base64.b64encode(URI.encode()).decode()}}
        return (FakeRunner()
                .on('kubectl', '-n', 'observability', 'get', 'secret', 'clickstack-mongodb-hyperdx-hyperdx',
                    stdout=json.dumps(uri_secret))
                .on(*exec_mongo, 'mongodump', handler=dump)
                .on('age', handler=respond('age'))
                .on(*exec_mongo, 'mongosh', handler=respond('counts'))
                .on(*exec_mongo, 'mongod', handler=respond('version')))

    def backup(self, runner):
        lines = []
        archive = recovery.backup(runner, self.settings, now=NOW, out=lines.append)
        return archive, lines

    def test_writes_private_archive_and_metadata(self):
        archive, lines = self.backup(self.runner())
        self.assertEqual(archive.name, 'clickstack-mongodb-20260928T120000Z.archive.gz.age')
        metadata = json.loads(archive.with_name('clickstack-mongodb-20260928T120000Z.json').read_text())
        self.assertEqual(metadata, {
            'created': '20260928T120000Z', 'source': 'observability/clickstack-mongodb-0', 'database': 'hyperdx',
            'mongodb_version': '5.0.32', 'age_recipient': 'age1fixture',
            'sha256': hashlib.sha256(archive.read_bytes()).hexdigest(), 'collections': COUNTS})
        for path in (self.dir, archive, archive.with_name('clickstack-mongodb-20260928T120000Z.json')):
            self.assertEqual(stat.S_IMODE(path.stat().st_mode) & 0o077, 0, f'{path} is readable by others')
        self.assertEqual(lines[:2], [f'[OK] Encrypted backup: {archive}',
                                     f"[OK] Metadata: {archive.with_name(archive.name.replace('.archive.gz.age', '.json'))}"])

    def test_login_reaches_mongodump_only_on_stdin(self):
        runner = self.runner()
        self.backup(runner)
        self.assertEqual(self.dump_input, [f'uri: {json.dumps(URI)}\n'])
        for call in runner.calls:
            self.assertNotIn('fixture-mongo-password', ' '.join(call))

    def test_plaintext_never_reaches_disk_or_output(self):
        archive, lines = self.backup(self.runner())
        for path in self.dir.iterdir():
            self.assertNotIn(DUMP.encode(), path.read_bytes(), path.name)
        self.assertNotIn(DUMP, '\n'.join(lines))

    def test_failures_leave_no_partial_or_archive(self):
        cases = {
            'dump fails': {'dump': Result((), 1, '', 'connection refused')},
            'encryption fails': {'age': lambda args, _in: Result(args, 1, '', 'bad recipient')},
            'empty archive': {'age': lambda args, _in: (Path(args[args.index('-o') + 1]).write_bytes(b''),
                                                        Result(args, 0))[1]},
            'counts fail': {'counts': Result((), 1, '', 'mongosh error')},
            'version unreadable': {'version': Result((), 0, 'something else')},
        }
        for label, overrides in cases.items():
            with self.subTest(label):
                with self.assertRaises((CommandError, recovery.RecoveryError)):
                    self.backup(self.runner(**overrides))
                self.assertEqual([p.name for p in self.dir.iterdir()] if self.dir.exists() else [], [])

    def test_dry_run_plans_without_calls_or_files(self):
        runner = FakeRunner(dry_run=True)
        self.settings.prune = True
        _, lines = self.backup(runner)
        self.assertEqual(lines[0], 'Plan (backup-mongodb):')
        self.assertIn('  - encrypt to age recipient age1fixture', lines)
        self.assertTrue(lines[-1].startswith(f'  - prune {self.dir} to the newest backup'))
        self.assertEqual(runner.calls, [])
        self.assertFalse(self.dir.exists())

    def test_prunes_after_writing(self):
        self.dir.mkdir()
        for day in range(1, 11):
            stamp = f'202609{day:02d}T010000Z'
            (self.dir / f'clickstack-mongodb-{stamp}.archive.gz.age').write_bytes(b'x')
            (self.dir / f'clickstack-mongodb-{stamp}.json').write_text('{}')
        self.settings.prune = True
        with redirect_stdout(io.StringIO()):
            self.backup(self.runner())
        self.assertEqual(len(list(self.dir.glob('*.age'))), 7)

    def test_backup_uploads_only_what_the_bucket_lacks(self):
        self.settings.s3_uri = 's3://bucket/clickstack-mongodb/'
        self.dir.mkdir(parents=True)
        (self.dir / 'clickstack-mongodb-20260927T000000Z.archive.gz.age').write_bytes(b'old')
        (self.dir / 'clickstack-mongodb-20260927T000000Z.json').write_text('{}')
        (self.dir / 'notes.txt').write_text('not a backup')
        runner = self.runner().on('aws', 's3api', 'list-objects-v2', stdout=json.dumps(
            ['clickstack-mongodb/clickstack-mongodb-20260927T000000Z.archive.gz.age',
             'clickstack-mongodb/clickstack-mongodb-20260927T000000Z.json'])).on('aws', 's3', 'cp')
        archive, lines = self.backup(runner)
        copies = [c for c in runner.calls if c[:3] == ('aws', 's3', 'cp')]
        self.assertEqual([c[-1] for c in copies], [
            's3://bucket/clickstack-mongodb/clickstack-mongodb-20260928T120000Z.archive.gz.age',
            's3://bucket/clickstack-mongodb/clickstack-mongodb-20260928T120000Z.json'])
        self.assertEqual(copies[0][-2], str(archive))
        self.assertIn('[OK] Off-host: 2 file(s) uploaded to s3://bucket/clickstack-mongodb/, 2 already there', lines)

    def test_upload_failure_fails_the_backup_after_keeping_it_locally(self):
        self.settings.s3_uri = 's3://bucket/clickstack-mongodb/'
        runner = self.runner().on('aws', 's3api', 'list-objects-v2', stdout='null').on(
            'aws', 's3', 'cp', returncode=1, stderr='AccessDenied')
        with self.assertRaisesRegex(CommandError, 'AccessDenied'):
            self.backup(runner)
        self.assertTrue(any(p.name.endswith('.archive.gz.age') for p in self.dir.iterdir()))

    def test_empty_uri_skips_upload_and_bad_uri_is_refused(self):
        _, lines = self.backup(self.runner())
        self.assertIn('[INFO] BACKUP_S3_URI is empty: nothing copied off-host.', lines)
        for uri in ('bucket/prefix/', 's3://', 's3://bucket/prefix'):
            with self.subTest(uri=uri), self.assertRaises(recovery.RecoveryError):
                recovery.s3_location(uri)

    def test_dry_run_plans_the_upload_without_listing(self):
        self.settings.s3_uri = 's3://bucket/clickstack-mongodb/'
        runner = FakeRunner(dry_run=True)
        _, lines = self.backup(runner)
        self.assertIn('  - upload local backups not yet in s3://bucket/clickstack-mongodb/', lines)
        self.assertEqual(runner.calls, [])

    def test_recipient_comes_from_sops_config(self):
        self.assertTrue(recovery.sops_recipient().startswith('age1'))
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root)
        (root / '.sops.yaml').write_text('creation_rules:\n  - age: age1a\n  - age: age1b\n')
        with self.assertRaisesRegex(recovery.RecoveryError, 'exactly one age recipient'):
            recovery.sops_recipient(root)


class RestoreTestTests(unittest.TestCase):
    def setUp(self):
        self.dir = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.dir)
        self.archive = self.dir / 'clickstack-mongodb-20260928T120000Z.archive.gz.age'
        self.archive.write_bytes(b'ciphertext')
        self.metadata = {'sha256': hashlib.sha256(b'ciphertext').hexdigest(), 'mongodb_version': '5.0.32',
                         'collections': COUNTS}
        self.archive.with_name('clickstack-mongodb-20260928T120000Z.json').write_text(json.dumps(self.metadata))
        self.key = self.dir / 'age.agekey'
        self.key.write_text('AGE-SECRET-KEY-fixture')
        self.settings = recovery.RestoreSettings(backup_dir=self.dir, age_key=self.key)

    def runner(self, *, namespace=None, counts=COUNTS, team_key=TEAM_KEY, secret_key=TEAM_KEY, restore_rc=0):
        secret = {'apiVersion': 'v1', 'kind': 'Secret',
                  'metadata': {'name': 'clickstack-ingestion-key', 'namespace': 'logging'},
                  'stringData': {'CLICKSTACK_INGESTION_KEY': secret_key}}
        applied = {}

        def apply(args, stdin):
            doc = yaml.safe_load(stdin)
            if doc['kind'] == 'Secret':  # the API server stores stringData as data
                data = {k: base64.b64encode(str(v).encode()).decode() for k, v in doc.pop('stringData', {}).items()}
                applied['secret'] = {**doc, 'data': {**doc.get('data', {}), **data}}
            return Result(args, 0)

        def get_secret(args, _in):
            return Result(args, 0, json.dumps(applied['secret']))

        def mongosh(args, _in):
            script = args[-1]
            if 'countDocuments' in script:
                return Result(args, 0, json.dumps(counts))
            if 'apiKey' in script:
                return Result(args, 0 if team_key else 2, team_key + '\n' if team_key else '')
            return Result(args, 0, '1')

        ns = 'recovery-test'
        fake = FakeRunner()
        fake.on('kubectl', 'get', 'namespace', ns,
                handler=lambda args, _in: Result(args, 0, json.dumps(namespace)) if namespace
                else Result(args, 1, '', 'NotFound'))
        fake.on('kubectl', 'create', 'namespace').on('kubectl', 'label', 'namespace')
        fake.on('kubectl', '-n', ns, 'apply', handler=apply).on('kubectl', 'apply', handler=apply)
        fake.on('kubectl', '-n', ns, 'wait')
        fake.on('kubectl', '-n', ns, 'exec', '-i', 'mongodb', returncode=restore_rc, stderr='restore failed')
        fake.on('kubectl', '-n', ns, 'exec', 'mongodb', handler=mongosh)
        fake.on('age', '-d', stdout='PLAINTEXT-ARCHIVE')
        fake.on('sops', 'decrypt', stdout=yaml.safe_dump(secret))
        fake.on('kubectl', '-n', ns, 'get', 'secret', handler=get_secret)
        fake.on('kubectl', 'delete', 'namespace')
        return fake

    def run_test(self, runner, settings=None):
        out = io.StringIO()
        report = Report(out)
        try:
            code = recovery.restore_test(runner, settings or self.settings, report, sleep=lambda _s: None)
        except (recovery.RecoveryError, CommandError) as error:
            code, out = 1, io.StringIO(out.getvalue() + f'[ERROR] {runner.redact(str(error))}\n')
        return code, out.getvalue()

    def assertNoSecrets(self, text):
        for value in (TEAM_KEY, base64.b64encode(TEAM_KEY.encode()).decode(), 'PLAINTEXT-ARCHIVE',
                      'AGE-SECRET-KEY-fixture'):
            self.assertNotIn(value, text)

    def deleted(self, runner):
        return ('kubectl', 'delete', 'namespace', 'recovery-test', '--wait=false') in runner.calls

    def test_happy_path(self):
        runner = self.runner()
        code, text = self.run_test(runner)
        self.assertEqual(code, 0, text)
        self.assertEqual(text.splitlines(), [
            '[OK] Disposable MongoDB (mongo:5.0.32-focal) ready in recovery-test',
            f'[OK] Restored {self.archive.name}',
            '[OK] Restored clickstack-ingestion-key from Git into recovery-test',
            '[OK] Restored collection counts match backup metadata',
            '[OK] Restored team ingestion key matches the restored Git Secret',
            'Restore test passed.',
            '[INFO] Deleted disposable namespace recovery-test',
        ])
        self.assertTrue(self.deleted(runner))
        self.assertNoSecrets(text)

    def test_mismatches_fail_and_still_clean_up(self):
        for label, kwargs, message in (
                ('counts', {'counts': {**COUNTS, 'users': 2}}, 'counts differ'),
                ('key', {'secret_key': 'fixture-other-key'}, 'does not match'),
                ('no team key', {'team_key': ''}, 'does not match')):
            with self.subTest(label):
                runner = self.runner(**kwargs)
                code, text = self.run_test(runner)
                self.assertEqual(code, 1)
                self.assertIn(message, text)
                self.assertNotIn('Restore test passed.', text)
                self.assertTrue(self.deleted(runner))
                self.assertNoSecrets(text)

    def test_failed_restore_still_deletes_namespace(self):
        runner = self.runner(restore_rc=1)
        code, text = self.run_test(runner)
        self.assertEqual(code, 1)
        self.assertTrue(self.deleted(runner))
        self.assertNoSecrets(text)

    def test_keep_leaves_namespace(self):
        self.settings.keep = True
        runner = self.runner()
        code, text = self.run_test(runner)
        self.assertEqual(code, 0)
        self.assertFalse(self.deleted(runner))
        self.assertIn('[INFO] KEEP=true: leaving namespace recovery-test', text)

    def test_refuses_existing_namespaces_without_touching_them(self):
        for label, namespace, message in (
                ('foreign', {'metadata': {'labels': {}}}, 'is not a recovery-test namespace'),
                ('leftover', {'metadata': {'labels': {recovery.RECOVERY_LABEL: 'true'}}}, 'already exists')):
            with self.subTest(label):
                runner = self.runner(namespace=namespace)
                code, text = self.run_test(runner)
                self.assertEqual(code, 1)
                self.assertIn(message, text)
                self.assertEqual(runner.calls, [('kubectl', 'get', 'namespace', 'recovery-test', '-o', 'json')])

    def test_bad_inputs_fail_before_any_cluster_call(self):
        cases = {
            'checksum': lambda: self.archive.write_bytes(b'tampered'),
            'metadata missing': lambda: self.archive.with_suffix('').with_suffix('').with_suffix('.json').unlink(),
            'key unreadable': lambda: self.key.chmod(0),
            'no archive': lambda: self.archive.unlink(),
        }
        for label, breaker in cases.items():
            with self.subTest(label):
                self.setUp()
                breaker()
                runner = self.runner()
                code, _ = self.run_test(runner)
                self.assertEqual(code, 1)
                self.assertEqual(runner.calls, [])

    def test_dry_run_plans_only(self):
        runner = FakeRunner(dry_run=True)
        code, text = self.run_test(runner)
        self.assertEqual(code, 0)
        self.assertEqual(text.splitlines()[0], 'Plan (live-test-restore-mongodb):')
        self.assertIn(str(self.archive), text)
        self.assertEqual(runner.calls, [])

    def test_latest_archive_is_newest_by_mtime(self):
        older = self.dir / 'clickstack-mongodb-20990101T000000Z.archive.gz.age'
        older.write_bytes(b'x')
        import os
        os.utime(older, (1, 1))
        self.assertEqual(self.settings.archive(), self.archive)


if __name__ == '__main__':
    unittest.main()
