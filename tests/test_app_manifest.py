"""swhurl.yaml: the schema, its mapping to app-new defaults, and reading it from GitHub; offline."""
import io
import json
import re
import tempfile
import unittest
import urllib.error
from contextlib import redirect_stderr
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import contract, declaration
from swhurl.apps import new as app_new
from swhurl.apps.declaration import DeclarationError

WEB = {'version': 1, 'stack': 'typescript', 'kind': 'web', 'port': 8080, 'healthPath': '/healthz', 'uid': 65532,
       'telemetry': 'otlp', 'autoDeploy': True}


def seed(doc):
    """app-new's defaults from a parsed swhurl.yaml."""
    return declaration.of(doc).seed


class ManifestTests(unittest.TestCase):
    def test_a_template_manifest_maps_to_app_new_defaults(self):
        self.assertEqual(seed(WEB), {
            'kind': 'web', 'exposure': 'authenticated-web', 'port': 8080, 'health_path': '/healthz',
            'uid': 65532, 'otlp': True, 'auto_deploy': True})
        self.assertEqual(seed({'version': 1, 'kind': 'worker', 'telemetry': 'otlp', 'autoDeploy': True}),
                         {'kind': 'worker', 'exposure': 'private', 'uid': 65532, 'otlp': True, 'auto_deploy': True})
        for name in ('web', 'worker'):  # the fixtures other tests generate instances from
            doc = yaml.safe_load((ROOT / f'tests/fixtures/manifests/{name}.yaml').read_text())
            self.assertEqual((seed(doc)['kind'], seed(doc)['auto_deploy']), (name, True))

    def test_every_capability_maps_to_its_app_new_option(self):
        doc = {'version': 1, 'kind': 'worker', 'database': 'sqlite', 'databaseSize': '2Gi',
               'secrets': ['API_TOKEN', 'DB_URL'], 'resources': {'cpu': '50m', 'memory': '256Mi', 'memoryLimit': '512Mi'},
               'team': 'payments', 'calls': ['orders', 'billing']}
        self.assertEqual(seed(doc), {
            'kind': 'worker', 'uid': 65532, 'otlp': False, 'auto_deploy': True, 'exposure': 'private',
            'database': 'sqlite', 'database_size': '2Gi', 'secret_keys': ['API_TOKEN', 'DB_URL'],
            'cpu': '50m', 'memory': '256Mi', 'memory_limit': '512Mi', 'team': 'payments', 'calls': ['orders', 'billing']})
        web = {'version': 1, 'kind': 'web', 'healthPath': '/healthz', 'provides': {'api': '/openapi.json'}}
        self.assertEqual(seed(web)['api_path'], '/openapi.json')
        seed({**web, 'provides': {'api': '/openapi.json', 'apiFile': 'openapi.json', 'eventsFile': 'src/main/resources/asyncapi.yaml'}})
        self.assertTrue(seed({**doc, 'provides': {'eventsFile': 'asyncapi.yaml'}})['offers'], 'app-new reads the documents')
        self.assertNotIn('offers', seed(web))
        for bad, message in (({**doc, 'provides': {'api': '/x'}}, 'kind web only'), ({**web, 'provides': {'api': 'x'}}, 'a path such as'),
                             ({**web, 'provides': {'events': []}}, 'apiFile and eventsFile only'),
                             ({**web, 'provides': {'apiFile': '../x'}}, 'a file in the repository, such as openapi.json'),
                             ({**web, 'provides': {'eventsFile': '/etc/x'}}, 'such as asyncapi.yaml'),
                             ({**doc, 'provides': {'apiFile': 'openapi.json'}}, 'provides.apiFile applies to kind web only'), ({**doc, 'calls': ['Orders']}, 'distinct app names'),
                             ({**doc, 'calls': ['a', 'a']}, 'distinct app names')):
            with self.subTest(bad=bad), self.assertRaisesRegex(DeclarationError, message):
                seed(bad)

    def test_first_problem_follows_validation_order_and_names_the_source(self):
        source = 'owner/app@revision:swhurl.yaml'
        scenarios = [
            ({'resources': {'memory': 64}, 'events': {'publish': ['Bad']}},
             'resources.memory must be a quantity string, for example "64Mi"'),
            ({'resources': {'gpu': '1', 'cpu': 1}}, 'resources may set cpu, memory, memoryLimit only'),
            ({'resources': {'memory': 64, 'cpu': 1}},
             'resources.cpu must be a quantity string, for example "64Mi"'),
            ({'events': {'publish': ['Bad']}, 'provides': {'api': 'relative'}},
             'events.publish lists event names (created); events.subscribe lists <app>.<event> (orders.created)'),
            ({'events': {'unknown': [], 'publish': ['Bad']}}, 'events may set publish and subscribe only'),
            ({'provides': {'api': 'relative', 'apiFile': '../bad'}, 'calls': ['Bad']},
             'provides may set api, a path such as /openapi.json that the app serves, and apiFile and eventsFile only'),
            ({'provides': {'eventsFile': '../events', 'apiFile': '../api'}},
             'provides.apiFile is a file in the repository, such as openapi.json'),
            ({'kind': 'worker', 'port': None, 'healthPath': None,
              'provides': {'apiFile': 'api.json', 'api': '/api'}}, 'provides.apiFile applies to kind web only'),
            ({'calls': ['Bad'], 'team': 1, 'stack': 1}, 'calls must be a list of distinct app names'),
            ({'uid': True, 'resources': {'memory': 64}}, 'uid must be int'),
        ]
        for fields, expected in scenarios:
            with self.subTest(fields=fields), self.assertRaises(DeclarationError) as error:
                declaration.of({**WEB, **fields}, source)
            self.assertEqual(str(error.exception), f'{source}: {expected}')

    def test_empty_nested_values_and_zero_defaults_keep_their_existing_meaning(self):
        expected = seed(WEB)
        for empty in (None, {}):
            with self.subTest(empty=empty):
                self.assertEqual(seed({**WEB, 'resources': empty, 'events': empty, 'provides': empty}), expected)
        self.assertEqual(seed({**WEB, 'resources': {'cpu': None}, 'events': {'publish': False, 'subscribe': ''},
                               'calls': [], 'uid': 0, 'port': 0}), expected)
        for field in ('resources', 'events', 'provides'):
            with self.subTest(field=field), self.assertRaisesRegex(DeclarationError, f'{field} must be dict'):
                seed({**WEB, field: False})

    def test_every_web_app_gets_the_same_startup_allowance(self):
        with self.assertRaisesRegex(DeclarationError, 'unknown field.*startupSeconds'):
            seed({**WEB, 'startupSeconds': 120})
        fail_after = int(contract.FAIL_AFTER.removesuffix('m')) * 60
        self.assertLessEqual(contract.STARTUP_SECONDS, fail_after - 60, 'Helm needs time to pull the image too')
        with tempfile.TemporaryDirectory() as root:
            (Path(root) / 'clusters/home').mkdir(parents=True)
            manifest = Path(root) / 'swhurl.yaml'
            manifest.write_text(yaml.safe_dump(WEB))
            args = app_new.parse_args(['kt', '--manifest', str(manifest), '--env', 'staging', '--root', root,
                                       '--image', 'ghcr.io/me/kt:1-abcdef0@sha256:' + 'a' * 64, '--no-register', '--team', 'demo'])
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
            with self.subTest(message=message), self.assertRaisesRegex(DeclarationError, message):
                seed(doc)


