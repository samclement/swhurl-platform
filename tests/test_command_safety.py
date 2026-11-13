"""The make interface, end to end through fake kubectl/flux executables on PATH.

Kept for what only a real process can show: exit codes, argument passing, dry
runs and refusals that make no cluster calls, and nothing secret on stdout or
stderr. Branch-by-branch behaviour is unit-tested with FakeRunner elsewhere.
"""
import json
import os
import subprocess
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from swhurl import ROOT, commands


class CommandSafetyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.bin = Path(self.tmp.name)
        self.calls = self.bin / 'calls'
        self.env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}', CALLS=str(self.calls))
        for name in ('kubectl', 'flux', 'systemctl'):
            path = self.bin / name
            path.write_text("""#!/usr/bin/env python3
import os, sys, time
from pathlib import Path
tool = Path(sys.argv[0]).name
with open(os.environ['CALLS'], 'a') as f:
    f.write(tool + '\\n')
if tool == 'systemctl':  # the host heartbeat facts the recipe passes to swhurl-v2, which decides them
    print('Id=swhurl-notification-heartbeat.timer\\nActiveState=active\\n')
    print('Id=swhurl-notification-heartbeat.service\\nResult=success\\nExecMainStatus=0\\n'
          'ExecMainExitTimestamp=@' + str(int(time.time())))
elif tool == 'flux':
    sys.exit(1 if os.environ.get('FLUX_FAIL') else 0)
else:
    print('{}')
""")
            path.chmod(0o700)

    def test_destroy_data_refuses_without_exact_confirmation(self):
        """A target of the wrong shape is refused by swhurl-v2 before it reads the kubeconfig (its own tests)."""
        cases = (('pvc/ns/data', ''), ('pvc/ns/data', 'pvc/ns/other'), ('pv/name', 'pv/other'), ('', ''))
        for target, confirm in cases:
            with self.subTest(target=target, confirm=confirm):
                result = subprocess.run(['make', 'destroy-data', f'TARGET={target}', f'CONFIRM={confirm}'],
                                        cwd=ROOT, env=self.env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
                self.assertFalse(self.calls.exists(), 'A refused destroy-data called a cluster tool')

    def test_every_dry_run_that_plans_calls_no_cluster_tool(self):
        """Every target whose DRY_RUN=true only prints a plan (commands.PLAN), so CI keeps no list of them."""
        targets = [name for name, command in commands.TARGETS.items()
                   if command.dry_run == commands.PLAN and not command.missing({})]
        self.assertGreater(len(targets), 11)

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

    def test_reconcile_needs_a_unit_and_its_dry_run_only_plans(self):
        result = subprocess.run(['make', 'reconcile'], cwd=ROOT, env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2, result.stdout + result.stderr)
        self.assertIn('Usage: make reconcile UNIT=<name>', result.stderr)
        result = subprocess.run(['make', '-s', 'reconcile', 'UNIT=gw', 'DRY_RUN=true'], cwd=ROOT, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stdout, 'Plan (reconcile gw):\n  would run: swhurl-v2 unit --action reconcile --unit gw\n')
        self.assertFalse(self.calls.exists(), 'A refused or planned reconcile called a cluster tool')
        self.assertFalse(self.calls.exists())

    def test_suspend_and_resume_hand_the_target_to_swhurl_v2(self):
        """The recipe passes TARGET to swhurl-v2 unit, which checks its shape and names (its own tests), and says what a
        suspend leaves running only after it succeeded. The dry run and a missing target call nothing."""
        fake = self.bin / 'unit-recipe'
        fake.write_text('''#!/usr/bin/env python3
import os, stat, sys
from pathlib import Path
assert sys.argv[6] == '--kubeconfig'
config = Path(sys.argv[7])
assert config.read_text().strip() == '{}'
assert stat.S_IMODE(config.stat().st_mode) == 0o600
Path(os.environ['RECEIPT']).write_text(str(config))
with open(os.environ['CALLS'], 'a') as f:
    f.write('swhurl-v2 ' + ' '.join(sys.argv[1:6]) + '\\n')
sys.exit(int(os.environ.get('GO_RC', '0')))
''')
        fake.chmod(0o700)
        build = f'V2_BUILD=set -eu; work=$$(mktemp -d); trap \'rm -rf "$$work"\' EXIT; cp {fake} "$$work/swhurl-v2"'
        receipt = self.bin / 'unit-receipt'
        thaw = '[INFO] HelmReleases it created keep reconciling'
        for action, target, go_rc, info in (('suspend', 'kustomization/gw', '0', 2), ('suspend', 'helmrelease/ns/r', '0', 1),
                                            ('resume', 'helmrelease/ns/r', '0', 0), ('suspend', 'kustomization/gw', '2', 0)):
            with self.subTest(action=action, target=target, go_rc=go_rc):
                self.calls.unlink(missing_ok=True)
                result = subprocess.run(['make', '-s', action, f'TARGET={target}', build], cwd=ROOT,
                                        env=dict(self.env, GO_RC=go_rc, RECEIPT=str(receipt)), capture_output=True, text=True)
                self.assertEqual(result.returncode != 0, go_rc != '0', result.stdout + result.stderr)
                self.assertEqual(self.calls.read_text().splitlines(),
                                 ['kubectl', f'swhurl-v2 unit --action {action} --target {target}'])
                self.assertEqual(result.stdout.count('[INFO]'), info, result.stdout)
                self.assertEqual(thaw in result.stdout, info == 2)
                self.assertFalse(Path(receipt.read_text()).exists(), 'private kubeconfig survived recipe exit')
        self.calls.unlink(missing_ok=True)
        receipt.unlink()
        kubectl = self.bin / 'kubectl'
        kubectl.write_text('#!/usr/bin/env bash\necho kubectl >> "$CALLS"\nexit 1\n')
        kubectl.chmod(0o700)
        result = subprocess.run(['make', '-s', 'suspend', 'TARGET=kustomization/gw', build], cwd=ROOT,
                                env=dict(self.env, RECEIPT=str(receipt)), capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.calls.read_text().splitlines(), ['kubectl'])
        self.assertNotIn('[INFO]', result.stdout)
        self.assertFalse(receipt.exists(), 'Go ran after kubeconfig export failed')
        self.calls.unlink()
        result = subprocess.run(['make', '-s', 'suspend', 'TARGET=kustomization/gw', 'DRY_RUN=true'], cwd=ROOT,
                                env=self.env, capture_output=True, text=True)
        self.assertEqual((result.returncode, result.stdout), (0, 'Plan (suspend kustomization/gw):\n'
                                                                 '  would run: swhurl-v2 unit --action suspend --target kustomization/gw\n'))
        result = subprocess.run(['make', '-s', 'resume'], cwd=ROOT, env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2)
        self.assertIn('Usage: make resume TARGET=kustomization/<name>|helmrelease/<ns>/<name>', result.stderr)
        self.assertFalse(self.calls.exists(), 'A planned or refused suspend called a tool')

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

    def test_notifications_recipe_is_private_read_only_and_stops_on_input_failure(self):
        """Only host glue is exercised: fake tools, a caller kubeconfig, and no topic or notify token."""
        caller = self.bin / 'caller-kubeconfig.json'
        caller.write_text('{"caller-credential": "private-kubeconfig-fixture"}')
        receipt = self.bin / 'receipt.json'
        workpath = self.bin / 'workpath'
        for name, source in {
            'kubectl': r'''import os, sys
from pathlib import Path
assert sys.argv[1:] == ['config', 'view', '--raw', '-o', 'json'], sys.argv
with open(os.environ['CALLS'], 'a') as f:
    f.write('kubectl\n')
print(Path(os.environ['KUBECONFIG']).read_text())
sys.exit(int(os.environ['CONFIG_RC']))
''',
            'jq': r'''import json, os, sys
assert sys.argv[1:] == ['-er', '.routing.baseDomain | strings | select(length > 0)',
                         'v2/testdata/profile.json'], sys.argv
with open(os.environ['CALLS'], 'a') as f:
    f.write('jq\n')
if os.environ['JQ_RC'] != '0':
    sys.exit(int(os.environ['JQ_RC']))
with open(sys.argv[-1]) as f:
    print(json.load(f)['routing']['baseDomain'])
''',
            'checker-recipe': r'''import json, os, stat, sys
from pathlib import Path
argv = sys.argv[1:]
assert argv[:3] == ['notifications-check', '--dry-run', '--kubeconfig'], argv
assert len(argv) == 4, argv
config = Path(argv[3])
assert config != Path(os.environ['KUBECONFIG'])
assert config.read_text().strip() == Path(os.environ['KUBECONFIG']).read_text()
assert stat.S_IMODE(config.stat().st_mode) == 0o600
assert stat.S_IMODE(config.parent.stat().st_mode) == 0o700
assert 'NTFY_PLATFORM_URL' not in os.environ
assert 'SWHURL_NOTIFY_URL' not in os.environ
with open(os.environ['CALLS'], 'a') as f:
    f.write('go\n')
Path(os.environ['RECEIPT']).write_text(json.dumps({'config': str(config), 'base': os.environ['CONSOLE_URL']}))
print('checker dry run')
sys.exit(int(os.environ['GO_RC']))
''',
        }.items():
            executable = self.bin / name
            executable.write_text('#!/usr/bin/env python3\n' + source)
            executable.chmod(0o700)
        build = ('V2_BUILD=set -eu; work=$$(mktemp -d); trap \'rm -rf "$$work"\' EXIT; '
                 f'printf "%s" "$$work" > {workpath}; cp {self.bin / "checker-recipe"} "$$work/swhurl-v2"')
        base = json.loads((ROOT / 'v2/testdata/profile.json').read_text())['routing']['baseDomain']
        for override, dry, config_rc, jq_rc, go_rc, expected in (
                ('', 'false', '0', '0', '0', ['kubectl', 'jq', 'go']),
                ('', 'true', '0', '0', '0', ['kubectl', 'jq', 'go']),
                ('https://console.example.test', 'false', '0', '127', '0', ['kubectl', 'go']),
                ('', 'false', '1', '0', '0', ['kubectl']),
                ('', 'false', '0', '1', '0', ['kubectl', 'jq']),
                ('', 'false', '0', '127', '0', ['kubectl', 'jq']),
                ('', 'false', '0', '0', '1', ['kubectl', 'jq', 'go'])):
            with self.subTest(override=override, dry=dry, config_rc=config_rc, jq_rc=jq_rc, go_rc=go_rc):
                self.calls.unlink(missing_ok=True)
                receipt.unlink(missing_ok=True)
                env = dict(self.env, KUBECONFIG=str(caller), CONSOLE_URL=override, CONFIG_RC=config_rc,
                           JQ_RC=jq_rc, GO_RC=go_rc, RECEIPT=str(receipt),
                           SWHURL_NOTIFY_TOKEN_FILE=str(self.bin / 'no-token-exists'))
                for key in ('NTFY_PLATFORM_URL', 'SWHURL_NOTIFY_URL'):
                    env.pop(key, None)
                result = subprocess.run(['make', 'notifications-check', build, f'DRY_RUN={dry}',
                                         'ARGS=--in-cluster'], cwd=ROOT, env=env, capture_output=True, text=True)
                failed = config_rc != '0' or (not override and jq_rc != '0') or go_rc != '0'
                self.assertEqual(result.returncode != 0, failed, result.stdout + result.stderr)
                self.assertEqual(self.calls.read_text().splitlines(), expected)
                self.assertFalse(Path(workpath.read_text()).exists(), 'temporary directory survived recipe exit')
                self.assertEqual(caller.read_text(), '{"caller-credential": "private-kubeconfig-fixture"}')
                if 'go' in expected:
                    facts = json.loads(receipt.read_text())
                    self.assertEqual(facts['base'], override or f'https://console.{base}')
                    self.assertFalse(Path(facts['config']).exists(), 'private config survived recipe exit')
                else:
                    self.assertFalse(receipt.exists(), 'Go ran after an input failure')
                for private in ('private-kubeconfig-fixture', str(caller), workpath.read_text()):
                    self.assertNotIn(private, result.stdout + result.stderr)

    def test_heartbeat_recipe_is_private_silent_and_stops_on_input_failure(self):
        """The host timer appends the recipe's output to a log file: it holds the program's lines only."""
        caller = self.bin / 'caller-kubeconfig.json'
        caller.write_text('{"caller-credential": "private-kubeconfig-fixture"}')
        workpath = self.bin / 'workpath'
        for name, source in {
            'kubectl': r'''import os, sys
from pathlib import Path
assert sys.argv[1:] == ['config', 'view', '--raw', '-o', 'json'], sys.argv
with open(os.environ['CALLS'], 'a') as f:
    f.write('kubectl\n')
if os.environ['CONFIG_RC'] != '0':
    sys.exit(int(os.environ['CONFIG_RC']))
print(Path(os.environ['KUBECONFIG']).read_text())
''',
            'heartbeat-recipe': r'''import os, stat, sys
from pathlib import Path
argv = sys.argv[1:]
dry = ['--dry-run'] if os.environ['WANT_DRY'] == 'true' else []
assert argv[:-1] == ['notifications-heartbeat', *dry, *os.environ['WANT_ARGS'].split(), '--kubeconfig'], argv
config = Path(argv[-1])
assert config.read_text().strip() == Path(os.environ['KUBECONFIG']).read_text()
assert stat.S_IMODE(config.stat().st_mode) == 0o600
with open(os.environ['CALLS'], 'a') as f:
    f.write('go\n')
print('{"message":"heartbeat"}')
''',
        }.items():
            executable = self.bin / name
            executable.write_text('#!/usr/bin/env python3\n' + source)
            executable.chmod(0o700)
        build = ('V2_BUILD=set -eu; work=$$(mktemp -d); trap \'rm -rf "$$work"\' EXIT; '
                 f'printf "%s" "$$work" > {workpath}; cp {self.bin / "heartbeat-recipe"} "$$work/swhurl-v2"')
        for dry, extra, config_rc, expected in (('false', '', '0', ['kubectl', 'go']), ('true', '', '0', ['kubectl', 'go']),
                                                ('', '--max-age 1s --state /tmp/x', '0', ['kubectl', 'go']),
                                                ('false', '', '1', ['kubectl'])):
            with self.subTest(dry=dry, extra=extra, config_rc=config_rc):
                self.calls.unlink(missing_ok=True)
                env = dict(self.env, KUBECONFIG=str(caller), CONFIG_RC=config_rc, WANT_DRY=dry, WANT_ARGS=extra)
                result = subprocess.run(['make', '--no-print-directory', 'notifications-heartbeat', build,
                                         f'DRY_RUN={dry}', f'ARGS={extra}'], cwd=ROOT, env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode != 0, config_rc != '0', result.stdout + result.stderr)
                self.assertEqual(self.calls.read_text().splitlines(), expected)
                self.assertEqual(result.stdout, '{"message":"heartbeat"}\n' if 'go' in expected else '')
                self.assertFalse(Path(workpath.read_text()).exists(), 'temporary directory survived recipe exit')
                self.assertNotIn('private-kubeconfig-fixture', result.stdout + result.stderr)

    def test_verify_recipe_supplies_hook_credential_and_host_facts_without_leaking(self):
        """The recipe hands swhurl-v2 the GitHub token, this checkout and the host heartbeat facts, and a failed token
        lookup stops nothing. What the checks then decide is Go's (v2/observe)."""
        gh = self.bin / 'gh'
        gh.write_text('#!/usr/bin/env bash\n'
                      'echo called >> "$CALLS"\n'
                      'echo "$FIXTURE_TOKEN"\n'
                      'echo "$FIXTURE_TOKEN" >&2\n'
                      'exit "${GH_RC:-0}"\n')
        gh.chmod(0o700)
        verifier = self.bin / 'verify-recipe'
        verifier.write_text('#!/usr/bin/env python3\n'
                            'import os, sys\n'
                            'argv = sys.argv[1:]\n'
                            'assert argv[:4] == ["verify", "--scope", "platform", "--kubeconfig"], argv\n'
                            'assert argv[argv.index("--root") + 1] == os.getcwd(), argv\n'
                            'heartbeat = argv[argv.index("--heartbeat") + 1]\n'
                            'assert open(heartbeat).read().startswith("Id="), heartbeat\n'
                            'expected = os.environ["EXPECTED_TOKEN"]\n'
                            'assert os.environ.get("GH_TOKEN", "") == expected\n'
                            'print("verifier ran")\n')
        verifier.chmod(0o700)
        build = ('V2_BUILD=set -eu; work=$$(mktemp -d); trap \'rm -rf "$$work"\' EXIT; '
                 f'cp {verifier} "$$work/swhurl-v2"')
        for supplied, lookup_rc, expected, looked_up in (
                ('supplied-fixture', '0', 'supplied-fixture', False),
                ('', '0', 'lookup-fixture', True), ('', '1', '', True)):
            with self.subTest(supplied=bool(supplied), lookup_rc=lookup_rc):
                self.calls.unlink(missing_ok=True)
                env = dict(self.env, GH_TOKEN=supplied, GH_RC=lookup_rc, EXPECTED_TOKEN=expected,
                           FIXTURE_TOKEN='lookup-fixture', AWS_ACCESS_KEY_ID='fixture-access-key')
                result = subprocess.run(['make', 'verify-platform', build],
                                        cwd=ROOT, env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('verifier ran', result.stdout)
                calls = self.calls.read_text().splitlines()
                self.assertEqual('called' in calls, looked_up)
                self.assertIn('systemctl', calls, 'the recipe reads the host heartbeat facts itself')
                for token in ('supplied-fixture', 'lookup-fixture'):
                    self.assertNotIn(token, result.stdout + result.stderr)


class OpsScriptTests(unittest.TestCase):
    """host/ops.sh behind make app-logs, app-reconcile, backup-mongodb and backup-sqlite: the argv
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


class FluxInstallScriptTests(unittest.TestCase):
    """host/flux-install.sh behind make flux-install: it refuses a flux CLI other than flux.json's version before any
    kubectl call, renders the export with the Git patches, and only a real run applies. The fake flux states
    ``CLIENT`` as its version; the fake kubectl renders ``RENDERED`` and has ``DIFF_RC`` differences."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.bin = Path(self.tmp.name)
        self.calls = self.bin / 'calls'
        self.settings = json.loads((ROOT / 'clusters/home/flux-system/install/flux.json').read_text())
        (self.bin / 'flux').write_text('#!/usr/bin/env bash\n'
                                       'printf "%s\\n" "flux $*" >> "$CALLS"\n'
                                       '[[ $1 == version ]] && echo "flux: $CLIENT"\n'
                                       '[[ $1 == install ]] && echo "# exported components"\nexit 0\n')
        (self.bin / 'kubectl').write_text('#!/usr/bin/env bash\n'
                                          'printf "%s\\n" "kubectl $*" >> "$CALLS"\n'
                                          'if [[ $1 == kustomize ]]; then ls "$2" >> "$CALLS"; echo "$RENDERED"; fi\n'
                                          '[[ $1 == diff ]] && exit "${DIFF_RC:-0}"\nexit 0\n')
        for name in ('flux', 'kubectl'):
            (self.bin / name).chmod(0o700)
        args = ' '.join(arg for values in self.settings['args'].values() for arg in values)
        self.env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}', CALLS=str(self.calls),
                        CLIENT=f'v{self.settings["version"]}', RENDERED=f'kind: Deployment\nargs: {args}')

    def install(self, **env):
        result = subprocess.run(['make', '-s', 'flux-install'], cwd=ROOT, env={**self.env, **env},
                                capture_output=True, text=True)
        called = self.calls.read_text().splitlines() if self.calls.exists() else []
        self.calls.unlink(missing_ok=True)
        return result, called

    def test_a_different_flux_cli_is_refused_before_kubectl(self):
        result, called = self.install(CLIENT='v0.0.1')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn(f'pins {self.settings["version"]}; install flux {self.settings["version"]}', result.stderr)
        self.assertEqual(called, ['flux version --client'])

    def test_a_render_without_the_patched_arguments_is_refused(self):
        result, called = self.install(RENDERED='kind: Deployment')
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('rendered kustomize-controller lacks --requeue-dependency=5s', result.stderr)
        self.assertFalse([c for c in called if c.startswith(('kubectl apply', 'kubectl delete', 'kubectl -n'))])

    def test_a_dry_run_diffs_and_applies_nothing(self):
        result, called = self.install(DRY_RUN='true', DIFF_RC='1')
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('would run: kubectl apply --server-side --field-manager=flux --force-conflicts', result.stdout)
        self.assertNotIn('[OK] no changes', result.stdout)
        self.assertTrue(any(c.startswith('kubectl diff --server-side --field-manager=flux -f ') for c in called), called)
        self.assertFalse([c for c in called if c.startswith(('kubectl apply', 'kubectl delete', 'kubectl -n'))], called)
        result, _ = self.install(DRY_RUN='true', DIFF_RC='2')
        self.assertNotEqual(result.returncode, 0, 'a diff that could not compare fails the dry run')

    def test_renders_the_export_with_the_git_patches_applies_as_flux_and_deletes_removed_components(self):
        result, called = self.install()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        extra = ','.join(self.settings['extraComponents'])
        self.assertIn(f'flux install --export --namespace flux-system --components-extra={extra}', called)
        kustomize = called.index(next(c for c in called if c.startswith('kubectl kustomize ')))
        self.assertEqual(called[kustomize + 1:kustomize + 4], ['flux.json', 'gotk-components.yaml', 'kustomization.yaml'])
        self.assertTrue(any(c.startswith('kubectl apply --server-side --field-manager=flux --force-conflicts -f ')
                            for c in called), called)
        self.assertEqual(sum(' rollout status deployment/' in c for c in called), 4 + len(self.settings['extraComponents']))
        for name in self.settings['removedComponents']:
            label = f'app.kubernetes.io/component={name},app.kubernetes.io/part-of=flux'
            self.assertIn(f'kubectl -n flux-system delete deployment,serviceaccount -l {label} --ignore-not-found', called)
            self.assertIn(f'kubectl delete customresourcedefinitions -l {label} --ignore-not-found', called)
        self.assertIn('[OK] Flux controllers applied and rolled out', result.stdout)
