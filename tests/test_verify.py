"""verify-platform and check-repo's required Secrets and settings: every branch, offline, against the fake cluster (fakecluster.py)."""
import datetime as dt
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest import mock

from fakecluster import KEY, b64, healthy, verify_report

from swhurl import ROOT, clickstack, platform, validate, verify
from swhurl.notifications import checks as notification_checks
from swhurl.report import Report
from swhurl.run import CommandError, FakeRunner, Result

OTHER = 'fixture-other-ingestion-key-9999'


def run(runner, **kwargs):
    out, err = io.StringIO(), io.StringIO()
    report = Report(out)
    env = {'SWHURL_V2': 'swhurl-v2', 'SWHURL_V2_KUBECONFIG': '/nonexistent/kubeconfig.json', 'SWHURL_V2_CONSOLE_TAG': 'src-abc'}
    with redirect_stderr(err), mock.patch.dict('os.environ', env):
        code = verify.verify_platform(runner, report, **kwargs)
    return code, report, out.getvalue() + err.getvalue()


class VerifyPlatformTests(unittest.TestCase):
    def assertNoKeys(self, text):
        for value in (KEY, OTHER):
            for form in (value, b64(value), b64(b64(value))):
                self.assertNotIn(form, text)

    def test_healthy_cluster_passes_in_order(self):
        code, report, text = run(healthy())
        self.assertEqual(code, 0, text)
        self.assertEqual(report.lines[:3], ['\n== Flux Kustomizations ==', '[OK] homelab-a', '[OK] homelab-b'])
        self.assertEqual([line for line in report.lines if line.startswith('\n==')],
                         ['\n== Flux Kustomizations ==', '\n== Ingress ==', '\n== Alerts ==', '\n== Notify service ==',
                          '\n== App dashboards and alerts ==', '\n== Notifications ==', '\n== Image Automation ==',
                          '\n== App isolation ==', '\n== Call identity ==', '\n== Events ==', '\n== Egress proxy ==',
                          '\n== Notification channels ==', '\n== Backups ==', '\n== App SQLite backups ==', '\n== Console ==', '\n== Push Webhook ==', '\n== Image Webhooks ==', '\n== Console GitHub Token ==',
                          '\n== Flux Controllers ==', '\n== Runtime Secrets ==', '\n== Ingestion Key Sync ==',
                          '\n== ClickStack Sign-up ==', '\n== Retention ==', '\n== Notification heartbeat =='])
        self.assertEqual(report.failures, 0)
        self.assertTrue(text.rstrip().endswith('Validation passed.'))
        self.assertNoKeys(text)

    def test_each_failure_is_reported_and_fails(self):
        cases = {
            'cluster check bad': ({'v2-verify': verify_report(('ingress', 'Ingress', [('bad', 'Traefik does not redirect HTTP to HTTPS')]))},
                                  'Traefik does not redirect HTTP to HTTPS'),
            'no cluster report': ({'v2-verify': Result((), 2, '', 'credentials: kubeconfig: no current context')},
                                  'swhurl-v2 verify gave no report (not a v2-platform-checks document): credentials: kubeconfig'),
            'another document': ({'v2-verify': {'kind': 'v2-platform-checks', 'version': 2, 'checks': []}}, 'swhurl-v2 verify gave no report'),
            'secret missing': ({'secret': {'data': {}}}, 'CLICKSTACK_INGESTION_KEY is empty'),
            'secret unreadable': ({'secret': Result((), 1, '', 'NotFound')}, 'CLICKSTACK_INGESTION_KEY is empty'),
            'team key unreadable': ({'team': Result((), 2, '', 'quit(2)')}, 'cannot read a unique ClickStack team'),
            'two team keys': ({'team': Result((), 0, 'RESULT ' + json.dumps({'keys': [KEY, OTHER]}))},
                              'cannot read a unique ClickStack team'),
            'registration open': ({'installation': Result((), 0, 'RESULT ' + json.dumps(
                {'status': 200, 'body': {'isTeamExisting': False}}))}, 'registration is open'),
            'api unreachable': ({'installation': Result((), 1, '', 'no pod')}, 'cannot ask the HyperDX API'),
            'invalid base64': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(KEY) + '!'}}}, 'invalid base64'),
            'different key': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(OTHER)}}}, 'does not match'),
            'double encoded': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(b64(KEY))}}}, 'does not match'),
            'trailing newline': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(KEY + '\n')}}}, 'does not match'),
            'telemetry ttl drift': ({'ttl30': Result((), 0, '8\t9\n')}, '1 of 9 telemetry tables have no 30-day TTL'),
            'no telemetry tables': ({'ttl30': Result((), 0, '0\t0\n')}, 'have no 30-day TTL'),
            'clickhouse down': ({'ttl30': Result((), 1, '', 'connection refused')}, 'cannot read ClickHouse telemetry'),
            'system logs untimed': ({'untimed': Result((), 0, '3\n')}, '3 ClickHouse system log table(s) have no TTL'),
            'merges failing': ({'merges': Result((), 0, '3765\tsystem.metric_log\n')},
                               '3765 ClickHouse merge(s) failed in the last hour (for example system.metric_log)'),
            'part_log unreadable': ({'merges': Result((), 1, '', 'UNKNOWN_TABLE')}, 'cannot read ClickHouse merge results'),
            'pv delete': ({'pv': {'spec': {'persistentVolumeReclaimPolicy': 'Delete'}}}, 'data volume is not Retain'),
            'pvc missing': ({'pvc': Result((), 1, '', 'NotFound')}, 'data volume is not Retain'),

        }
        for label, (overrides, expected) in cases.items():
            with self.subTest(label):
                code, report, text = run(healthy(**overrides))
                self.assertEqual(code, 1, text)
                self.assertTrue(any(line.startswith('[BAD]') and expected in line for line in report.lines), text)
                self.assertNotIn('Validation passed.', text)
                self.assertNoKeys(text)

    def test_mismatch_prints_fix_hint(self):
        _, report, _ = run(healthy(secret={'data': {'CLICKSTACK_INGESTION_KEY': b64(OTHER)}}))
        self.assertTrue(any('make clickstack-bootstrap' in line for line in report.lines))

    def test_team_key_failure_output_never_leaks(self):
        runner = healthy(team=Result((), 1, KEY, f'error near {KEY}'))
        code, _, text = run(runner)
        self.assertEqual(code, 1)
        self.assertNoKeys(text)

    def test_unreachable_cluster_stops_early(self):
        runner = healthy(version=Result((), 1, '', 'connection refused'))
        code, report, text = run(runner)
        self.assertEqual(code, 1)
        self.assertIn('kubectl cannot reach a cluster', text)
        self.assertEqual(runner.calls, [('kubectl', 'get', '--raw=/version')])

    def test_every_call_is_read_only(self):
        """kubectl only gets or execs, and what an exec runs is allowlisted: exec itself can write."""
        runner = healthy()
        run(runner)
        verbs = {c[c.index('get') if 'get' in c else c.index('exec')] for c in runner.calls if c[0] == 'kubectl'}
        self.assertLessEqual(verbs, {'get', 'exec'})
        read_only_scripts = (clickstack.TEAM_KEYS,)
        self.assertTrue(runner.stdin, 'verify-platform sends nothing into pods? the allowlist would be untested')
        for args, program in runner.stdin:
            if clickstack.MONGO_POD in args:
                self.assertTrue(program.rstrip('\n').endswith(read_only_scripts), f'unlisted mongosh script: {program}')
            else:
                request = json.loads(program.split(', ', 1)[1].split(');\n', 1)[0])
                self.assertEqual(request['method'], 'GET', f'HyperDX API call is not a GET: {program}')


