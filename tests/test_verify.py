"""verify-platform and check-config: every branch, offline, with FakeRunner."""
import base64
import datetime as dt
import io
import json
import shutil
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest import mock

from swhurl import ROOT, clickstack, platform, verify
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

KEY = 'fixture-team-ingestion-key-0001'
CONSOLE_TAG = 'b63d2aff9dfd4cf9df0307e382038dcbd15e29ed'
TOKEN = 'github_pat_fixture_token_0001'
OTHER = 'fixture-other-ingestion-key-9999'


def b64(value: bytes | str) -> str:
    return base64.b64encode(value.encode() if isinstance(value, str) else value).decode()


def fresh_backup(hours=1):
    taken = dt.datetime.now(dt.UTC) - dt.timedelta(hours=hours)
    return f'clickstack-mongodb-{taken:%Y%m%dT%H%M%SZ}.archive.gz.age'


def healthy(**overrides):
    """A FakeRunner answering every verify-platform call for a healthy cluster."""
    responses = {
        'version': Result((), 0, '{}'),
        'units': {'items': [
            {'metadata': {'name': 'homelab-b'}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}},
            {'metadata': {'name': 'homelab-a'}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}},
        ]},
        'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(KEY)}},
        'mongouri': {'data': {'connectionString.standard': b64('mongodb://u:p@db/hyperdx')}},
        'team': Result((), 0, 'Warning: EACCES\nRESULT ' + json.dumps({'keys': [KEY]}) + '\n'),
        'installation': Result((), 0, 'RESULT ' + json.dumps({'status': 200, 'body': {'isTeamExisting': True}}) + '\n'),
        'traefik': {'spec': {'template': {'spec': {'containers': [
            {'args': ['--entryPoints.web.http.redirections.entryPoint.scheme=https']}]}}}},
        'ttl30': Result((), 0, '9\t9\n'),
        'untimed': Result((), 0, '0\n'),
        'pvc': {'spec': {'volumeName': 'pv-1'}},
        'pv': {'spec': {'persistentVolumeReclaimPolicy': 'Retain'}},
        'remote': [f'clickstack-mongodb/{fresh_backup()}', f'clickstack-mongodb/{fresh_backup()[:-15]}.json'],
        'console': {'spec': {'values': {'controllers': {'main': {'containers': {'main': {'image': {
            'tag': CONSOLE_TAG}}}}}}}},
        'git-diff': Result((), 0),
        'token': {'data': {'GITHUB_TOKEN': b64(TOKEN)}},
        'github': Result((), 0, 'HTTP/2 200\r\ngithub-authentication-token-expiration: 2099-01-01 00:00:00 UTC\r\n\r\n'),
    }
    responses.update(overrides)

    def answer(key):
        def handler(args, _input):
            value = responses[key]
            if isinstance(value, Result):
                return Result(args, value.returncode, value.stdout, value.stderr)
            return Result(args, 0, json.dumps(value))
        return handler

    def clickhouse(args, _input):
        return answer('ttl30' if 'toIntervalDay(30)' in args[-1] else 'untimed')(args, _input)

    stdin = []  # every program sent into a pod (mongosh scripts, API requests)

    def recorded(key):
        def handler(args, program):
            stdin.append((args, program))
            return answer(key)(args, program)
        return handler

    runner = FakeRunner()
    runner.stdin = stdin
    runner.github = []  # (args, stdin) of each GitHub API call

    def github(args, config):
        runner.github.append((args, config))
        return answer('github')(args, config)
    return (runner
            .on('kubectl', 'get', '--raw=/version', handler=answer('version'))
            .on('kubectl', '-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io',
                handler=answer('units'))
            .on('kubectl', '-n', 'logging', 'get', 'secret', 'clickstack-ingestion-key', handler=answer('secret'))
            .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.MONGO_URI_SECRET, handler=answer('mongouri'))
            .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.MONGO_POD, handler=recorded('team'))
            .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.APP, handler=recorded('installation'))
            .on('kubectl', '-n', 'kube-system', 'get', 'deploy', 'traefik', handler=answer('traefik'))
            .on('kubectl', '-n', 'observability', 'exec', clickstack.CLICKHOUSE_POD, handler=clickhouse)
            .on('kubectl', '-n', 'observability', 'get', 'pvc', handler=answer('pvc'))
            .on('kubectl', 'get', 'pv', handler=answer('pv'))
            .on('aws', 's3api', 'list-objects-v2', handler=answer('remote'))
            .on('kubectl', '-n', 'console', 'get', 'helmrelease', 'console', handler=answer('console'))
            .on('git', '-C', str(ROOT), 'diff', '--quiet', CONSOLE_TAG, 'HEAD', '--', handler=answer('git-diff'))
            .on('kubectl', '-n', 'console', 'get', 'secret', 'console-github', handler=answer('token'))
            .on('curl', handler=github))


