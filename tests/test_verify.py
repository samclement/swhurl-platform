"""verify-platform and verify-config: every branch, offline, with FakeRunner."""
import base64
import io
import json
import sys
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'tools'))

from swhurl import verify  # noqa: E402
from swhurl.report import Report  # noqa: E402
from swhurl.run import FakeRunner, Result  # noqa: E402

KEY = 'fixture-team-ingestion-key-0001'
OTHER = 'fixture-other-ingestion-key-9999'


def b64(value: bytes | str) -> str:
    return base64.b64encode(value.encode() if isinstance(value, str) else value).decode()


def healthy(**overrides):
    """A FakeRunner answering every verify-platform call for a healthy cluster."""
    responses = {
        'version': Result((), 0, '{}'),
        'units': {'items': [
            {'metadata': {'name': 'homelab-b'}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}},
            {'metadata': {'name': 'homelab-a'}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}},
        ]},
        'secret': {'data': {'HYPERDX_API_KEY': b64(KEY)}},
        'team': Result((), 0, KEY + '\n'),
        'traefik': {'spec': {'template': {'spec': {'containers': [
            {'args': ['--entryPoints.web.http.redirections.entryPoint.scheme=https']}]}}}},
        'ttl30': Result((), 0, '9\t9\n'),
        'untimed': Result((), 0, '0\n'),
        'pvc': {'metadata': {'annotations': {'helm.sh/resource-policy': 'keep'}}, 'spec': {'volumeName': 'pv-1'}},
        'pv': {'spec': {'persistentVolumeReclaimPolicy': 'Retain'}},
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

    return (FakeRunner()
            .on('kubectl', 'get', '--raw=/version', handler=answer('version'))
            .on('kubectl', '-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io',
                handler=answer('units'))
            .on('kubectl', '-n', 'logging', 'get', 'secret', 'hyperdx-secret', handler=answer('secret'))
            .on('kubectl', '-n', 'observability', 'exec', 'deploy/clickstack-mongodb', handler=answer('team'))
            .on('kubectl', '-n', 'kube-system', 'get', 'deploy', 'traefik', handler=answer('traefik'))
            .on('kubectl', '-n', 'observability', 'exec', 'deploy/clickstack-clickhouse', handler=clickhouse)
            .on('kubectl', '-n', 'observability', 'get', 'pvc', handler=answer('pvc'))
            .on('kubectl', 'get', 'pv', handler=answer('pv')))


def run(runner):
    out, err = io.StringIO(), io.StringIO()
    report = Report(out)
    with redirect_stderr(err):
        code = verify.verify_platform(runner, report)
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
                          '\n== Ingress ==', '\n== Retention =='])
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
            'secret missing': ({'secret': {'data': {}}}, 'HYPERDX_API_KEY is empty'),
            'secret unreadable': ({'secret': Result((), 1, '', 'NotFound')}, 'HYPERDX_API_KEY is empty'),
            'team key unreadable': ({'team': Result((), 2, '', 'quit(2)')}, 'cannot read a unique ClickStack team'),
            'invalid base64': ({'secret': {'data': {'HYPERDX_API_KEY': b64(KEY) + '!'}}}, 'invalid base64'),
            'different key': ({'secret': {'data': {'HYPERDX_API_KEY': b64(OTHER)}}}, 'does not match'),
            'double encoded': ({'secret': {'data': {'HYPERDX_API_KEY': b64(b64(KEY))}}}, 'does not match'),
            'trailing newline': ({'secret': {'data': {'HYPERDX_API_KEY': b64(KEY + '\n')}}}, 'does not match'),
            'no redirect': ({'traefik': {'spec': {'template': {'spec': {'containers': [{'args': []}]}}}}},
                            'does not redirect HTTP to HTTPS'),
            'telemetry ttl drift': ({'ttl30': Result((), 0, '8\t9\n')}, '8 of 9 have it'),
            'no telemetry tables': ({'ttl30': Result((), 0, '0\t0\n')}, 'without a 30-day TTL'),
            'clickhouse down': ({'ttl30': Result((), 1, '', 'connection refused')}, 'could not read ClickHouse telemetry'),
            'system logs untimed': ({'untimed': Result((), 0, '3\n')}, '3 ClickHouse system log table(s) have no TTL'),
            'pv delete': ({'pv': {'spec': {'persistentVolumeReclaimPolicy': 'Delete'}}}, 'PV is not Retain'),
            'pvc missing': ({'pvc': Result((), 1, '', 'NotFound')}, 'PV is not Retain'),
            'pvc not kept': ({'pvc': {'metadata': {}, 'spec': {'volumeName': 'pv-1'}}}, 'lacks helm.sh/resource-policy'),
        }
        for label, (overrides, expected) in cases.items():
            with self.subTest(label):
                code, report, text = run(healthy(**overrides))
                self.assertEqual(code, 1, text)
                self.assertTrue(any(line.startswith('[BAD]') and expected in line for line in report.lines), text)
                self.assertNotIn('Validation passed.', text)
                self.assertNoKeys(text)

    def test_mismatch_prints_fix_hint(self):
        _, report, _ = run(healthy(secret={'data': {'HYPERDX_API_KEY': b64(OTHER)}}))
        self.assertTrue(any('exactly one base64 layer' in line for line in report.lines))

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
        runner = healthy()
        run(runner)
        verbs = {c[c.index('get') if 'get' in c else c.index('exec')] for c in runner.calls if c[0] == 'kubectl'}
        self.assertLessEqual(verbs, {'get', 'exec'})


class VerifyConfigTests(unittest.TestCase):
    def test_passes_on_this_repo(self):
        self.assertEqual(verify.verify_config([]), 0)

    def test_fails_on_missing_secret_file(self):
        with mock.patch.dict(verify.REQUIRED_SECRETS, {'no/such/secret.sops.yaml': 'fixture secret missing'}):
            out = io.StringIO()
            with mock.patch('sys.stdout', out):
                self.assertEqual(verify.verify_config([]), 1)
            self.assertIn('fixture secret missing', out.getvalue())


if __name__ == '__main__':
    unittest.main()