class FluxControllerTests(unittest.TestCase):
    def test_flux_controllers_without_git_settings_warn(self):
        plain = {'metadata': {'labels': {'app.kubernetes.io/version': 'v0.0.1'}},
                 'spec': {'template': {'spec': {'containers': [{'args': ['--log-level=info']}]}}}}
        code, report, text = run(healthy(**{'kustomize-controller': plain}))
        self.assertEqual(code, 0, text)
        warning = next(line for line in report.lines if line.startswith('[WARN] kustomize-controller'))
        self.assertIn('is v0.0.1 with 0/1 Git settings', warning)
        self.assertIn('make flux-install', warning)

    def test_unreadable_flux_controller_fails(self):
        code, report, text = run(healthy(**{'kustomize-controller': Result((), 0, '')}))
        self.assertEqual(code, 1, text)
        self.assertIn('[BAD] cannot read flux-system/kustomize-controller (kubectl -n flux-system get deploy kustomize-controller)', report.lines)



class AllowedChecksTests(unittest.TestCase):
    def test_the_cluster_checks_are_swhurl_v2_verify_with_the_named_binary_kubeconfig_checkout_and_tag(self):
        runner = healthy()
        run(runner)
        self.assertIn(('swhurl-v2', 'verify', '--scope', 'platform', '--kubeconfig', '/nonexistent/kubeconfig.json',
                       '--root', str(ROOT), '--console-tag', 'src-abc', '--source', 'verify-platform', '--json'), runner.calls)
        names = {check.name for check in verify.CHECKS}
        self.assertEqual(len(names), len(verify.CHECKS))
        self.assertFalse(names & {name for name, _, _ in verify.CLUSTER_CHECKS}, 'a check is either Go\'s or Python\'s')
        self.assertEqual(frozenset().union(*(check.needs for check in verify.CHECKS)), {'cluster', 'secret', 'exec', 'host'})
        self.assertLessEqual({read for _, reads, _ in verify.CLUSTER_CHECKS for read in reads.split()}, verify.READS.keys())

    def test_without_a_console_tag_none_is_passed_and_go_says_it_was_not_supplied(self):
        runner = healthy()
        with mock.patch.dict('os.environ', {'SWHURL_V2': 'swhurl-v2', 'SWHURL_V2_KUBECONFIG': 'k.json', 'SWHURL_V2_CONSOLE_TAG': ''}):
            verify.check_cluster(runner, Report(io.StringIO()))
        (call,) = [c for c in runner.calls if c[:2] == ('swhurl-v2', 'verify')]
        self.assertNotIn('--console-tag', call)

    def test_the_operations_table_names_go_s_checks_in_go_s_order(self):
        names = []
        for path, function in (('v2/observe/platform.go', 'func PlatformChecks('),
                               ('v2/observe/platform_scope.go', 'func PlatformScopeChecks(')):
            source = (ROOT / path).read_text()
            body = source[source.index(function):]
            names += re.findall(r'\{"([a-z-]+)", "[^"]+", platform', body[:body.index('\n}\n')])
        self.assertEqual(names, [name for name, _, _ in verify.CLUSTER_CHECKS])

    def test_a_binary_that_cannot_start_is_a_failed_check(self):
        runner = FakeRunner().on('swhurl-v2', 'verify', handler=lambda args, _: (_ for _ in ()).throw(
            CommandError(args, 'missing required command: swhurl-v2')))
        with mock.patch.dict('os.environ', {'SWHURL_V2': 'swhurl-v2', 'SWHURL_V2_KUBECONFIG': 'k.json'}):
            report = Report(io.StringIO())
            verify.check_cluster(runner, report)
        self.assertEqual(report.failures, 1)
        self.assertIn('swhurl-v2 verify did not run: missing required command', report.lines[-1])

    def test_without_the_binary_the_cluster_checks_fail(self):
        runner = healthy()
        with mock.patch.dict('os.environ', {'SWHURL_V2': ''}):
            report = Report(io.StringIO())
            verify.check_cluster(runner, report)
        self.assertEqual(report.failures, 1)
        self.assertIn('swhurl-v2 verify did not run', report.lines[-1])

    def test_the_operations_page_lists_the_checks_as_the_registry_does(self):
        page = (ROOT / 'docs/operations.md').read_text()
        self.assertIn(verify.table(), page, 'docs/operations.md: replace the table under "Check health" with:\n' + verify.table())

    def test_the_command_loads_with_the_system_interpreter(self):
        """make runs it without uv: no check's module may need httpx or ruamel to load."""
        code = "import sys; sys.modules['httpx'] = sys.modules['ruamel'] = None; import swhurl.verify, swhurl.validate"
        loaded = subprocess.run([sys.executable, '-c', code], env=os.environ | {'PYTHONPATH': str(ROOT / 'tools')},
                                capture_output=True, text=True)
        self.assertEqual(loaded.returncode, 0, loaded.stderr)

    def test_entries_carry_section_and_level(self):
        _, report, _ = run(healthy(**{'v2-verify': verify_report(
            ('flux-units', 'Flux Kustomizations', [('ok', 'a')]), ('ingress', 'Ingress', [('bad', 'Traefik does not redirect HTTP to HTTPS')]))}))
        bad = [e for e in report.entries if e.level == 'bad']
        self.assertEqual([(e.section, e.message[:28]) for e in bad], [('Ingress', 'Traefik does not redirect HT')])