def run(runner, **kwargs):
    out, err = io.StringIO(), io.StringIO()
    report = Report(out)
    backups = Path(tempfile.mkdtemp())
    (backups / fresh_backup()).write_bytes(b'x')
    env = {'BACKUP_DIR': str(backups), 'BACKUP_S3_URI': 's3://bucket/clickstack-mongodb/'}
    try:
        with redirect_stderr(err), mock.patch.dict('os.environ', env):
            code = verify.verify_platform(runner, report, **kwargs)
    finally:
        shutil.rmtree(backups)
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
                         ['\n== Flux Kustomizations ==', '\n== Runtime Secrets ==', '\n== Ingestion Key Sync ==',
                          '\n== ClickStack Sign-up ==', '\n== Ingress ==', '\n== Retention ==', '\n== Backups ==',
                          '\n== Console ==', '\n== Console GitHub Token =='])
        self.assertEqual(report.failures, 0)
        self.assertTrue(text.rstrip().endswith('Validation passed.'))
        self.assertNoKeys(text)

    def test_each_failure_is_reported_and_fails(self):
        cases = {
            'unit not ready': ({'units': {'items': [{'metadata': {'name': 'homelab-x'}, 'status': {'conditions': [
                {'type': 'Ready', 'status': 'False', 'message': 'dependency not ready'}]}}]}},
                'homelab-x is not Ready: dependency not ready'),
            'no units': ({'units': {'items': []}}, 'no Flux kustomizations found'),
            'units unreadable': ({'units': Result((), 1, '', 'forbidden')}, 'could not read Flux kustomizations'),
            'secret missing': ({'secret': {'data': {}}}, 'CLICKSTACK_INGESTION_KEY is empty'),
            'secret unreadable': ({'secret': Result((), 1, '', 'NotFound')}, 'CLICKSTACK_INGESTION_KEY is empty'),
            'team key unreadable': ({'team': Result((), 2, '', 'quit(2)')}, 'cannot read a unique ClickStack team'),
            'two team keys': ({'team': Result((), 0, 'RESULT ' + json.dumps({'keys': [KEY, OTHER]}))},
                              'cannot read a unique ClickStack team'),
            'registration open': ({'installation': Result((), 0, 'RESULT ' + json.dumps(
                {'status': 200, 'body': {'isTeamExisting': False}}))}, 'registration is open'),
            'api unreachable': ({'installation': Result((), 1, '', 'no pod')}, 'could not ask the HyperDX API'),
            'invalid base64': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(KEY) + '!'}}}, 'invalid base64'),
            'different key': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(OTHER)}}}, 'does not match'),
            'double encoded': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(b64(KEY))}}}, 'does not match'),
            'trailing newline': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(KEY + '\n')}}}, 'does not match'),
            'no redirect': ({'traefik': {'spec': {'template': {'spec': {'containers': [{'args': []}]}}}}},
                            'does not redirect HTTP to HTTPS'),
            'telemetry ttl drift': ({'ttl30': Result((), 0, '8\t9\n')}, '8 of 9 have it'),
            'no telemetry tables': ({'ttl30': Result((), 0, '0\t0\n')}, 'without a 30-day TTL'),
            'clickhouse down': ({'ttl30': Result((), 1, '', 'connection refused')}, 'could not read ClickHouse telemetry'),
            'system logs untimed': ({'untimed': Result((), 0, '3\n')}, '3 ClickHouse system log table(s) have no TTL'),
            'pv delete': ({'pv': {'spec': {'persistentVolumeReclaimPolicy': 'Delete'}}}, 'data volume is not Retain'),
            'pvc missing': ({'pvc': Result((), 1, '', 'NotFound')}, 'data volume is not Retain'),
            'console missing': ({'console': Result((), 1, '', 'NotFound')}, 'cannot read the console HelmRelease'),
            'token placeholder': ({'token': {'data': {'GITHUB_TOKEN': b64('REPLACE_ME')}}}, 'is not set'),
            'token secret missing': ({'token': Result((), 1, '', 'NotFound')}, 'is not set'),
            'token rejected': ({'github': Result((), 0, 'HTTP/2 401\r\n\r\n')}, 'did not accept the console token'),
            'github unreachable': ({'github': Result((), 6, '', 'could not resolve')}, 'did not accept'),
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
        for args, _ in runner.github:
            self.assertFalse({'--request', '-X', '--data', '--data-binary'} & set(args), f'GitHub call is not a GET: {args}')
        read_only_scripts = (clickstack.TEAM_KEYS,)
        self.assertTrue(runner.stdin, 'verify-platform sends nothing into pods? the allowlist would be untested')
        for args, program in runner.stdin:
            if clickstack.MONGO_POD in args:
                self.assertTrue(program.rstrip('\n').endswith(read_only_scripts), f'unlisted mongosh script: {program}')
            else:
                request = json.loads(program.split(', ', 1)[1].split(');\n', 1)[0])
                self.assertEqual(request['method'], 'GET', f'HyperDX API call is not a GET: {program}')


