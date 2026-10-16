"""The make interface, end to end through fake kubectl/flux executables on PATH.

Kept for what only a real process can show: exit codes, argument passing, dry
runs and refusals that make no cluster calls, and nothing secret on stdout or
stderr. Branch-by-branch behaviour is unit-tested with FakeRunner elsewhere.
"""
import base64
import datetime as dt
import json
import os
import subprocess
import sys
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from swhurl import ROOT, commands, flux, platform
from swhurl.apps import calling, channels, contract

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
        backups = self.bin / 'fresh-backups'
        backups.mkdir()
        stamp = dt.datetime.now(dt.UTC).strftime('%Y%m%dT%H%M%SZ')
        (backups / f'clickstack-mongodb-{stamp}.archive.gz.age').write_bytes(b'x')
        self.env.update(BACKUP_DIR=str(backups), BACKUP_S3_URI='s3://bucket/clickstack-mongodb/', STAMP=stamp)
        self.env['WEBHOOK_HOST'] = f'flux-webhook.{platform.base_domain()}'
        self.env.update(FLUX_VERSION=f'v{flux.pinned_version()}',
                        FLUX_ARGS=' '.join(flux.required_args()['kustomize-controller']))
        # The console image tag is this checkout's commit, so the real git diff finds no change.
        self.env['CONSOLE_TAG'] = subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=ROOT, capture_output=True,
                                                 text=True, check=True).stdout.strip()
        self.env['INSTANCES'] = json.dumps([(p.parent.parent.name, p.parent.name)
                                            for p in (ROOT / 'apps').glob('*/*/helmrelease.yaml')])
        self.env['QUOTA'] = json.dumps(contract.QUOTA)
        self.env['ROUTES'] = json.dumps(channels.routes(ROOT))
        self.env['CHANNEL_SECRETS'] = '\n'.join(f'secret/channel-{name}' for name in channels.channels(ROOT))
        self.env['REPO'], self.env['ISSUER'] = str(ROOT), calling.ISSUER
        for name in ('kubectl', 'flux', 'aws', 'curl', 'gh', 'systemctl'):
            path = self.bin / name
            path.write_text('''#!/usr/bin/env python3
import base64, datetime, json, os, sys
from pathlib import Path
with open(os.environ['CALLS'], 'a') as f:
    f.write(Path(sys.argv[0]).name + '\\n')
scenario = os.environ.get('SCENARIO', 'match')
argv = sys.argv[1:]
def emit(obj):
    print(json.dumps(obj))
if Path(sys.argv[0]).name == 'curl':
    sys.stdin.read()
    print('HTTP/2 200')
elif Path(sys.argv[0]).name == 'systemctl':
    if argv[0] == 'is-active':
        print('active')
    elif argv[0] == 'show':
        timestamp = datetime.datetime.now().astimezone().strftime('%a %Y-%m-%d %H:%M:%S %Z')
        print('Result=success\\nExecMainStatus=0\\nExecMainExitTimestamp=' + timestamp)
elif Path(sys.argv[0]).name == 'aws':
    emit(['clickstack-mongodb/clickstack-mongodb-' + os.environ['STAMP'] + '.archive.gz.age'])
elif Path(sys.argv[0]).name == 'gh':
    emit([{'active': True, 'config': {'url': 'https://' + os.environ['WEBHOOK_HOST'] + '/hook/x'},
           'last_response': {'code': 200}}])
elif any(a.startswith('imagepolicies') for a in argv):
    emit({'items': []})
elif argv[:3] == ['-n', 'messaging', 'get']:
    emit({'status': {'readyReplicas': 1}})
elif len(argv) > 1 and argv[0] == 'get' and argv[1].startswith('streams.jetstream'):
    emit({'items': []})
elif argv[:3] == ['get', '--raw', '/openid/v1/jwks']:
    print((Path(os.environ['REPO']) / 'apps/signing-keys.json').read_text())
elif argv[:3] == ['get', '--raw', '/.well-known/openid-configuration']:
    emit({'issuer': os.environ['ISSUER']})
elif argv[:2] == ['get', 'namespaces']:  # isolation check: every instance in this checkout is in order
    emit({'items': [{'metadata': {'name': a + '-' + e, 'labels': {'platform.swhurl.com/app': a,
                                                                  'platform.swhurl.com/team': 'demo'}}}
                    for a, e in json.loads(os.environ['INSTANCES'])]})
elif argv[:2] == ['get', 'resourcequotas']:
    emit({'items': [{'metadata': {'namespace': a + '-' + e}, 'spec': {'hard': json.loads(os.environ['QUOTA'])}}
                    for a, e in json.loads(os.environ['INSTANCES'])]})
elif any(a.startswith('networkpolicies') for a in argv):
    emit({'items': [{'metadata': {'namespace': a + '-' + e},
                     'spec': {'podSelector': {'matchLabels': {'app.kubernetes.io/instance': a}}}}
                    for a, e in json.loads(os.environ['INSTANCES'])]})
elif argv[:2] == ['get', 'deployments']:  # backup-sqlite discovery: no app has a SQLite database
    emit({'items': []})
elif argv[:3] == ['-n', 'notify', 'get']:
    if 'deployment' in argv:
        emit({'spec': {'template': {'spec': {'containers': [{'image': 'ghcr.io/x/swhurl-console:' + os.environ['CONSOLE_TAG']}]}}},
              'status': {'readyReplicas': 1}})
    elif 'configmaps' in argv:
        emit({'items': [{'metadata': {'name': 'notification-routes'}, 'data': {'routes.json': os.environ['ROUTES']}}]})
    else:
        print(os.environ['CHANNEL_SECRETS'])
elif any(a.startswith('alerts.notification') for a in argv):
    emit({'items': [{'metadata': {'name': n}, 'spec': {'providerRef': {'name': 'p'}}} for n in ('failures',)]})
elif any(a.startswith('providers.notification') for a in argv):
    emit({'items': [{'metadata': {'name': 'p'}}]})
elif any(a.startswith('receivers') for a in argv):
    emit({'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}})
elif Path(sys.argv[0]).name == 'flux':
    sys.exit(1 if os.environ.get('FLUX_FAIL') else 0)
elif any(a.startswith('kustomizations') for a in argv):
    ready = 'False' if scenario == 'unready' else 'True'
    emit({'items': [{'metadata': {'name': 'platform-clickstack'},
                     'status': {'conditions': [{'type': 'Ready', 'status': ready, 'message': 'fixture'}]}}]})
elif 'console-github' in argv:
    emit({'data': {'GITHUB_TOKEN': base64.b64encode(b'fixture-github-token').decode()}})
elif 'cronjob' in argv and any(n in argv for n in ('console-dashboards', 'console-notifications')):
    emit({'spec': {}, 'status': {'lastSuccessfulTime': datetime.datetime.now(datetime.timezone.utc).isoformat()}})
elif 'console-jobs' in argv:
    emit({'data': {}})
elif 'helmrelease' in argv and 'console' in argv:
    emit({'spec': {'values': {'controllers': {'main': {'containers': {'main': {'image': {
        'tag': os.environ['CONSOLE_TAG']}}}}}}}})
elif 'flux-system' in argv and 'deployment' in argv:
    emit({'metadata': {'labels': {'app.kubernetes.io/version': os.environ['FLUX_VERSION']}},
          'spec': {'template': {'spec': {'containers': [{'args': os.environ['FLUX_ARGS'].split()}]}}}})
elif 'traefik' in argv:
    emit({'spec': {'template': {'spec': {'containers': [{'args': [
        '--entryPoints.web.http.redirections.entryPoint.to=:443',
        '--entryPoints.web.http.redirections.entryPoint.scheme=https']}]}}}})
elif 'clickhouse-client' in argv:
    print('9\\t9' if 'toIntervalDay(30)' in argv[-1] else '0')
elif 'pvc' in argv:
    emit({'spec': {'volumeName': 'pv-mongodb'}})
elif 'pv' in argv:
    emit({'spec': {'persistentVolumeReclaimPolicy': 'Retain'}})
elif 'exec' in argv and 'node' in argv:
    print('RESULT ' + json.dumps({'status': 200, 'body': {'isTeamExisting': True}}))
elif 'exec' in argv:
    sys.stdin.read()
    print('RESULT ' + json.dumps({'keys': [os.environ['KEY']]}))
    if scenario == 'mongo-failure':
        print('failed near ' + os.environ['KEY'], file=sys.stderr)
        sys.exit(1)
elif 'clickstack-mongodb-hyperdx-hyperdx' in argv:
    emit({'data': {'connectionString.standard': base64.b64encode(b'mongodb://u:p@db/hyperdx').decode()}})
elif 'secret' in argv:
    value = os.environ['KEY'].encode()
    if scenario == 'mismatch': value = os.environ['OTHER_KEY'].encode()
    if scenario == 'newline': value += b'\\n'
    if scenario == 'double': value = base64.b64encode(value)
    encoded = base64.b64encode(value).decode()
    if scenario == 'invalid': encoded += '!'
    emit({'data': {} if scenario == 'missing' else {'CLICKSTACK_INGESTION_KEY': encoded}})
''')
            path.chmod(0o700)

    def test_recovery_dry_runs_never_call_cluster_tools(self):
        for target in ('backup-mongodb', 'live-test-restore-mongodb'):
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

    def test_every_dry_run_that_plans_calls_no_cluster_tool(self):
        """Every target whose DRY_RUN=true only prints a plan (commands.PLAN), so CI keeps no list of them."""
        targets = [name for name, command in commands.TARGETS.items()
                   if command.dry_run == commands.PLAN and not command.missing({})]
        self.assertGreater(len(targets), 15)

        def plan(target):
            return subprocess.run(['make', target, 'DRY_RUN=true'], cwd=ROOT, capture_output=True, text=True,
                                  env=dict(self.env, BACKUP_DIR=str(self.bin / 'backups')))

        with ThreadPoolExecutor() as pool:
            results = dict(zip(targets, pool.map(plan, targets), strict=True))
        for target, result in results.items():
            with self.subTest(target=target):
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue(result.stdout.strip(), 'a plan says what would happen')
        self.assertFalse(self.calls.exists(), 'A dry run that plans called a cluster tool')
        self.assertFalse((self.bin / 'backups').exists(), 'A dry run that plans wrote files')

    def test_a_missing_variable_stops_a_target_before_any_tool(self):
        result = subprocess.run(['make', 'app-reconcile', 'APP=web'], cwd=ROOT, env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn('Usage: make app-reconcile APP=<app> ENV=<env>', result.stderr)
        self.assertFalse(self.calls.exists())

    def test_sqlite_restore_test_dry_run_never_calls_cluster_tools(self):
        result = subprocess.run(['make', 'live-test-restore-sqlite', 'DRY_RUN=true'], cwd=ROOT, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('restore-sqlite brings back one, two', result.stdout)
        self.assertFalse(self.calls.exists())

    def test_restore_sqlite_refuses_without_exact_confirmation(self):
        for confirm in ('', 'notes/prod', 'notes-staging'):
            with self.subTest(confirm=confirm):
                result = subprocess.run(['make', 'restore-sqlite', 'APP=notes', 'ENV=staging', f'CONFIRM={confirm}'],
                                        cwd=ROOT, env=self.env, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('CONFIRM=notes/staging', result.stderr)
                self.assertFalse(self.calls.exists(), 'A refused restore-sqlite called a cluster tool')

    def test_install_stops_at_the_first_failing_step(self):
        result = subprocess.run(['make', 'install'], cwd=ROOT, env=dict(self.env, FLUX_FAIL='1'),
                                capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        calls = self.calls.read_text().split() if self.calls.exists() else []
        self.assertEqual(calls, ['flux'], 'verify-platform must not run after a failed reconcile')

    def test_install_plan_respects_skip_verify(self):
        for extra, steps in (([], ['flux-reconcile', 'verify-platform']),
                             (['SKIP_VERIFY=1'], ['flux-reconcile'])):
            with self.subTest(args=extra):
                result = subprocess.run(['make', '--no-print-directory', 'install', 'DRY_RUN=true', *extra], cwd=ROOT,
                                        env=self.env, capture_output=True, text=True)
                self.assertEqual(result.stdout.splitlines(), ['Plan (install):', *[f'  - make {s}' for s in steps]])
                self.assertFalse(self.calls.exists())

    def test_verifier_checks_actual_bytes_and_never_prints_credentials(self):
        """End to end through real subprocesses and the fake kubectl (contract test)."""
        scenarios = ('match', 'double', 'mismatch', 'missing', 'invalid', 'newline', 'mongo-failure', 'unready')

        def verify(scenario):
            return subprocess.run([sys.executable, '-m', 'swhurl', 'verify-platform'], cwd=ROOT,
                                  env=dict(self.env, SCENARIO=scenario, PYTHONPATH=str(ROOT / 'tools')),
                                  capture_output=True, text=True)

        # The scenarios are independent processes; running them together keeps make test quick.
        with ThreadPoolExecutor() as pool:
            results = dict(zip(scenarios, pool.map(verify, scenarios), strict=True))
        for scenario, result in results.items():
            with self.subTest(scenario=scenario):
                output = result.stdout + result.stderr
                self.assertEqual(result.returncode == 0, scenario == 'match', output)
                for value in (KEY, OTHER_KEY):
                    self.assertNotIn(value, output)
                    self.assertNotIn(base64.b64encode(value.encode()).decode(), output)


if __name__ == '__main__':
    unittest.main()
