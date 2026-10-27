"""The app interface names describe actual output, including absent conditional capabilities."""
import hashlib
import io
import itertools
import os
import re
import subprocess
import sys
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path
from unittest import mock

from swhurl import ROOT, interface, validate, yamlio
from swhurl.apps import calling, contract, teams
from swhurl.apps.instance import Instance
from swhurl.apps.model import draft, records, render, write
from swhurl.console import merge
from swhurl.console.actions import ActionError
from swhurl.report import Report
from swhurl.run import FakeRunner, Result


def scenario(kind='web', full=False):
    """Only public records and availability facts: no credential values or filesystem reads."""
    each, peer = Instance('shop', 'staging'), Instance('orders-api', 'staging')
    release = records.Release(records.Image('ghcr.io/samclement/shop', '12-abcdef0', 'sha256:' + 'a' * 64),
                              kind, 65532, port=8080 if kind == 'web' else None,
                              health_path='/healthz' if kind == 'web' else '')
    grants = records.Grants()
    allocations = records.Allocations()
    encrypted = frozenset()
    if full:
        release = replace(release, telemetry=True, database='sqlite' if kind == 'web' else '',
                          secrets=('API_TOKEN',), description='Shop', api_path='/openapi.json',
                          publishes=('sent',), offers='abcdef0')
        grants = replace(grants, calls=('orders-api',), subscriptions=('orders-api.created',),
                         egress=('api.example.com:443',), exceptions='non-root=reviewed')
        allocations = records.Allocations(30101, 'U-SHOP', records.Storage('1Gi', 'local-path-retain') if kind == 'web' else None)
        encrypted = frozenset({draft.EVENTS_SECRET})
    app = records.App('payments', 'none' if full else '')
    entry = draft.Entry(app, release, records.Settings(grants, allocations=allocations), encrypted)
    peer_release = records.Release(records.Image('ghcr.io/samclement/orders-api', '12-abcdef0'), 'web', 65532,
                                   port=9090, health_path='/healthz', publishes=('created',) if full else ())
    peer_entry = draft.Entry(app, peer_release, records.Settings(records.Grants(allows=('shop',) if full else ()),
                             allocations=records.Allocations(30102, 'U-ORDERS' if full else '')), encrypted)
    return each, {each: entry, peer: peer_entry}, draft.Platform(signing_keys='{"keys":[]}', egress=full)


def output(each, world, platform):
    files = render.instance(each, world, platform)
    release = files[Path('apps') / each.app / each.env / 'helmrelease.yaml'][0]
    values = release['spec']['values']
    controller = values['controllers']['main']
    return files, release, values, controller, controller['containers']['main']['env']


