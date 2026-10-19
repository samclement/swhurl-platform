"""live-test-restore-mongodb: every branch offline, with FakeRunner."""
import base64
import hashlib
import io
import json
import shutil
import stat
import tempfile
import unittest
from pathlib import Path

import yaml

from swhurl import recovery
from swhurl.backupset import BackupError
from swhurl.report import Report
from swhurl.run import CommandError, FakeRunner, Result

TEAM_KEY = 'fixture-team-key-0000'
COUNTS = {'sources': 4, 'teams': 1, 'users': 1}
BUCKET = 's3://bucket/clickstack-mongodb/'


class SopsRecipientTests(unittest.TestCase):
    def test_recipient_comes_from_sops_config(self):
        self.assertTrue(recovery.sops_recipient().startswith('age1'))
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root)
        (root / '.sops.yaml').write_text('creation_rules:\n  - age: age1a\n  - age: age1b\n')
        with self.assertRaisesRegex(BackupError, 'exactly one age recipient'):
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
        self.settings = recovery.RestoreSettings(uri=BUCKET, age_key=self.key, backup_file=self.archive)

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

        def ping(args, _in):
            return Result(args, 0, '1')

        def exec_stdin(args, stdin):
            if 'mongorestore' in args:
                return Result(args, restore_rc, '', 'restore failed' if restore_rc else '')
            self.assertIn('mongodb://127.0.0.1:27017/hyperdx', stdin, 'scripts reach the throwaway pod over stdin')
            if 'countDocuments' in stdin:
                return Result(args, 0, 'RESULT ' + json.dumps(counts))
            if 'apiKey' in stdin:
                return Result(args, 0, 'RESULT ' + json.dumps({'keys': [team_key] if team_key else []}))
            raise AssertionError(stdin)

        ns = 'recovery-test'
        fake = FakeRunner()
        fake.on('kubectl', 'get', 'namespace', ns,
                handler=lambda args, _in: Result(args, 0, json.dumps(namespace)) if namespace
                else Result(args, 1, '', 'NotFound'))
        fake.on('kubectl', 'create', 'namespace').on('kubectl', 'label', 'namespace')
        fake.on('kubectl', '-n', ns, 'apply', handler=apply).on('kubectl', 'apply', handler=apply)
        fake.on('kubectl', '-n', ns, 'wait')
        fake.on('kubectl', '-n', ns, 'exec', '-i', 'mongodb', handler=exec_stdin)
        fake.on('kubectl', '-n', ns, 'exec', 'mongodb', handler=ping)
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
        except (BackupError, CommandError) as error:
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
        settings = recovery.RestoreSettings(uri=BUCKET, age_key=self.key)
        code, text = self.run_test(runner, settings)
        self.assertIn(f'the newest complete backup in {BUCKET}', text)
        self.assertEqual(runner.calls, [])

    def test_without_a_file_it_restores_the_newest_complete_backup_from_s3(self):
        older = 'clickstack-mongodb-20260101T000000Z.archive.gz.age'
        half = 'clickstack-mongodb-20261001T000000Z.archive.gz.age'  # newer, but its metadata never arrived
        listing = [older, older.replace('.archive.gz.age', '.json'), self.archive.name, self.archive.name.replace('.archive.gz.age', '.json'), half]
        runner = self.runner()
        runner.on('aws', 's3api', 'list-objects-v2', stdout=json.dumps([f'clickstack-mongodb/{name}' for name in listing]))

        def download(args, _in):
            target = Path(args[-1])
            self.assertEqual(stat.S_IMODE(target.parent.stat().st_mode), 0o700)
            shutil.copyfile(self.dir / target.name, target)
            return Result(args, 0)
        runner.on('aws', 's3', 'cp', handler=download)
        code, text = self.run_test(runner, recovery.RestoreSettings(uri=BUCKET, age_key=self.key))
        self.assertEqual(code, 0, text)
        self.assertEqual([c[-2] for c in runner.calls if c[:3] == ('aws', 's3', 'cp')],
                         [BUCKET + self.archive.name, BUCKET + self.archive.name.replace('.archive.gz.age', '.json')])
        self.assertIn(f'[OK] Restored {self.archive.name}', text)
        self.assertFalse(Path([c[-1] for c in runner.calls if c[:3] == ('aws', 's3', 'cp')][0]).exists(),
                         'the download is deleted afterwards')

if __name__ == '__main__':
    unittest.main()