class VerifyConfigTests(unittest.TestCase):
    def make_root(self, *, secret=True, settings='  BASE_DOMAIN: example.test\n  CERT_ISSUER: letsencrypt-prod\n'):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root)
        (root / 'clusters/home/flux-system/sources').mkdir(parents=True)
        (root / 'clusters/home/flux-system/kustomizations.yaml').write_text('')
        (root / 'clusters/home/platform.yaml').write_text(
            'apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata: {name: homelab-svc}\n'
            'spec: {path: ./svc, decryption: {provider: sops}}\n')
        (root / 'svc').mkdir()
        if secret:
            (root / 'svc/secret.sops.yaml').write_text('kind: Secret\n')
        (root / platform.SETTINGS).write_text(
            'apiVersion: v1\nkind: ConfigMap\nmetadata: {name: platform-settings}\ndata:\n' + settings)
        return root

    def run_config(self, root=None):
        out = io.StringIO()
        try:
            validate.check_config(Report(out), **({'root': root} if root else {}))
        except validate.ValidationError as error:
            return 1, str(error)
        return 0, out.getvalue()

    def test_passes_on_this_repo(self):
        self.assertEqual(self.run_config()[0], 0)

    def test_passes_on_a_minimal_repo(self):
        self.assertEqual(self.run_config(self.make_root())[0], 0)

    def test_required_secrets_come_from_decrypting_units(self):
        code, out = self.run_config(self.make_root(secret=False))
        self.assertEqual(code, 1)
        self.assertIn('homelab-svc decrypts SOPS but its path has no *.sops.yaml Secret', out)

    def test_missing_settings_fail(self):
        code, out = self.run_config(self.make_root(settings='  CERT_ISSUER: letsencrypt-prod\n'))
        self.assertEqual(code, 1)
        self.assertIn('BASE_DOMAIN missing', out)


