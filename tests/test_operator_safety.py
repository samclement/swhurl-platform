"""Exercise destructive-command guards, verifier credential boundaries and sign-in policy offline."""
import base64
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
KEY = 'fixture-private-ingestion-token'
OTHER_KEY = 'fixture-other-private-token'


class OperatorSafetyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.bin = Path(self.tmp.name)
        self.calls = self.bin / 'calls'
        self.env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}',
                        CALLS=str(self.calls), KEY=KEY, OTHER_KEY=OTHER_KEY)
        for name in ('kubectl', 'flux'):
            path = self.bin / name
            path.write_text('''#!/usr/bin/env python3
import base64, os, sys
from pathlib import Path
with open(os.environ['CALLS'], 'a') as f:
    f.write(Path(sys.argv[0]).name + '\\n')
scenario = os.environ.get('SCENARIO', 'match')
if Path(sys.argv[0]).name == 'flux':
    print('homelab-platform main@sha1:test False ' + ('False' if scenario == 'unready' else 'True') + ' reconciled')
elif 'clickhouse-client' in sys.argv:
    print('9\\t9' if 'toIntervalDay(30)' in sys.argv[-1] else '0')
elif 'pvc' in sys.argv:
    print('keep' if 'resource-policy' in sys.argv[-1] else 'pv-mongodb')
elif 'pv' in sys.argv:
    print('Retain')
elif 'exec' in sys.argv:
    print(os.environ['KEY'])
    if scenario == 'mongo-failure':
        sys.exit(1)
elif 'secret' in sys.argv:
    value = os.environ['KEY'].encode()
    if scenario == 'mismatch': value = os.environ['OTHER_KEY'].encode()
    if scenario == 'newline': value += b'\\n'
    if scenario == 'double': value = base64.b64encode(value)
    encoded = base64.b64encode(value).decode()
    if scenario == 'missing': encoded = ''
    if scenario == 'invalid': encoded += '!'
    print(encoded)
''')
            path.chmod(0o700)

    def test_destructive_targets_never_call_cluster_tools(self):
        for target in ('teardown', 'reinstall'):
            for dry in ('false', 'true'):
                with self.subTest(target=target, dry=dry):
                    result = subprocess.run(['make', target, f'DRY_RUN={dry}'], cwd=ROOT,
                                            env=self.env, capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, dry == 'true', result.stdout + result.stderr)
                    self.assertIn('disabled', result.stdout + result.stderr)
                    self.assertFalse(self.calls.exists(), 'A disabled target called a cluster tool')

    def test_recovery_dry_runs_never_call_cluster_tools(self):
        for target in ('backup-clickstack-mongodb', 'restore-test-clickstack-mongodb'):
            with self.subTest(target=target):
                result = subprocess.run(['make', target, 'DRY_RUN=true'], cwd=ROOT,
                                        env=dict(self.env, BACKUP_DIR=str(self.bin / 'backups')),
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('Plan (', result.stdout)
                self.assertFalse(self.calls.exists(), 'A recovery dry run called a cluster tool')
                self.assertFalse((self.bin / 'backups').exists(), 'A recovery dry run wrote files')

    def test_verifier_checks_actual_bytes_and_never_prints_credentials(self):
        for scenario in ('match', 'double', 'mismatch', 'missing', 'invalid',
                         'newline', 'mongo-failure', 'unready'):
            with self.subTest(scenario=scenario):
                result = subprocess.run(['bash', '-x', 'scripts/verify-platform.sh'], cwd=ROOT,
                                        env=dict(self.env, SCENARIO=scenario), capture_output=True, text=True)
                output = result.stdout + result.stderr
                self.assertEqual(result.returncode == 0, scenario == 'match', output)
                for value in (KEY, OTHER_KEY):
                    self.assertNotIn(value, output)
                    self.assertNotIn(base64.b64encode(value.encode()).decode(), output)

    def test_shared_sign_in_is_restricted_to_approved_emails(self):
        path = ROOT / 'platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml'
        values = yaml.safe_load(path.read_text())['spec']['values']
        self.assertNotIn('email-domain', values.get('extraArgs', {}))
        self.assertEqual(values['config'].get('configFile', '').strip(), 'email_domains = []',
                         'Chart default email_domains = ["*"] must stay overridden')
        emails = values.get('authenticatedEmailsFile', {})
        self.assertTrue(emails.get('enabled'))
        approved = [line.strip() for line in emails.get('restricted_access', '').splitlines() if line.strip()]
        self.assertTrue(approved, 'Approved email list is empty')
        for email in approved:
            self.assertRegex(email, r'^[^@\s*]+@[^@\s*]+\.[^@\s*]+$')


if __name__ == '__main__':
    unittest.main()