class ConsoleCheckTests(unittest.TestCase):
    def test_current_image_passes_and_compares_only_image_inputs(self):
        runner = healthy()
        _, report, _ = run(runner)
        self.assertIn('[OK] console image b63d2af is built from the current tooling', report.lines)
        diff = next(c for c in runner.calls if c[0] == 'git')
        self.assertEqual(diff[diff.index('--') + 1:], platform.CONSOLE_IMAGE_INPUTS)

    def test_changed_or_unknown_tooling_warns_without_failing(self):
        for code, expected in ((1, 'tooling changed since console image b63d2af'), (128, 'cannot compare')):
            with self.subTest(code=code):
                exit_code, report, text = run(healthy(**{'git-diff': Result((), code)}))
                self.assertEqual(exit_code, 0, text)
                self.assertTrue(any(line.startswith('[WARN]') and expected in line for line in report.lines), text)

    def test_publish_workflow_hashes_the_same_inputs_in_the_same_order(self):
        workflow = (ROOT / '.github/workflows/publish-console.yml').read_text()
        listed = workflow.split('paths=(', 1)[1].split(')', 1)[0].split()
        self.assertEqual(tuple(listed), platform.CONSOLE_IMAGE_INPUTS, 'the order changes the hash')

    def test_content_tag_is_compared_exactly(self):
        for current, expected in (('src-abc', '[OK] console image src-abc is built from the current tooling'),
                                  ('src-new', '[WARN] tooling changed since console image src-abc')):
            with self.subTest(current=current):
                runner = healthy(console={'spec': {'values': {'controllers': {'main': {'containers': {'main': {
                    'image': {'tag': 'src-abc'}}}}}}}})
                with mock.patch.object(verify.images, 'content_tag', return_value=current):
                    _, report, _ = run(runner)
                self.assertTrue(any(line.startswith(expected) for line in report.lines), report.lines)


class ConsoleTokenTests(unittest.TestCase):
    def test_expiry_soon_warns_and_token_never_leaks(self):
        soon = (dt.datetime.now(dt.UTC) + dt.timedelta(days=5)).strftime('%Y-%m-%d %H:%M:%S UTC')
        runner = healthy(github=Result((), 0, f'HTTP/2 200\r\ngithub-authentication-token-expiration: {soon}\r\n'))
        code, report, text = run(runner)
        self.assertEqual(code, 0, text)
        self.assertTrue(any(line.startswith('[WARN]') and 'expires' in line for line in report.lines), text)
        self.assertNotIn(TOKEN, text)
        ((args, stdin),) = runner.github
        self.assertNotIn(TOKEN, ' '.join(args))
        self.assertEqual(stdin, f'header = "Authorization: Bearer {TOKEN}"\n')

    def test_no_expiry_passes(self):
        _, report, _ = run(healthy(github=Result((), 0, 'HTTP/2 200\r\n\r\n')))
        self.assertIn('[OK] GitHub accepts the console token (no expiry date)', report.lines)


