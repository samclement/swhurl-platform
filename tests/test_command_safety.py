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
from swhurl.apps import calling, channels

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
        stamp = dt.datetime.now(dt.UTC).strftime('%Y%m%dT%H%M%SZ')  # the fake bucket's backups are this fresh
        self.env.update(STAMP=stamp)
        self.env['WEBHOOK_HOST'] = f'flux-webhook.{platform.base_domain()}'
        self.env.update(FLUX_VERSION=f'v{flux.pinned_version()}',
                        FLUX_ARGS=' '.join(flux.required_args()['kustomize-controller']))
        # The console image tag is this checkout's commit, so the real git diff finds no change.
        self.env['CONSOLE_TAG'] = subprocess.run(['git', 'rev-parse', 'HEAD'], cwd=ROOT, capture_output=True,
                                                 text=True, check=True).stdout.strip()
        self.env['ROUTES'] = json.dumps(channels.routes(ROOT))
        self.env['CHANNEL_SECRETS'] = '\n'.join(f'secret/channel-{name}' for name in channels.routes(ROOT)['channels'])
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
    prefix, stamp = argv[argv.index('--prefix') + 1], os.environ['STAMP']
    names = ['clickstack-mongodb-' + stamp + s for s in ('.archive.gz.age', '.json')] if prefix.startswith('clickstack') \\
        else ['sqlite-' + stamp + s for s in ('.db.age', '.json')]
    emit([prefix + name for name in names])
elif Path(sys.argv[0]).name == 'gh':
    emit([{'active': True, 'config': {'url': 'https://' + os.environ['WEBHOOK_HOST'] + '/hook/x'},
           'last_response': {'code': 200}}])
elif any(a.startswith('imagepolicies') for a in argv):
    emit({'items': []})
elif argv[:3] == ['-n', 'messaging', 'get']:
    emit({'status': {'readyReplicas': 1}})
elif argv[:3] == ['-n', 'egress', 'get']:
    emit({'spec': {'replicas': 2}, 'status': {'readyReplicas': 2}})