if __name__ == '__main__':
    unittest.main()


class NotificationHeartbeatHostVerifyTests(unittest.TestCase):
    def verify(self, *, age=60, timer='active', result='success', status='0', timestamp=True):
        now = dt.datetime.now(dt.UTC)
        exit_time = (dt.datetime.fromtimestamp(now.timestamp()-age).astimezone().strftime('%a %Y-%m-%d %H:%M:%S %Z')
                     if timestamp else '')
        details = (f'Result={result}\nExecMainStatus={status}\n'
                   f'ExecMainExitTimestamp={exit_time}\n')
        runner = FakeRunner().on('systemctl', 'is-active', stdout=timer+'\n').on(
            'systemctl', 'show', stdout=details)
        out = io.StringIO()
        report = Report(out)
        notification_checks.check_notification_heartbeat(runner, report, now)
        return report.exit_code(), out.getvalue()

    def test_active_timer_and_recent_success_pass(self):
        code, output = self.verify()
        self.assertEqual(code, 0, output)
        self.assertIn('succeeded within 15 minutes', output)

    def test_inactive_timer_failed_run_missing_timestamp_and_stale_run_fail(self):
        for options in ({'timer': 'inactive'}, {'result': 'exit-code'}, {'status': '1'},
                        {'timestamp': False}, {'age': 16*60}):
            with self.subTest(options=options):
                code, output = self.verify(**options)
                self.assertEqual(code, 1, output)

    def test_check_is_registered_as_host_scoped(self):
        check = next(c for c in verify.CHECKS if c.name == 'notification-heartbeat')
        self.assertEqual(check.needs, frozenset({'host'}))
