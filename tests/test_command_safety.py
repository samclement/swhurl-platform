"""The make interface, end to end through fake kubectl/flux executables on PATH.

Kept for what only a real process can show: exit codes, argument passing, dry
runs and refusals that make no cluster calls, and nothing secret on stdout or
stderr. Branch-by-branch behaviour is unit-tested with FakeRunner elsewhere.
"""
import base64
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from swhurl import ROOT

KEY = 'fixture-private-ingestion-token'
OTHER_KEY = 'fixture-other-private-token'


class CommandSafetyTests(unittest.TestCase):
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
import base64, json, os, sys
from pathlib import Path
with open(os.environ['CALLS'], 'a') as f:
    f.write(Path(sys.argv[0]).name + '\\n')
scenario = os.environ.get('SCENARIO', 'match')
argv = sys.argv[1:]
def emit(obj):
    print(json.dumps(obj))
if Path(sys.argv[0]).name == 'flux':
    sys.exit(1 if os.environ.get('FLUX_FAIL') else 0)
elif any(a.startswith('kustomizations') for a in argv):
    ready = 'False' if scenario == 'unready' else 'True'
    emit({'items': [{'metadata': {'name': 'homelab-clickstack'},
                     'status': {'conditions': [{'type': 'Ready', 'status': ready, 'message': 'fixture'}]}}]})
elif 'traefik' in argv:
    emit({'spec': {'template': {'spec': {'containers': [{'args': [
        '--entryPoints.web.http.redirections.entryPoint.to=:443',
        '--entryPoints.web.http.redirections.entryPoint.scheme=https']}]}}}})
elif 'clickhouse-client' in argv:
    print('9\\t9' if 'toIntervalDay(30)' in argv[-1] else '0')
elif 'pvc' in argv:
    emit({'metadata': {'annotations': {'helm.sh/resource-policy': 'keep'}}, 'spec': {'volumeName': 'pv-mongodb'}})
elif 'pv' in argv:
    emit({'spec': {'persistentVolumeReclaimPolicy': 'Retain'}})
elif 'exec' in argv:
    print(os.environ['KEY'])
    if scenario == 'mongo-failure':
        print('failed near ' + os.environ['KEY'], file=sys.stderr)
        sys.exit(1)
elif 'secret' in argv:
    value = os.environ['KEY'].encode()
    if scenario == 'mismatch': value = os.environ['OTHER_KEY'].encode()
    if scenario == 'newline': value += b'\\n'
    if scenario == 'double': value = base64.b64encode(value)
    encoded = base64.b64encode(value).decode()
    if scenario == 'invalid': encoded += '!'
    emit({'data': {} if scenario == 'missing' else {'HYPERDX_API_KEY': encoded}})
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

    def test_destroy_data_refuses_without_exact_confirmation(self):
        cases = (('pvc/ns/data', ''), ('pvc/ns/data', 'pvc/ns/other'), ('pv/name', 'pv/other'),
                 ('pvc/ns', 'pvc/ns'), ('secret/ns/x', 'secret/ns/x'), ('', ''))
        for target, confirm in cases:
            with self.subTest(target=target, confirm=confirm):
                result = subprocess.run(['make', 'destroy-data', f'TARGET={target}', f'CONFIRM={confirm}'],
                                        cwd=ROOT, env=self.env, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse(self.calls.exists(), 'A refused destroy-data called a cluster tool')

    def test_lifecycle_test_dry_run_never_calls_cluster_tools(self):
        result = subprocess.run(['make', 'lifecycle-test', 'DRY_RUN=true'], cwd=ROOT, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(self.calls.exists())

    def test_reloader_test_dry_run_never_calls_cluster_tools(self):
        result = subprocess.run(['make', 'reloader-test', 'DRY_RUN=true'], cwd=ROOT, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(self.calls.exists())

    def test_install_stops_at_the_first_failing_step(self):
        result = subprocess.run(['make', 'install'], cwd=ROOT, env=dict(self.env, FLUX_FAIL='1'),
                                capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = self.calls.read_text().split() if self.calls.exists() else []
        self.assertEqual(calls, ['flux'], 'verify-platform must not run after a failed reconcile')

    def test_install_plan_respects_feat_verify(self):
        for feat, steps in (('true', ['verify-config', 'flux-reconcile', 'verify-platform']),
                            ('false', ['flux-reconcile'])):
            with self.subTest(FEAT_VERIFY=feat):
                result = subprocess.run(['make', '--no-print-directory', 'install', 'DRY_RUN=true', f'FEAT_VERIFY={feat}'], cwd=ROOT,
                                        env=self.env, capture_output=True, text=True)
                self.assertEqual(result.stdout.splitlines(), ['Plan (install):', *[f'  - make {s}' for s in steps]])
                self.assertFalse(self.calls.exists())

    def test_verifier_checks_actual_bytes_and_never_prints_credentials(self):
        """End to end through real subprocesses and the fake kubectl (contract test)."""
        for scenario in ('match', 'double', 'mismatch', 'missing', 'invalid',
                         'newline', 'mongo-failure', 'unready'):
            with self.subTest(scenario=scenario):
                result = subprocess.run([sys.executable, '-m', 'swhurl', 'verify-platform'], cwd=ROOT,
                                        env=dict(self.env, SCENARIO=scenario, PYTHONPATH=str(ROOT / 'tools')),
                                        capture_output=True, text=True)
                output = result.stdout + result.stderr
                self.assertEqual(result.returncode == 0, scenario == 'match', output)
                for value in (KEY, OTHER_KEY):
                    self.assertNotIn(value, output)
                    self.assertNotIn(base64.b64encode(value.encode()).decode(), output)


if __name__ == '__main__':
    unittest.main()
