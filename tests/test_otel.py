"""check-otel: render, stub pod-only inputs, validate with the collector, flag deprecated names; offline."""
import hashlib
import io
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import yaml

from swhurl import otel
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

CONFIG = {'receivers': {'kubeletstats': {'auth_type': 'serviceAccount'}, 'hostmetrics': {'root_path': '/hostfs'},
                        'file_log/host-timers': {}, 'otlp': {}},
          'processors': {'k8s_attributes': {}, 'batch': {}}, 'exporters': {'otlphttp': {}},
          'service': {'pipelines': {'metrics': {'receivers': ['kubeletstats'], 'processors': ['k8s_attributes']}}}}
KNOWN = {'receivers': {'kubelet_stats', 'host_metrics', 'file_log', 'otlp'}, 'processors': {'k8s_attributes', 'batch'},
         'exporters': {'otlphttp'}, 'extensions': set(), 'connectors': set()}


def rendered(image='otel/opentelemetry-collector-k8s:0.161.0'):
    return yaml.safe_dump_all([
        {'kind': 'ConfigMap', 'metadata': {'name': 'x'}, 'data': {'relay': yaml.safe_dump(CONFIG)}},
        {'kind': 'DaemonSet', 'metadata': {'name': 'x'}, 'spec': {'template': {'spec': {'containers': [
            {'image': image, 'env': [{'name': 'GOMEMLIMIT', 'value': '152MiB'},
                                     {'name': 'MY_POD_IP', 'valueFrom': {'fieldRef': {'fieldPath': 'status.podIP'}}}]}]}}}}])


class OtelCheckTests(unittest.TestCase):
    def test_render_reads_config_env_and_version(self):
        runner = FakeRunner().on('helm', 'template', stdout=rendered())
        release = {'metadata': {'name': 'otel', 'namespace': 'logging'}, 'spec': {'values': {}, 'chart': {'spec': {
            'chart': 'opentelemetry-collector', 'version': '0.174.0', 'sourceRef': {'name': 'open-telemetry'}}}}}
        with mock.patch.object(otel.policy, 'chart_dir', return_value=Path('/chart')):
            config, env, version = otel.render(runner, release)
        self.assertEqual((config, version), (CONFIG, '0.161.0'))
        self.assertEqual(env, {'GOMEMLIMIT': '152MiB', 'MY_POD_IP': otel.PLACEHOLDER})

    def test_validation_stubs_only_pod_inputs(self):
        seen = {}

        def validate(args, _):
            path = args[-1].removeprefix('--config=file:')
            seen['config'] = yaml.safe_load(Path(path).read_text())
            seen['root_exists'] = Path(seen['config']['receivers']['hostmetrics']['root_path']).is_dir()
            return Result(args, 0)
        runner = FakeRunner().on('/bin/otelcol-k8s', 'validate', handler=validate)
        config = yaml.safe_load(yaml.safe_dump(CONFIG))
        self.assertEqual(otel.validate(runner, Path('/bin/otelcol-k8s'), config, {'A': 'b'}), '')
        receivers = seen['config']['receivers']
        self.assertEqual(receivers['kubeletstats']['auth_type'], 'none')
        self.assertTrue(seen['root_exists'])
        self.assertEqual(seen['config']['service'], CONFIG['service'])

    def test_rejection_returns_the_collectors_message(self):
        runner = FakeRunner().on('/c', 'validate', returncode=1, stderr='Error: references processor "x" which is not configured')
        self.assertIn('not configured', otel.validate(runner, Path('/c'), {}, {}))

    def test_validation_uses_the_deployments_feature_gates(self):
        runner = FakeRunner().on('/c', 'validate')
        otel.validate(runner, Path('/c'), {}, {}, ['--feature-gates=ottl.functions.enableLambda', '--unrelated'])
        args = runner.calls[0]
        self.assertIn('--feature-gates=ottl.functions.enableLambda', args)
        self.assertNotIn('--unrelated', args)

    def test_deprecated_names_with_hints(self):
        self.assertEqual(otel.deprecated_names(CONFIG, KNOWN),
                         ['receivers kubeletstats (now kubelet_stats)', 'receivers hostmetrics (now host_metrics)'])

    def test_collector_download_is_verified_and_cached(self):
        with tempfile.TemporaryDirectory() as tmp:
            payload = io.BytesIO()
            with tarfile.open(fileobj=payload, mode='w:gz') as tar:
                info = tarfile.TarInfo('otelcol-k8s')
                info.size, info.mode = 3, 0o755
                tar.addfile(info, io.BytesIO(b'bin'))
            archive = payload.getvalue()

            def download(args, _):
                Path(args[args.index('--output') + 1]).write_bytes(archive)
                return Result(args, 0)

            def extract(args, _):
                with tarfile.open(args[2]) as tar:
                    tar.extractall(args[4], filter='data')
                return Result(args, 0)
            for checksum, ok in ((hashlib.sha256(archive).hexdigest(), True), ('0' * 64, False)):
                cache = Path(tmp) / checksum[:4]
                runner = (FakeRunner().on('curl', '--fail', '--silent', '--show-error', '--location', '--output', handler=download)
                          .on('curl', stdout=f'{checksum}  otelcol-k8s.tar.gz\n').on('tar', handler=extract))
                with self.subTest(ok=ok):
                    if ok:
                        binary = otel.collector(runner, '0.161.0', cache)
                        self.assertEqual(binary.read_bytes(), b'bin')
                        calls = len(runner.calls)
                        otel.collector(runner, '0.161.0', cache)
                        self.assertEqual(len(runner.calls), calls, 'cached: no second download')
                    else:
                        with self.assertRaisesRegex(otel.OtelError, 'does not match'):
                            otel.collector(runner, '0.161.0', cache)
                        self.assertFalse(list(cache.rglob('*.tar.gz')), 'a bad archive is removed')
                        self.assertFalse((cache / '0.161.0' / 'otelcol-k8s').exists())

    def test_check_reports_ok_bad_and_warnings(self):
        report = Report(io.StringIO())
        with mock.patch.object(otel, 'render', return_value=(CONFIG, {}, '0.161.0')), \
             mock.patch.object(otel, 'collector', return_value=Path('/c')), \
             mock.patch.object(otel, 'canonical_names', return_value=KNOWN), \
             mock.patch.object(otel, 'validate', side_effect=['', 'Error: bad']):
            code = otel.check(FakeRunner(), report)
        self.assertEqual(code, 1)
        levels = [(e.level, e.message[:30]) for e in report.entries]
        self.assertIn(('bad', 'otelcol-k8s 0.161.0 rejects th'), levels)
        self.assertEqual(sum(1 for level, _ in levels if level == 'warn'), 2)


if __name__ == '__main__':
    unittest.main()