class AllowedChecksTests(unittest.TestCase):
    def test_cluster_only_reads_no_secrets_execs_nothing_and_names_what_it_skipped(self):
        runner = healthy()
        code, report, text = run(runner, allowed=frozenset({'cluster'}))
        self.assertEqual(code, 0, text)
        self.assertEqual([e.section for e in report.entries if e.level != 'info'],
                         ['Flux Kustomizations'] * 2 + ['Ingress'])
        self.assertFalse([c for c in runner.calls if 'secret' in c or 'exec' in c or c[0] != 'kubectl'], runner.calls)
        self.assertIn('[INFO] skipped (need more than cluster): ingestion-key, registration, retention, backups, console, console-token',
                      report.lines)

    def test_every_check_names_only_known_needs(self):
        self.assertEqual(verify.NEEDS, {'cluster', 'secret', 'exec', 'host'})
        self.assertEqual(len({check.name for check in verify.CHECKS}), len(verify.CHECKS))

    def test_entries_carry_section_and_level(self):
        _, report, _ = run(healthy(traefik={'spec': {'template': {'spec': {'containers': [{'args': []}]}}}}))
        bad = [e for e in report.entries if e.level == 'bad']
        self.assertEqual([(e.section, e.message[:28]) for e in bad], [('Ingress', 'Traefik does not redirect HT')])


class BackupAgeTests(unittest.TestCase):
    NOW = dt.datetime(2026, 9, 29, 12, 0, tzinfo=dt.UTC)

    def check(self, local=(), remote=(), uri='s3://bucket/p/', remote_rc=0, max_hours=None):
        folder = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, folder)
        for name in local:
            (folder / name).write_bytes(b'x')
        runner = FakeRunner().on('aws', 's3api', 'list-objects-v2', returncode=remote_rc, stderr='AccessDenied',
                                 stdout=json.dumps([f'p/{n}' for n in remote]))
        env = {'BACKUP_DIR': str(folder), 'BACKUP_S3_URI': uri, **({'BACKUP_MAX_AGE_HOURS': max_hours} if max_hours else {})}
        report = Report(io.StringIO())
        verify.check_backups(runner, report, env, now=self.NOW)
        return report, runner

    def test_fresh_backups_pass(self):
        name = 'clickstack-mongodb-20260929T033000Z.archive.gz.age'
        report, _ = self.check(local=[name, 'clickstack-mongodb-20260929T033000Z.json'], remote=[name])
        self.assertEqual(report.failures, 0, report.lines)

    def test_stale_missing_or_unlistable_backups_fail(self):
        old = 'clickstack-mongodb-20260928T030000Z.archive.gz.age'
        new = 'clickstack-mongodb-20260929T033000Z.archive.gz.age'
        cases = {
            'local stale': ({'local': [old], 'remote': [new]}, 'is 33 h old'),
            'remote stale': ({'local': [new], 'remote': [old]}, 's3://bucket/p/ is 33 h old'),
            'none local': ({'remote': [new]}, 'no MongoDB backup in'),
            'metadata only': ({'local': ['clickstack-mongodb-20260929T033000Z.json'], 'remote': [new]}, 'no MongoDB backup'),
            'remote unlistable': ({'local': [new], 'remote_rc': 255}, 'cannot list backups in s3://bucket/p/'),
            'tighter limit': ({'local': [new], 'remote': [new], 'max_hours': '6'}, 'is 8 h old'),
        }
        for label, (kwargs, message) in cases.items():
            with self.subTest(label):
                report, _ = self.check(**kwargs)
                self.assertTrue(any(line.startswith('[BAD]') and message in line for line in report.lines), report.lines)

    def test_empty_uri_checks_only_local(self):
        report, runner = self.check(local=['clickstack-mongodb-20260929T033000Z.archive.gz.age'], uri='')
        self.assertEqual(report.failures, 0)
        self.assertEqual(runner.calls, [])


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
        with mock.patch('sys.stdout', out):
            code = verify.verify_config([], **({'root': root} if root else {}))
        return code, out.getvalue()

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
