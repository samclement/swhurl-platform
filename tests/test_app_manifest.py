"""swhurl.yaml: the schema, its mapping to app-new defaults, and reading it from GitHub; offline."""
import io
import json
import tempfile
import unittest
import urllib.error
from contextlib import redirect_stderr
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import contract
from swhurl.apps import new as app_new
from swhurl.apps.contract import ManifestError, manifest_defaults

WEB = {'version': 1, 'stack': 'typescript', 'kind': 'web', 'port': 8080, 'healthPath': '/healthz', 'uid': 65532,
       'telemetry': 'otlp', 'autoDeploy': True}


class ManifestTests(unittest.TestCase):
    def test_a_template_manifest_maps_to_app_new_defaults(self):
        self.assertEqual(manifest_defaults(WEB), {
            'kind': 'web', 'exposure': 'authenticated-web', 'port': 8080, 'health_path': '/healthz',
            'uid': 65532, 'otlp': True, 'auto_deploy': True})
        self.assertEqual(manifest_defaults({'version': 1, 'kind': 'worker', 'telemetry': 'otlp', 'autoDeploy': True}),
                         {'kind': 'worker', 'exposure': 'private', 'uid': 65532, 'otlp': True, 'auto_deploy': True})
        for name in ('web', 'worker'):  # the fixtures other tests generate instances from
            doc = yaml.safe_load((ROOT / f'tests/fixtures/manifests/{name}.yaml').read_text())
            self.assertEqual((manifest_defaults(doc)['kind'], manifest_defaults(doc)['auto_deploy']), (name, True))

    def test_every_capability_maps_to_its_app_new_option(self):
        doc = {'version': 1, 'kind': 'worker', 'database': 'sqlite', 'databaseSize': '2Gi',
               'secrets': ['API_TOKEN', 'DB_URL'], 'resources': {'cpu': '50m', 'memory': '256Mi', 'memoryLimit': '512Mi'},
               'team': 'payments'}
        self.assertEqual(manifest_defaults(doc), {
            'kind': 'worker', 'uid': 65532, 'otlp': False, 'auto_deploy': False, 'exposure': 'private',
            'database': 'sqlite', 'database_size': '2Gi', 'secret_keys': ['API_TOKEN', 'DB_URL'],
            'cpu': '50m', 'memory': '256Mi', 'memory_limit': '512Mi', 'team': 'payments'})

    def test_every_web_app_gets_the_same_startup_allowance(self):
        with self.assertRaisesRegex(ManifestError, 'unknown field.*startupSeconds'):
            manifest_defaults({**WEB, 'startupSeconds': 120})
        fail_after = int(contract.FAIL_AFTER.removesuffix('m')) * 60
        self.assertLessEqual(contract.STARTUP_SECONDS, fail_after - 60, 'Helm needs time to pull the image too')
        with tempfile.TemporaryDirectory() as root:
            (Path(root) / 'clusters/home').mkdir(parents=True)
            manifest = Path(root) / 'swhurl.yaml'
            manifest.write_text(yaml.safe_dump(WEB))
            args = app_new.parse_args(['kt', '--manifest', str(manifest), '--env', 'staging', '--root', root,
                                       '--image', 'ghcr.io/me/kt:1-abcdef0@sha256:' + 'a' * 64, '--no-register'])
            app_new.generate(args, Path(root))
            release = yaml.safe_load((Path(root) / 'apps/kt/staging/helmrelease.yaml').read_text())
        probes = release['spec']['values']['controllers']['main']['containers']['main']['probes']
        self.assertEqual(probes['startup']['spec'], {'httpGet': {'path': '/healthz', 'port': 8080},
                                                     'periodSeconds': 5, 'failureThreshold': 24})
        self.assertNotIn('periodSeconds', probes['liveness']['spec'], 'liveness keeps its defaults')

    def test_refuses_what_the_platform_cannot_honour(self):
        cases = [
            ({**WEB, 'version': 2}, 'version must be 1'),
            ({k: v for k, v in WEB.items() if k != 'version'}, 'version must be 1'),
            ({**WEB, 'replicas': 3}, 'unknown field'),
            ({**WEB, 'kind': 'cron'}, 'kind must be one of'),
            ({'version': 1}, 'kind is required'),
            ({**WEB, 'healthPath': None}, 'healthPath is required'),
            ({'version': 1, 'kind': 'worker', 'port': 8080}, 'kind web only'),
            ({**WEB, 'port': '8080'}, 'port must be int'),
            ({**WEB, 'uid': True}, 'uid must be int'),
            ({**WEB, 'database': 'postgres'}, 'database must be one of'),
            ({**WEB, 'databaseSize': '1Gi'}, 'databaseSize needs database'),
            ({**WEB, 'secrets': ['api-token']}, 'environment variable names'),
            ({**WEB, 'secrets': ['OTEL_SERVICE_NAME']}, 'reserved for the platform: OTEL_SERVICE_NAME'),
            ({**WEB, 'secrets': ['DATABASE_PATH']}, 'reserved for the platform: DATABASE_PATH'),
            ({**WEB, 'secrets': ['A', 'A']}, 'secret keys repeated: A'),
            ({**WEB, 'resources': {'gpu': '1'}}, 'resources may set'),
            ({**WEB, 'resources': {'memory': 64}}, 'quantity string'),
            ({**WEB, 'telemetry': 'datadog'}, 'telemetry must be one of'),
            (['kind', 'web'], 'must be a mapping'),
        ]
        for doc, message in cases:
            with self.subTest(message=message), self.assertRaisesRegex(ManifestError, message):
                manifest_defaults(doc)