class LifetimeTests(unittest.TestCase):
    """Each field's kind and when it is read today, and the one way the file is fetched."""

    def test_every_field_has_a_kind_and_a_time_and_the_docs_say_the_same(self):
        kinds = (declaration.IDENTITY, declaration.NEED, declaration.OFFER, declaration.GRANT, declaration.DROPPED)
        self.assertEqual({item.kind for item in declaration.FIELDS}, set(kinds))
        self.assertEqual([item.kind for item in declaration.FIELDS], sorted((item.kind for item in declaration.FIELDS), key=kinds.index),
                         'FIELDS is ordered by kind, as the page and the templates are')
        self.assertEqual({item.read for item in declaration.FIELDS}, {declaration.SEED, declaration.LIVE, declaration.IGNORED})
        self.assertEqual([item.name for item in declaration.FIELDS if item.read == declaration.LIVE],
                         ['version', 'stack', 'provides', 'flags'])
        page = (ROOT / 'docs/apps.md').read_text()
        section = page[page.index('## swhurl.yaml'):page.index('## Who can reach it')]
        rows = re.findall(r'^\| `([A-Za-z.]+)` \| ([a-z]+) \| (read once|read again|ignored) \|', section, re.M)
        read = {declaration.SEED: 'read once', declaration.LIVE: 'read again', declaration.IGNORED: 'ignored'}
        self.assertEqual(rows, [(item.name, item.kind, read[item.read]) for item in declaration.FIELDS])
        for item in declaration.FIELDS:
            if item.then.startswith('make '):
                with self.subTest(field=item.name):
                    self.assertRegex(section, rf'(?m)^\| `{re.escape(item.name)}` \|.*`{re.escape(item.then.split(";")[0])}')

    def test_auto_deploy_is_accepted_and_ignored(self):
        for doc in ({'version': 1, 'kind': 'worker'}, {'version': 1, 'kind': 'worker', 'autoDeploy': False}):
            with self.subTest(doc=doc):
                self.assertTrue(seed(doc)['auto_deploy'])
        with self.assertRaisesRegex(DeclarationError, 'autoDeploy must be bool'):
            seed({'version': 1, 'kind': 'worker', 'autoDeploy': 'yes'})

    def test_the_live_fields_are_read_from_a_valid_file(self):
        found = declaration.of({**WEB, 'provides': {'api': '/openapi.json', 'apiFile': 'openapi.json', 'eventsFile': 'asyncapi.yaml'}})
        self.assertEqual((found.stack, found.api_path, found.api_file, found.events_file),
                         ('typescript', '/openapi.json', 'openapi.json', 'asyncapi.yaml'))
        self.assertEqual(declaration.of({'version': 1, 'kind': 'worker'}).stack, '')
        with self.assertRaisesRegex(DeclarationError, 'x/y@abc1234:swhurl.yaml: kind is required'):
            declaration.read('x/y', 'abc1234', opener=lambda request: b'version: 1\n')

    def test_one_fetcher_tells_missing_from_unreachable(self):
        seen = []

        def opener(code=None, error=None):
            def open_(request):
                seen.append((request.full_url, request.get_header('Authorization')))
                if code:
                    raise urllib.error.HTTPError(request.full_url, code, 'No', {}, None)
                if error:
                    raise error
                return yaml.safe_dump(WEB).encode()
            return open_
        self.assertEqual(declaration.read('x/y', token='t', opener=opener()).stack, 'typescript')
        self.assertIsNone(declaration.read('x/y', 'abc1234', opener=opener(404)))
        self.assertEqual(seen, [('https://api.github.com/repos/x/y/contents/swhurl.yaml', 'Bearer t'),
                                ('https://api.github.com/repos/x/y/contents/swhurl.yaml?ref=abc1234', None)])
        for failing in (opener(403), opener(error=urllib.error.URLError('down')), opener(error=OSError('away'))):
            with self.assertRaises(declaration.Unreachable):
                declaration.read('x/y', opener=failing)
        with self.assertRaisesRegex(DeclarationError, 'larger than 512 KiB'):
            declaration.read('x/y', opener=lambda request: b'#' * (declaration.MAX_BYTES + 1))