elif len(argv) > 1 and argv[0] == 'get' and argv[1].startswith('streams.jetstream'):
    # every stream and consumer v2 rendered for this checkout's instances, Ready in NATS
    folders = Path(os.environ['REPO']).glob('v2-apps/instances/*/*')
    docs = [json.loads(p.read_text()) for f in folders for p in
            [*sorted(f.glob('stream.json')), *sorted(f.glob('consumer-*.json'))]]
    emit({'items': [d | {'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}} for d in docs]})
elif argv[:3] == ['get', '--raw', '/openid/v1/jwks']:
    print(json.loads((Path(os.environ['REPO']) / 'v2/testdata/profile.json').read_text())['auth']['keys'])
elif argv[:3] == ['get', '--raw', '/.well-known/openid-configuration']:
    emit({'issuer': os.environ['ISSUER']})
elif argv[:2] == ['get', 'namespaces']:  # isolation: a healthy cluster has what Git declares
    folders = Path(os.environ['REPO']).glob('v2-apps/instances/*/*')
    emit({'items': [json.loads((f / 'namespace.json').read_text()) for f in folders]})
elif argv[:2] == ['get', 'resourcequotas']:
    folders = Path(os.environ['REPO']).glob('v2-apps/instances/*/*')
    emit({'items': [json.loads((f / 'resourcequota.json').read_text()) for f in folders]})
elif any(a.startswith('networkpolicies') for a in argv):
    folders = Path(os.environ['REPO']).glob('v2-apps/instances/*/*')
    emit({'items': [json.loads((f / 'networkpolicy.json').read_text()) for f in folders]})
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
        self.assertGreater(len(targets), 14)

        def plan(target):
            return subprocess.run(['make', target, 'DRY_RUN=true'], cwd=ROOT, capture_output=True, text=True, env=self.env)

        with ThreadPoolExecutor() as pool:
            results = dict(zip(targets, pool.map(plan, targets), strict=True))
        for target, result in results.items():
            with self.subTest(target=target):
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertTrue(result.stdout.strip(), 'a plan says what would happen')
        self.assertFalse(self.calls.exists(), 'A dry run that plans called a cluster tool')

    def test_a_missing_variable_stops_a_target_before_any_tool(self):
        result = subprocess.run(['make', 'app-reconcile', 'APP=web'], cwd=ROOT, env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn('Usage: make app-reconcile APP=<app> ENV=<env>', result.stderr)
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
        """End to end through real subprocesses and the fake kubectl (contract test).

        ``match`` and ``unready`` run the whole verify-platform; the key scenarios change only the
        ingestion Secret, so they run that one check (a whole run costs about 60 fake tool processes)."""
        whole = ('match', 'unready')
        key_only = ('double', 'mismatch', 'missing', 'invalid', 'newline', 'mongo-failure')
        check = ('import sys\n'
                 'from swhurl import clickstack\n'
                 'from swhurl.report import Report\n'
                 'from swhurl.run import Runner\n'
                 'runner, report = Runner(), Report()\n'
                 'report.redact = runner.redact\n'
                 'clickstack.check_ingestion(runner, report)\n'
                 'sys.exit(report.exit_code())\n')

        def verify(scenario):
            argv = ['-m', 'swhurl', 'verify-platform'] if scenario in whole else ['-c', check]
            return subprocess.run([sys.executable, *argv], cwd=ROOT,
                                  env=dict(self.env, SCENARIO=scenario, PYTHONPATH=str(ROOT / 'tools')),
                                  capture_output=True, text=True)

        # The scenarios are independent processes; running them together keeps make test quick.
        scenarios = whole + key_only
        with ThreadPoolExecutor() as pool:
            results = dict(zip(scenarios, pool.map(verify, scenarios), strict=True))
        for scenario, result in results.items():
            with self.subTest(scenario=scenario):
                output = result.stdout + result.stderr
                self.assertEqual(result.returncode == 0, scenario == 'match', output)
                if scenario in key_only:
                    self.assertIn('Ingestion Key Sync', output, 'the key check ran')
                for value in (KEY, OTHER_KEY):
                    self.assertNotIn(value, output)
                    self.assertNotIn(base64.b64encode(value.encode()).decode(), output)


class OpsScriptTests(unittest.TestCase):
    """host/ops.sh behind make app-logs, app-reconcile, suspend, resume, backup-mongodb and backup-sqlite: the argv
    each tool gets, and refusals that call none. The fake kubectl answers ``get`` with ``GET_RC`` (0: the object
    exists), lists ``CRONJOBS`` as the backup CronJobs and states ``JOB_STATE`` as a Job's true conditions."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.bin = Path(self.tmp.name)
        self.calls = self.bin / 'calls'
        for name in ('kubectl', 'flux'):
            (self.bin / name).write_text('#!/usr/bin/env bash\n'
                                         'printf "%s\\n" "$(basename "$0") $*" >> "$CALLS"\n'
                                         '[[ $1 == get && $2 == cronjobs ]] && printf "%s\\n" ${CRONJOBS:-}\n'
                                         '[[ $3 == get && $4 == job ]] && printf "%b\\n" "${JOB_STATE:-}"\n'
                                         '[[ $3 == logs ]] && echo "[OK] published"\n'
                                         '[[ $1 == -n && $3 == get ]] && exit "${GET_RC:-0}"\nexit 0\n')
            (self.bin / name).chmod(0o700)
        self.env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}', CALLS=str(self.calls))

    def make(self, *argv, **env):
        result = subprocess.run(['make', '-s', *argv], cwd=ROOT, env={**self.env, **env}, capture_output=True, text=True)
        called = self.calls.read_text().splitlines() if self.calls.exists() else []
        self.calls.unlink(missing_ok=True)
        return result, called

    def test_logs_passes_tail_previous_and_follow_and_has_no_dry_run(self):
        _, called = self.make('app-logs', 'APP=web', 'ENV=prod')
        self.assertEqual(called, ['kubectl -n web-prod logs deploy/web --all-containers --tail=100'])
        _, called = self.make('app-logs', 'APP=web', 'ENV=prod', TAIL='5', PREVIOUS='true', FOLLOW='true')
        self.assertEqual(called, ['kubectl -n web-prod logs deploy/web --all-containers --tail=5 --previous --follow'])
        result, called = self.make('app-logs', 'APP=web', 'ENV=prod', 'DRY_RUN=true')
        self.assertEqual((result.returncode, called), (2, []))
        self.assertIn('make app-logs has no dry run', result.stderr)

    def test_reconcile_fetches_git_then_the_one_unit_and_plans_without_a_call(self):
        result, called = self.make('app-reconcile', 'APP=web', 'ENV=prod')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(called, ['flux reconcile source git swhurl-platform -n flux-system --timeout=5m',
                                  'flux reconcile kustomization app-web-prod -n flux-system --timeout=10m'])
        result, called = self.make('app-reconcile', 'APP=web', 'ENV=prod', 'DRY_RUN=true')
        self.assertEqual((result.returncode, called), (0, []))
        self.assertTrue(result.stdout.startswith('Plan (app-reconcile web/prod):\n  would run: flux reconcile source'))

    def test_an_instance_that_is_not_one_is_refused_before_any_tool(self):
        for app, env, message in (('web', '', 'Usage: make app-logs APP=<app> ENV=<env>'),
                                  ('Web', 'prod', 'no instance Web/prod'), ('web', 'dev', 'no instance web/dev'),
                                  ('-A', 'prod', 'no instance -A/prod'), ('web;id', 'prod', 'no instance web;id/prod')):
            with self.subTest(app=app, env=env):
                result, called = self.make('app-logs', f'APP={app}', f'ENV={env}')
                self.assertEqual((result.returncode, called), (2, []))
                self.assertIn(message, result.stderr)

    def test_suspend_and_resume_check_the_target_then_call_flux(self):
        result, called = self.make('suspend', 'TARGET=kustomization/homelab-a')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(called, ['kubectl -n flux-system get kustomization homelab-a',
                                  'flux suspend kustomization homelab-a -n flux-system'])
        self.assertIn('suspend them separately to freeze Helm', result.stdout)
        result, called = self.make('resume', 'TARGET=helmrelease/ns/r')
        self.assertEqual((result.returncode, result.stdout), (0, ''))
        self.assertEqual(called[-1], 'flux resume helmrelease r -n ns')

    def test_suspend_dry_run_reads_but_does_not_call_flux(self):
        result, called = self.make('suspend', 'TARGET=kustomization/homelab-a', 'DRY_RUN=true')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(called, ['kubectl -n flux-system get kustomization homelab-a'])
        self.assertEqual(result.stdout, 'Plan (suspend kustomization/homelab-a):\n'
                                        '  would run: flux suspend kustomization homelab-a -n flux-system\n')

    def test_a_missing_or_malformed_target_is_refused(self):
        result, called = self.make('suspend', 'TARGET=kustomization/nope', GET_RC='1')
        self.assertEqual(result.returncode, 2)
        self.assertIn('kustomization flux-system/nope not found', result.stderr)
        self.assertEqual(called, ['kubectl -n flux-system get kustomization nope'], 'no flux call')
        for target, message in (('', 'Usage: make suspend TARGET='), ('pvc/ns/c', 'Usage: make suspend TARGET='),
                                ('kustomization/a/b', 'Use kustomization/<name>'),
                                ('kustomization/-A', 'Use kustomization/<name>'),
                                ('helmrelease/r', 'Use helmrelease/<namespace>/<name>'),
                                ('helmrelease/ns/r/x', 'Use helmrelease/<namespace>/<name>'),
                                ('helmrelease//r', 'Use helmrelease/<namespace>/<name>')):
            with self.subTest(target=target):
                result, called = self.make('resume', f'TARGET={target}')
                self.assertEqual((result.returncode, called), (2, []))
                self.assertIn(message.replace('suspend', 'resume'), result.stderr)

    def test_backup_sqlite_runs_every_app_job_but_mongodbs_and_shows_each_log(self):
        cronjobs = 'notes-staging/notes-backup observability/backup-mongodb'
        result, called = self.make('backup-sqlite', CRONJOBS=cronjobs, JOB_STATE='SuccessCriteriaMet\\nComplete')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(called[0], 'kubectl get cronjobs --all-namespaces -l platform.swhurl.com/backup=true -o '
                                    'jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name}{"\\n"}{end}')
        self.assertRegex(called[1], r'^kubectl -n notes-staging create job notes-backup-manual-\d{8}t\d{6}z '
                                    r'--from=cronjob/notes-backup$')
        self.assertEqual([call.split()[:5] for call in called[2:]],
                         [['kubectl', '-n', 'notes-staging', 'get', 'job'], ['kubectl', '-n', 'notes-staging', 'logs',
                                                                              called[1].split()[5].join(('job/', ''))]])
        self.assertIn('  notes-staging: [OK] published\n[OK] notes-staging/notes-backup-manual-', result.stdout)
        self.assertNotIn('backup-mongodb', ' '.join(called[1:]), "MongoDB's job is make backup-mongodb's")

    def test_a_failed_backup_job_fails_the_target_after_its_log(self):
        result, called = self.make('backup-mongodb', JOB_STATE='FailureTarget\\nFailed')
        self.assertEqual(result.returncode, 2, 'make reports a failed recipe as 2')
        self.assertRegex(called[0], '^kubectl -n observability create job backup-mongodb-manual-')
        self.assertIn('  observability: [OK] published\n[ERROR] observability/backup-mongodb-manual-', result.stdout)
        self.assertIn('failed\n', result.stdout)
        self.assertIn('[ERROR] backup failed for observability/backup-mongodb-manual-', result.stderr)

    def test_backup_plans_read_at_most_the_list_of_jobs(self):
        result, called = self.make('backup-mongodb', 'DRY_RUN=true')
        self.assertEqual((result.returncode, called), (0, []))
        self.assertRegex(result.stdout, r'^Plan \(backup jobs\):\n  - kubectl -n observability create job '
                                        r'backup-mongodb-manual-\d{8}t\d{6}z --from=cronjob/backup-mongodb, wait, show its log\n$')
        result, called = self.make('backup-sqlite', 'DRY_RUN=true', CRONJOBS='a-prod/a-backup')
        self.assertEqual((result.returncode, len(called)), (0, 1), 'only the list of CronJobs is read')
        self.assertIn('--from=cronjob/a-backup, wait', result.stdout)
        result, called = self.make('backup-sqlite', CRONJOBS='observability/backup-mongodb')
        self.assertEqual((result.returncode, len(called)), (0, 1))
        self.assertEqual(result.stdout, '[INFO] No app SQLite backup job: nothing to run.\n')


if __name__ == '__main__':
    unittest.main()