class InterfaceTests(unittest.TestCase):
    def test_import_without_site_packages_configuration_or_yaml(self):
        program = ('import pathlib, sys; '
                   'pathlib.Path.read_text = lambda *a, **k: (_ for _ in ()).throw(AssertionError("I/O")); '
                   'from swhurl import interface; from swhurl.apps.model import records; '
                   'assert interface.label("x") == "platform.swhurl.com/x"; '
                   'assert not any(n == "yaml" or n.startswith("ruamel") for n in sys.modules); '
                   'assert "swhurl.platform" not in sys.modules')
        with tempfile.TemporaryDirectory() as cwd:
            subprocess.run([sys.executable, '-S', '-c', program], cwd=cwd, check=True,
                           env={**os.environ, 'PYTHONPATH': str(ROOT / 'tools')}, capture_output=True)

    def test_table_checks_missing_extra_duplicate_and_description(self):
        validate.check_interface_table(Report(io.StringIO()))
        text = (ROOT / 'docs/apps.md').read_text()
        row = next(line for line in text.splitlines(True) if line.startswith('| `SWHURL_APP` |'))
        for changed, problem in ((text.replace(row, ''), 'SWHURL_APP: missing'),
                                 (text.replace(row, row.replace('SWHURL_APP', 'SWHURL_EXTRA')), 'SWHURL_EXTRA: extra'),
                                 (text.replace(row, row + row), 'SWHURL_APP: duplicate'),
                                 (text.replace(row, row.replace('App name', 'Other')), 'SWHURL_APP: description differs')):
            with self.subTest(problem=problem), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                (root / 'docs').mkdir()
                (root / 'docs/apps.md').write_text(changed)
                with self.assertRaisesRegex(validate.ValidationError, problem):
                    validate.check_interface_table(Report(io.StringIO()), root)

    def test_emitted_environment_names_across_independent_capabilities(self):
        observed = set()
        for kind, telemetry, egress, keyed in itertools.product(records.KINDS, (False, True), (False, True), (False, True)):
            with self.subTest(kind=kind, telemetry=telemetry, egress=egress, keyed=keyed):
                each, world, platform = scenario(kind)
                entry = world[each]
                world[each] = replace(entry, release=replace(entry.release, telemetry=telemetry),
                                      settings=replace(entry.settings, allocations=records.Allocations(30101, 'U-TEST' if keyed else '')),
                                      encrypted=frozenset({draft.EVENTS_SECRET}) if keyed else frozenset())
                files, release, values, controller, env = output(each, world, replace(platform, egress=egress))
                expected = {'SWHURL_APP', 'SWHURL_ENV', 'SWHURL_NOTIFY_URL'}
                if kind == 'web':
                    expected |= {'SWHURL_AUTH_ISSUER', 'SWHURL_AUTH_KEYS'}
                if telemetry:
                    expected |= {'HOST_IP', 'OTEL_EXPORTER_OTLP_ENDPOINT', 'OTEL_EXPORTER_OTLP_PROTOCOL', 'OTEL_SERVICE_NAME'}
                if egress:
                    expected |= {'HOST_IP', 'HTTP_PROXY', 'HTTPS_PROXY', 'NO_PROXY', 'NODE_USE_ENV_PROXY', 'JAVA_TOOL_OPTIONS'}
                if keyed:
                    expected |= {'SWHURL_EVENTS_URL', 'SWHURL_EVENTS_NKEY'}
                self.assertEqual(set(env), expected)
                self.assertNotIn('SWHURL_EVENTS_PUBLISH', env)
                self.assertNotIn('SWHURL_EVENTS_SUBSCRIBE', env)
                self.assertEqual(set(release['metadata']['annotations']), {'platform.swhurl.com/render'})
                namespace = files[Path('apps/shop/staging/namespace.yaml')][0]
                self.assertNotIn('annotations', namespace['metadata'])
                self.assertEqual(set(namespace['metadata']['labels']), {'platform.swhurl.com/managed', 'platform.swhurl.com/app',
                                 'platform.swhurl.com/environment', 'platform.swhurl.com/exposure', 'platform.swhurl.com/team'})
                self.assertEqual(bool(controller.get('annotations', {}).get(interface.RELOAD)), keyed)
                self.assertEqual(bool(controller['containers']['main'].get('envFrom')), keyed)
                if telemetry or egress:
                    self.assertLess(list(env).index('HOST_IP'), list(env).index('NO_PROXY') if egress else list(env).index('OTEL_EXPORTER_OTLP_ENDPOINT'))
                observed.update(env)
        self.assertLessEqual(observed, interface.ENTRIES.keys())

    def prepared_seed_names(self):
        def encrypt(args, text):
            document = yamlio.load(text)
            document['stringData'] = dict.fromkeys(document['stringData'], 'ENC[synthetic]')
            document['sops'] = {}
            return Result(args, stdout=yamlio.dump(document))
        runner = FakeRunner().on('sops', handler=encrypt)
        each, world, platform = scenario(full=True)
        entry = world[each]
        entry = replace(entry, encrypted=frozenset(), settings=replace(entry.settings,
                        allocations=replace(entry.settings.allocations, events_key='')))
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(write.nkeys, 'generate', return_value=('synthetic seed', 'U-TEST')):
            allocated = write.allocate(Path(tmp), {each: entry}, platform, {}, runner)
        document = yamlio.load(allocated[each].pending[draft.EVENTS_SECRET])
        self.assertEqual(document['metadata']['name'], 'shop-events')
        self.assertEqual(set(document['stringData']), {'SWHURL_EVENTS_SEED'})
        return set(document['stringData'])

    def test_full_render_covers_registry_metadata_paths_and_dynamic_names(self):
        observed = set()
        for kind in records.KINDS:
            each, world, platform = scenario(kind, True)
            files, release, values, controller, env = output(each, world, platform)
            self.assertEqual(env['SWHURL_SERVICE_ORDERS_API_URL'], 'http://orders-api.orders-api-$(SWHURL_ENV).svc.cluster.local:9090')
            self.assertLess(list(env).index('SWHURL_ENV'), list(env).index('SWHURL_SERVICE_ORDERS_API_URL'))
            self.assertEqual(env['SWHURL_EVENTS_PUBLISH'], 'sent')
            self.assertEqual(env['SWHURL_EVENTS_SUBSCRIBE'], 'orders-api.created')
            self.assertEqual(env['SWHURL_DESCRIPTION'], 'Shop')
            self.assertEqual(calling.declared_calls(env), ['orders-api'])
            # Render the callee as well: the caller list is supplied there, not on the caller.
            peer = Instance('orders-api', 'staging')
            peer_env = output(peer, world, platform)[4]
            self.assertEqual(peer_env['SWHURL_ALLOWED_CALLERS'], 'shop')
            observed.update(env)
            observed.update(peer_env)
            self.assertEqual(('DATABASE_PATH' in env), kind == 'web')
            self.assertEqual((interface.API_ANNOTATION in release['metadata']['annotations']), kind == 'web')
            for docs in files.values():
                for doc in docs:
                    if doc['kind'] in ('Namespace', 'HelmRelease', 'ImageRepository', 'ImagePolicy'):
                        for surface in ('labels', 'annotations'):
                            observed.update(doc['metadata'].get(surface, {}))
            observed.update(controller.get('annotations', {}))
            tokens = values['persistence']['tokens']
            self.assertEqual(tokens['globalMounts'], [{'path': '/var/run/swhurl/tokens', 'readOnly': True}])
            self.assertEqual([s['serviceAccountToken']['path'] for s in tokens['sources']], ['orders-api/token', 'notify/token'])
            if kind == 'web':
                self.assertEqual(env['DATABASE_PATH'], '/data/app.db')
                self.assertEqual(values['persistence']['data']['globalMounts'], [{'path': '/data'}])
                observed.update(('/data', '/data/app.db'))
        observed.remove('SWHURL_SERVICE_ORDERS_API_URL')
        observed.add(interface.SERVICE_VARIABLE)
        observed.add('/var/run/swhurl/tokens/<audience>/token')
        # Inspect the actual Secret preparation with a synthetic seed and fake encryption only.
        observed.update(self.prepared_seed_names())
        self.assertEqual(observed, {name for name, (surface, _, _) in interface.ENTRIES.items()
                                    if surface != 'env override'})
        self.assertNotIn(interface.NOTIFY_TOKEN_VARIABLE, observed, 'the reader override is not injected')

    def test_dynamic_family_format_and_reader_preserve_names_and_order(self):
        self.assertEqual(interface.callee_variable('orders-api'), 'SWHURL_SERVICE_ORDERS_API_URL')
        self.assertEqual(interface.callee_variable('a1-2'), 'SWHURL_SERVICE_A1_2_URL')
        variables = dict.fromkeys(('SWHURL_SERVICE_A1_2_URL', 'SWHURL_SERVICE_ORDERS_API_URL',
                                  'SWHURL_SERVICE_lower_URL', 'SWHURL_SERVICE__URL'), 'address')
        self.assertEqual(calling.declared_calls(variables), ['a1-2', 'orders-api'])

    def test_waiting_calls_and_empty_keys_do_not_invent_capabilities(self):
        each, world, platform = scenario()
        entry = world[each]
        world[each] = replace(entry, settings=replace(entry.settings, grants=records.Grants(calls=('orders-api', 'absent'))))
        env = output(each, world, replace(platform, signing_keys=''))[4]
        self.assertEqual(env['SWHURL_AUTH_KEYS'], '')
        self.assertFalse(calling.declared_calls(env))
        self.assertNotIn('SWHURL_ALLOWED_CALLERS', output(Instance('orders-api', 'staging'), world, platform)[4])
        # A prepared encrypted seed has the same availability as an on-disk encrypted seed.
        keyed = replace(entry, settings=replace(entry.settings, allocations=records.Allocations(events_key='U-TEST')))
        world[each] = replace(keyed, pending={draft.EVENTS_SECRET: 'prepared ciphertext'})
        pending = output(each, world, platform)[0]
        world[each] = replace(keyed, encrypted=frozenset({draft.EVENTS_SECRET}))
        self.assertEqual(pending, output(each, world, platform)[0])

    def test_every_environment_entry_is_reserved_and_refusals_keep_their_order(self):
        for name, (surface, _, _) in interface.ENTRIES.items():
            if surface in ('env', 'Secret env', 'env family', 'env override'):
                concrete = interface.callee_variable('orders-api') if surface == 'env family' else name
                self.assertIn('reserved for the platform:', contract.secret_key_problem([concrete]))
        self.assertEqual(contract.secret_key_problem(['ORDERS_TOKEN']), '')
        self.assertIn('variable names', contract.secret_key_problem(['bad', 'HOST_IP']))
        self.assertIn('reserved for the platform', contract.secret_key_problem(['HOST_IP', 'HOST_IP']))
        self.assertIn('secret keys repeated', contract.secret_key_problem(['TOKEN', 'TOKEN']))
        self.assertEqual(contract.secret_key_problem(['HOST_IP']),
                         'reserved for the platform: HOST_IP (the platform sets HOST_IP, DATABASE_PATH, HTTP_PROXY, HTTPS_PROXY, '
                         'NO_PROXY, NODE_USE_ENV_PROXY, JAVA_TOOL_OPTIONS and names starting OTEL_, SWHURL_, KUBERNETES_); choose another name')

    def test_name_and_subscription_boundaries_keep_match_vs_fullmatch_semantics(self):
        names = ('a', 'ab', 'a-b9', 'a' * 40, 'a' * 41, 'a-', '-a', '1a', 'A', 'a_b', '', 'a\n')
        old_name = re.compile(r'^[a-z]([a-z0-9-]{0,38}[a-z0-9])?$')
        old_subscription = re.compile(r'^([a-z]([a-z0-9-]{0,38}[a-z0-9])?)\.([a-z]([a-z0-9-]{0,38}[a-z0-9])?)$')
        for name in names:
            with self.subTest(name=name):
                valid = bool(old_name.match(name))
                self.assertEqual(bool(records.NAME_RE.match(name)), valid)
                self.assertEqual(bool(records.NAME_RE.fullmatch(name)), bool(old_name.fullmatch(name)))
                self.assertEqual(Instance.named(name, 'staging') is not None, valid)
                if valid:
                    self.assertEqual(teams.checked_name(name), name)
                    self.assertEqual(records.App.parse({'version': 1, 'team': name}).team, name)
                else:
                    with self.assertRaisesRegex(ValueError, 'team must be a team name'):
                        records.App.parse({'version': 1, 'team': name})
                if old_name.fullmatch(name):
                    merge.team_commands('add', name, {})
                else:
                    with self.assertRaisesRegex(ActionError, 'team name: lowercase'):
                        merge.team_commands('add', name, {})
        for app, event in itertools.product(names, repeat=2):
            text = f'{app}.{event}'
            self.assertEqual(bool(records.SUBSCRIPTION_RE.match(text)), bool(old_subscription.match(text)), text)

    def test_generated_bytes_and_order_match_pre_m6_goldens(self):
        # SHA-256 of ordered paths and YAML bytes, captured from a755d200 with these public fixtures. The
        # web-with-a-database hash was re-captured on 27 October 2026, when the backup image moved to the Go
        # one: no other scenario renders a backup job, and nothing else in the payload changed.
        expected = {
            ('web', False): '4464c72b9ec7e36081511be14e4a1d99531422c7f8d3524e5bb8aff6947448ff',
            ('web', True): '841ce9aaee805eb9ea9961abd3cbf3547a94d024c56257c0ef5d353834669a89',
            ('worker', False): '9dac7f1aea85e32d306295ba0d5571710d6e295cebec0696452c21ef46c0c860',
            ('worker', True): 'ae588cb93769a110cdc004b0b9c6a433b8f91a1ce1765bf2006b33cc2002cb43',
            ('job', False): '4e03395306aef0aa605789439b32b2190a9a649e8e51317afc5f95b63c357978',
            ('job', True): '5bb0bacbb82ded6c87e1b9cf9cb069df52c92e2eb391975092c57e58116940ab',
        }
        for kind, full in itertools.product(records.KINDS, (False, True)):
            each, world, platform = scenario(kind, full)
            payload = ''.join(f'{path}\0{render.text(docs)}' for path, docs in render.instance(each, world, platform).items())
            self.assertEqual(hashlib.sha256(payload.encode()).hexdigest(), expected[(kind, full)])