class FlagTests(unittest.TestCase):
    """flags: typed values per environment; the templates' generators refuse what this refuses."""

    def test_typed_flags_with_values_per_environment_are_kept_as_written(self):
        flags = {'new-checkout': {'type': 'boolean', 'description': 'Use the new checkout', 'default': False, 'staging': True},
                 'max-basket-items': {'type': 'integer', 'min': 1, 'max': 100, 'default': 20, 'prod': 10},
                 'discount-rate': {'type': 'number', 'max': 0.5, 'default': 0, 'staging': 0.25},
                 'banner': {'type': 'string', 'enum': ['none', 'sale'], 'default': 'none'},
                 'motto': {'type': 'string', 'default': ''}}
        self.assertEqual(declaration.of({**WEB, 'flags': flags}).flags, flags)
        self.assertEqual(declaration.of(WEB).flags, {})

    def test_a_flag_that_does_not_fit_its_type_is_refused(self):
        cases = [
            ({'New': {'type': 'boolean', 'default': True}}, "flag name 'New' must be kebab case"),
            ({'a-1': {'type': 'boolean', 'default': True}}, 'each part starting with a letter'),
            ({'a': True}, 'flags.a must be a mapping'),
            ({'a': {'type': 'json', 'default': 1}}, 'flags.a.type must be one of boolean, integer, number, string'),
            ({'a': {'type': 'boolean'}}, 'flags.a.default is required'),
            ({'a': {'type': 'boolean', 'default': 'yes'}}, "flags.a.default must be a boolean, not 'yes'"),
            ({'a': {'type': 'integer', 'default': True}}, 'flags.a.default must be an integer, not True'),
            ({'a': {'type': 'integer', 'default': 1.5}}, 'flags.a.default must be an integer'),
            ({'a': {'type': 'integer', 'default': 2**31}}, 'flags.a.default must be an integer'),
            ({'a': {'type': 'number', 'default': float('inf')}}, 'flags.a.default must be a number'),
            ({'a': {'type': 'boolean', 'default': True, 'min': 1}}, 'flags.a may not set min'),
            ({'a': {'type': 'boolean', 'default': True, 'dev': False}}, 'flags.a may not set dev'),
            ({'a': {'type': 'integer', 'default': 5, 'min': 10}}, 'flags.a.default is outside min and max'),
            ({'a': {'type': 'integer', 'default': 5, 'prod': 101, 'max': 100}}, 'flags.a.prod is outside min and max'),
            ({'a': {'type': 'integer', 'default': 5, 'min': 9, 'max': 1}}, 'flags.a.min is above its max'),
            ({'a': {'type': 'integer', 'default': 5, 'max': 1.5}}, 'flags.a.max must be an integer'),
            ({'a': {'type': 'string', 'default': 'x', 'enum': []}}, 'flags.a.enum must list distinct strings'),
            ({'a': {'type': 'string', 'default': 'x', 'enum': ['y']}}, 'flags.a.default must be one of y'),
            ({'a': {'type': 'string', 'default': 'x', 'description': 3}}, 'flags.a.description must be text'),
        ]
        for flags, message in cases:
            with self.subTest(message=message), self.assertRaisesRegex(DeclarationError, re.escape(message)):
                declaration.of({**WEB, 'flags': flags})


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