class FromRepoTests(unittest.TestCase):
    def setUp(self):
        self.requests = []

    def opener(self, text=None, status=None):
        def open_(request):
            self.requests.append(request)
            if status:
                raise urllib.error.HTTPError(request.full_url, status, 'Not Found', {}, None)
            return (text if text is not None else yaml.safe_dump(WEB)).encode()
        return open_

    def test_reads_the_manifest_at_a_ref_and_flags_still_win(self):
        args = app_new.parse_args(['notes', '--from-repo', 'samclement/notes@v1', '--env', 'staging',
                                   '--image', 'ghcr.io/samclement/notes:1-abc1234', '--no-otlp'], self.opener())
        (request,) = self.requests
        self.assertEqual(request.full_url, 'https://api.github.com/repos/samclement/notes/contents/swhurl.yaml?ref=v1')
        self.assertEqual(request.get_header('Accept'), 'application/vnd.github.raw+json')
        self.assertIsNone(request.get_header('Authorization'))
        self.assertEqual((args.kind, args.health_path, args.exposure, args.auto_deploy, args.otlp),
                         ('web', '/healthz', 'authenticated-web', True, False))

    def test_a_missing_manifest_or_bad_spec_is_a_clear_error(self):
        for argv, opener, message in (
                (['--from-repo', 'samclement/notes'], self.opener(status=404), 'has no swhurl.yaml, or is private'),
                (['--from-repo', 'notes'], self.opener(), 'OWNER/REPO'),
                (['--from-repo', 'samclement/notes'], self.opener('version: 1\nkind: [web'), 'not valid YAML'),
                (['--from-repo', 'samclement/notes'], self.opener(json.dumps({'version': 1, 'kind': 'web'})),
                 'samclement/notes:swhurl.yaml: healthPath is required')):
            with self.subTest(message=message):
                err = io.StringIO()
                with redirect_stderr(err):
                    code = app_new.main(['notes', '--env', 'staging', '--image', 'x:1', *argv], opener)
                self.assertEqual(code, 2)
                self.assertIn(message, err.getvalue())

    def test_a_local_manifest_is_read_and_cannot_be_combined_with_a_repository(self):
        with tempfile.NamedTemporaryFile('w', suffix='.yaml') as handle:
            handle.write(yaml.safe_dump({**WEB, 'kind': 'worker', 'port': None, 'healthPath': None}))
            handle.flush()
            args = app_new.parse_args(['w', '--manifest', handle.name, '--env', 'staging', '--image', 'x:1'])
            self.assertEqual((args.kind, args.exposure), ('worker', 'private'))
            with self.assertRaisesRegex(app_new.GenerationError, 'cannot be combined'):
                app_new.parse_args(['w', '--manifest', handle.name, '--from-repo', 'samclement/w', '--env', 'staging',
                                    '--image', 'x:1'])
        err = io.StringIO()
        with redirect_stderr(err):
            self.assertEqual(app_new.main(['w', '--manifest', str(Path('/nonexistent/swhurl.yaml')), '--env', 'staging',
                                           '--image', 'x:1']), 2)
        self.assertIn('cannot read', err.getvalue())


if __name__ == '__main__':
    unittest.main()
