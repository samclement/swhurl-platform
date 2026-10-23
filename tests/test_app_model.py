"""The app model (docs/plan.md section 24): records, rendering, and the shadow that compares them with the manifests."""
import dataclasses
import io
import re
import shutil
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock

from swhurl import ROOT, interface, yamlio
from swhurl.apps import cli, edit, policy
from swhurl.apps.contract import JOB_SCHEDULE
from swhurl.apps.errors import EditError
from swhurl.apps.instance import Instance
from swhurl.apps.model import generated, render, store
from swhurl.apps.model import write as model_sync
from swhurl.apps.model.draft import EVENTS_SECRET, Entry, Platform, RenderError
from swhurl.apps.model.records import (
    Allocations,
    App,
    Choices,
    Grants,
    Image,
    RecordError,
    Release,
    Resources,
    Settings,
    Storage,
)

SAMPLE = ROOT / 'tests/fixtures/instance'  # frozen sample instance; see its README
IMAGE = Image('ghcr.io/samclement/orders', '12-abcdef0', 'sha256:' + 'a' * 64)


def web(**changes) -> Release:
    return dataclasses.replace(Release(IMAGE, 'web', 65532, port=8080, health_path='/healthz'), **changes)


class RecordTests(unittest.TestCase):
    def test_every_record_round_trips_through_its_document(self):
        release = web(stack='typescript', telemetry=True, resources=Resources('100m', '192Mi', '384Mi'), database='sqlite',
                      secrets=('API_TOKEN',), description='Records orders', api_path='/openapi.json',
                      publishes=('created',), offers='abcdef0')
        settings = Settings(grants=Grants(exposure='public', calls=('inventory',), allows=('team:payments', 'reporting'), subscriptions=('shop.visited',), egress=('api.example.com:443',), exceptions='no-escalation=needs raw sockets'), choices=Choices(host='orders.example.com', issuer='letsencrypt-staging', replicas=2, capacity=Resources(memory='256Mi')), allocations=Allocations(30101, 'UDFZ', Storage('1Gi', 'local-path-retain')))
        app = App('payments', 'errors=none')
        self.assertEqual(Release.parse(release.document()), release)
        self.assertEqual(Settings.parse(settings.document()), settings)
        self.assertEqual(App.parse(app.document()), app)
        self.assertEqual(Settings.parse({'version': 1}), Settings(), 'every setting has a default')
        self.assertNotIn('capacity', Settings().document(), 'an empty optional field is left out')

    def test_a_document_is_refused_rather_than_guessed(self):
        for doc, message in (({'version': 2, 'team': 'x'}, 'version must be 1'),
                             ({'version': 1, 'team': 'x', 'colour': 'red'}, 'unknown field colour'),
                             ({'version': 1}, 'team is required')):
            with self.subTest(message=message), self.assertRaisesRegex(RecordError, message):
                App.parse(doc)
        base = web().document()
        for change, message in (({'port': None}, 'a web image needs port and healthPath'),
                                ({'kind': 'worker'}, 'only a web image has port and healthPath'),
                                ({'kind': 'daemon'}, 'kind must be one of'),
                                ({'database': 'postgres'}, 'database must be one of'),
                                ({'publishes': ['Bad.Name']}, 'publishes: not allowed')):
            with self.subTest(message=message), self.assertRaisesRegex(RecordError, message):
                Release.parse({**base, **change})
        with self.assertRaisesRegex(RecordError, 'subscriptions: not allowed: shop'):
            Settings.parse({'version': 1, 'subscriptions': ['shop']})
        for entry in ('*', 'team:*', 'team:', 'group:demo', 'Team:demo'):
            with self.subTest(allows=entry), self.assertRaisesRegex(RecordError, 'allows: not allowed'):
                Settings.parse({'version': 1, 'allows': [entry]})


class RenderTests(unittest.TestCase):
    platform = Platform(signing_keys='{}', egress=False)

    def render(self, world, each):
        return render.instance(each, world, self.platform)

    def test_the_hash_is_deterministic_covers_other_rendered_files_and_lives_only_in_metadata(self):
        from copy import deepcopy

        each = Instance('orders', 'staging')
        entry = Entry(App('payments'), web(), Settings())
        path = Path('apps/orders/staging/helmrelease.yaml')
        files = self.render({each: entry}, each)
        digest = files[path][0]['metadata']['annotations'][interface.RENDER_ANNOTATION]
        before_hash = deepcopy(files)
        del before_hash[path][0]['metadata']['annotations'][interface.RENDER_ANNOTATION]
        if not before_hash[path][0]['metadata']['annotations']:
            del before_hash[path][0]['metadata']['annotations']
        self.assertEqual(render.fingerprint(before_hash), digest, 'the hash does not include itself')
        self.assertEqual(render.fingerprint(dict(reversed(list(before_hash.items())))), digest)
        changed_team = self.render({each: dataclasses.replace(entry, app=App('another'))}, each)
        self.assertNotEqual(changed_team[path][0]['metadata']['annotations'][interface.RENDER_ANNOTATION], digest,
                            'a namespace change is part of the instance render')
        self.assertEqual(files[path][0]['spec'], changed_team[path][0]['spec'], 'the hash never enters the Helm spec')
        self.assertEqual(self.render({each: entry}, each), files, 'rendering is repeatable and does not mutate its inputs')

    def test_the_larger_of_the_minimum_and_the_choice_wins(self):
        each = Instance('orders', 'staging')
        def resources(release, settings):
            files = self.render({each: Entry(App('payments'), release, settings)}, each)
            return files[Path('apps/orders/staging/helmrelease.yaml')][0]['spec']['values']['controllers']['main'][
                'containers']['main']['resources']
        floor = Resources('100m', '192Mi', '384Mi')
        self.assertEqual(resources(web(), Settings()),
                         {'requests': {'cpu': '10m', 'memory': '32Mi'}, 'limits': {'memory': '128Mi'}}, 'the default')
        self.assertEqual(resources(web(resources=floor), Settings(choices=Choices(capacity=Resources(memory='256Mi', cpu='50m')))),
                         {'requests': {'cpu': '100m', 'memory': '256Mi'}, 'limits': {'memory': '384Mi'}})

    def test_records_that_cannot_be_rendered_together_are_refused(self):
        orders, shop = Instance('orders', 'staging'), Instance('shop', 'staging')
        cases = (
            ({orders: Entry(App('p'), web(), Settings(grants=Grants(calls=('shop',)))),
              shop: Entry(App('p'), Release(IMAGE, 'worker', 1), Settings(grants=Grants(allows=('orders',))))}, orders, 'a worker: it has no address'),
            ({orders: Entry(App('p'), web(), Settings(grants=Grants(subscriptions=('shop.paid',)))),
              shop: Entry(App('p'), web(), Settings())}, orders, 'subscribes to shop.paid, which shop/staging does not publish'),
            ({orders: Entry(App('p'), web(publishes=('created',)), Settings())}, orders, 'declares events but has no NATS key'),
            ({orders: Entry(App('p'), web(database='sqlite'), Settings())}, orders, 'declares sqlite but has no database allocation'),
        )
        for world, each, message in cases:
            with self.subTest(message=message), self.assertRaisesRegex(RenderError, message):
                self.render(world, each)
        with self.assertRaisesRegex(RenderError, 'has no egress port allocated'):
            render.instance(orders, {orders: Entry(App('p'), web(), Settings())}, Platform())

    def test_a_call_connects_only_when_the_callee_allows_the_caller_or_its_team(self):
        shop, orders = Instance('shop', 'staging'), Instance('orders', 'staging')

        def connection(world):
            files = {each: self.render(world, each) for each in world}
            caller = files[shop][Path('apps/shop/staging/helmrelease.yaml')][0]['spec']['values']
            env = caller['controllers']['main']['containers']['main']['env']
            audiences = [s['serviceAccountToken']['audience'] for s in caller['persistence']['tokens']['sources']]
            callee = files.get(orders, {}).get(Path('apps/orders/staging/helmrelease.yaml'))
            callers = callee[0]['spec']['values']['controllers']['main']['containers']['main']['env'].get('SWHURL_ALLOWED_CALLERS') if callee else None
            return 'SWHURL_SERVICE_ORDERS_URL' in env, audiences, callers

        def world(allows=(), team='demo', present=True):
            found = {shop: Entry(App(team), web(), Settings(grants=Grants(calls=('orders',))))}
            if present:
                found[orders] = Entry(App('demo'), web(), Settings(grants=Grants(allows=allows)))
            return found
        connected = (True, ['orders', 'notify'], 'shop')
        self.assertEqual(connection(world(('shop',))), connected, 'granted to the app')
        self.assertEqual(connection(world(('team:demo',))), connected, 'granted to its team')
        self.assertEqual(connection(world(('team:demo',), team='payments')), (False, ['notify'], None),
                         'the grant follows the caller\'s current team')
        self.assertEqual(connection(world()), (False, ['notify'], None), 'a call without a grant waits')
        self.assertEqual(connection(world(present=False)), (False, ['notify'], None), 'a call to an absent app renders nothing')
        unused = {orders: Entry(App('demo'), web(), Settings(grants=Grants(allows=('shop',))))}
        self.assertNotIn('SWHURL_ALLOWED_CALLERS', self.render(unused, orders)[Path('apps/orders/staging/helmrelease.yaml')][0]
                         ['spec']['values']['controllers']['main']['containers']['main']['env'], 'a grant without a call is unused')

    def test_job_schedules_use_the_shared_validator_and_keep_the_default(self):
        each = Instance('nightly', 'staging')
        release = Release(IMAGE, 'job', 65532)
        for schedule in ('', '*/10 * * * *', '@daily'):
            with self.subTest(schedule=schedule):
                entry = Entry(App('demo'), release, Settings.parse({'version': 1, 'schedule': schedule}))
                files = self.render({each: entry}, each)
                controller = files[Path('apps/nightly/staging/helmrelease.yaml')][0]['spec']['values']['controllers']['main']
                self.assertEqual(controller['cronjob']['schedule'], schedule or JOB_SCHEDULE)
        for kind, schedule in (('job', '99 99 * * *'), ('job', '*/0 * * * *'), ('job', 'not a cron schedule'),
                               ('web', JOB_SCHEDULE), ('worker', JOB_SCHEDULE)):
            with self.subTest(kind=kind, schedule=schedule), self.assertRaisesRegex(RenderError, 'schedule'):
                entry = Entry(App('demo'), web() if kind == 'web' else dataclasses.replace(release, kind=kind),
                              Settings.parse({'version': 1, 'schedule': schedule}))
                self.render({each: entry}, each)

    def test_arbitrary_secret_names_and_non_web_databases_preserve_creation_restrictions(self):
        each = Instance('orders', 'staging')
        for names in (('api-token',), ('HOST_IP',), ('SWHURL_PRIVATE',), ('TOKEN', 'TOKEN')):
            with self.subTest(names=names), self.assertRaisesRegex(RenderError, 'secret|reserved'):
                entry = Entry(App('demo'), Release.parse(web(secrets=names).document()), Settings())
                self.render({each: entry}, each)
        for kind in ('worker', 'job'):
            with self.subTest(kind=kind), self.assertRaisesRegex(RenderError, 'keeps no database'):
                release = Release.parse(Release(IMAGE, kind, 65532, database='sqlite').document())
                # With no allocation, the kind restriction must fail first.
                self.render({each: Entry(App('demo'), release, Settings())}, each)
        entry = Entry(App('demo'), web(secrets=('API_TOKEN',)), Settings())
        files = self.render({each: entry}, each)
        self.assertIn('API_TOKEN', str(files[Path('apps/orders/staging/helmrelease.yaml')]))

    def test_a_publisher_and_its_subscriber_share_one_user_list(self):
        orders, shop = Instance('orders', 'staging'), Instance('shop', 'staging')
        key = frozenset({EVENTS_SECRET})
        world = {orders: Entry(App('p'), web(publishes=('created',)), Settings(allocations=Allocations(events_key='UORDERS')), key),
                 shop: Entry(App('p'), web(), Settings(grants=Grants(subscriptions=('orders.created',)), allocations=Allocations(events_key='USHOP')), key)}
        users = render.shared(world, dataclasses.replace(self.platform, controller_key='UCONTROL')).nats_users
        self.assertEqual([user['nkey'] for user in users], ['UCONTROL', 'UORDERS', 'USHOP'])
        self.assertIn('$JS.ACK.staging-orders.shop_created.>', users[2]['permissions']['publish']['allow'])
        files = self.render(world, shop)
        self.assertEqual([doc['kind'] for doc in files[Path('apps/shop/staging/events.yaml')]], ['Consumer'])


class ShadowTests(unittest.TestCase):
    """What today's commands write, the model reads back and renders again unchanged."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        for base, rel in ((SAMPLE, 'apps'), (SAMPLE, 'clusters'), (ROOT, 'platform/reloader/helmrelease.yaml'),
                          (ROOT, 'apps/teams.yaml')):
            src, dst = base / rel, self.root / rel
            dst.parent.mkdir(parents=True, exist_ok=True)
            (shutil.copytree if src.is_dir() else shutil.copy)(src, dst)
        reloader = self.root / 'platform/reloader/helmrelease.yaml'  # the platform's own namespaces only, as for the sample
        reloader.write_text(re.sub(r'namespaces: \[[^\]]*\]', 'namespaces: [ingress, logging, console]', reloader.read_text()))

    def problems(self):
        return generated.problems(self.root)

    def test_the_sample_and_the_repository_render_as_committed(self):
        self.assertEqual(self.problems(), [])
        self.assertEqual(generated.check(ROOT), [], 'make check-apps reports the differences')

    def test_invalid_reloader_is_a_validation_problem_without_writes(self):
        path = self.root / render.RELOADER_FILE
        for text in ('spec: [', '', '[]', '{}', 'spec: null',
                     'spec: {values: []}', 'spec: {values: {reloader: {}}}',
                     'spec: {values: {reloader: {namespaces: null}}}',
                     'spec: {values: {reloader: {namespaces: hello-staging}}}',
                     'spec: {values: {reloader: {namespaces: {hello-staging: true}}}}',
                     'spec: {values: {reloader: {namespaces: [ingress, 1]}}}',
                     'spec: {values: {reloader: {namespaces: [{}]}}}'):
            with self.subTest(text=text):
                path.write_text(text)
                before = self.snapshot()
                problems = generated.check(self.root)
                self.assertEqual(len(problems), 1)
                self.assertIn(f'{render.RELOADER_FILE}: cannot read committed YAML', problems[0])
                self.assertEqual(self.snapshot(), before)

    def test_policy_cli_fails_cleanly_on_invalid_reloader(self):
        (self.root / render.RELOADER_FILE).write_text('spec: [')
        output = io.StringIO()
        with mock.patch.object(policy, 'ROOT', self.root), mock.patch.object(policy, 'instances', return_value=[]), \
                redirect_stdout(output):
            self.assertEqual(policy.main([]), 1)
        self.assertIn('[BAD] app model', output.getvalue())
        self.assertIn(f'{render.RELOADER_FILE}: cannot read committed YAML', output.getvalue())

    def test_unreadable_reloader_is_a_validation_problem(self):
        read_text = Path.read_text
        path = self.root / render.RELOADER_FILE
        for error in (OSError('unreadable'), UnicodeError('invalid encoding')):
            def read(file, *args, failure=error, **kwargs):
                if file == path:
                    raise failure
                return read_text(file, *args, **kwargs)
            with self.subTest(error=type(error).__name__), mock.patch.object(Path, 'read_text', read):
                self.assertEqual(generated.check(self.root),
                                 [f'{render.RELOADER_FILE}: cannot read committed YAML; expected '
                                  'spec.values.reloader.namespaces to be a list of strings'])

    def test_settings_written_by_the_commands_render_as_written(self):
        (self.root / 'platform/egress').mkdir(parents=True)
        shutil.copy(ROOT / 'platform/egress/proxy.yaml', self.root / 'platform/egress/proxy.yaml')
        with redirect_stdout(io.StringIO()):
            edit.sync(self.root)  # every instance gets its egress port, as on the cluster
            edit.expose(self.root, 'hello', 'prod', 'public', 'hello.example.com')
            edit.set_egress(self.root, 'hello', 'staging', add=['api.example.com', '*.example.org:8443'])
            edit.set_alerts(self.root, 'hello', edit.Thresholds({'errors': 'none', 'error-logs': '20'}))
            edit.scale(self.root, 'hello', 'staging', replicas=2, memory='256Mi')
        settings = self.root / 'apps/hello/prod/instance.yaml'  # a reviewed exception, written in the record as documented
        settings.write_text(settings.read_text() + 'exceptions: no-escalation=needs raw sockets\n')
        with redirect_stdout(io.StringIO()):
            edit.sync(self.root)
        self.assertEqual(self.problems(), [])
        entries, _ = store.world(self.root)
        prod = entries[Instance('hello', 'prod')]
        self.assertEqual((prod.settings.grants.exposure, prod.settings.choices.host), ('public', 'hello.example.com'))
        self.assertEqual(prod.settings.grants.exceptions, 'no-escalation=needs raw sockets')
        self.assertEqual(entries[Instance('hello', 'staging')].settings.choices.replicas, 2)
        self.assertEqual(prod.app.alerts, entries[Instance('hello', 'staging')].app.alerts)

    def test_records_without_a_render_or_unit_are_rendered_and_registered(self):
        """A first promotion merged on GitHub carries only records: make app-sync on main renders production
        and registers its unit (docs/console.md#auto-merge)."""
        prod = self.root / 'apps/hello/prod'
        for path in prod.iterdir():
            if path.name not in ('release.yaml', 'instance.yaml'):
                path.unlink()
        unit, registry = self.root / 'clusters/home/app-hello-prod.yaml', self.root / 'clusters/home/kustomization.yaml'
        unit.unlink()
        registry.write_text(registry.read_text().replace('- app-hello-prod.yaml\n', ''))
        self.assertIn(Instance('hello', 'prod'), store.world(self.root)[0], 'found by its records')
        with redirect_stdout(io.StringIO()):
            edit.sync(self.root)
        self.assertTrue((prod / 'helmrelease.yaml').is_file())
        self.assertTrue(unit.is_file())
        self.assertIn('app-hello-prod.yaml', registry.read_text())
        self.assertEqual(self.problems(), [])
        registry.unlink()  # a tree without the registry (a test tree) gets no unit
        unit.unlink()
        with redirect_stdout(io.StringIO()):
            edit.sync(self.root)
        self.assertFalse(unit.exists())

    def snapshot(self):
        return {path.relative_to(self.root): path.read_bytes() for path in self.root.rglob('*') if path.is_file()}

    def test_record_only_removal_cleans_shared_outputs_and_preserves_unrelated_files(self):
        registry = self.root / render.REGISTRY_FILE
        registry.write_text(registry.read_text() + '  - app-control.yaml # not an instance unit\n')
        (self.root / 'platform/egress').mkdir(parents=True)
        (self.root / 'platform/nats').mkdir(parents=True)
        (self.root / 'platform/nats/controller-nkey.pub').write_text('UCONTROL')
        for env in ('staging', 'prod'):
            record = self.root / f'apps/hello/{env}/instance.yaml'
            doc = yamlio.load(record.read_text())
            doc['allocations'] = {'eventsKey': 'U' + env.upper()}
            record.write_text(yamlio.dump(doc))
            (record.parent / 'events.sops.yaml').write_text('sops: {}')
        model_sync.sync(self.root)
        self.assertIn('hello-prod', (self.root / render.RELOADER_FILE).read_text())
        self.assertIn('UPROD', (self.root / 'platform/nats/users.yaml').read_text())
        prod = self.root / 'apps/hello/prod'
        unrelated = {'notes.yaml': 'kind: Note', 'other.sops.yaml': 'sops: {}', 'data/db': 'keep me',
                     'offers/notes.txt': 'not an offer'}
        for name, text in unrelated.items():
            (prod / name).parent.mkdir(parents=True, exist_ok=True)
            (prod / name).write_text(text)
        (prod / 'offers/source.yaml').write_text('api: openapi.json\n')
        (prod / 'offers/openapi.json').write_text('{}')
        (prod / 'secret.sops.yaml').write_text('sops: {}')
        for name in ('release.yaml', 'instance.yaml'):
            (prod / name).unlink()  # what a held removal's merge carries; the old outputs are still present
        (prod / 'helmrelease.yaml').write_text('invalid: [')  # stale output cannot resurrect the instance
        self.assertNotIn(Instance('hello', 'prod'), store.world(self.root)[0])
        model_sync.sync(self.root)
        self.assertEqual({name: (prod / name).read_text() for name in unrelated}, unrelated)
        for name in render.MANAGED_FILES | {'events.sops.yaml', 'secret.sops.yaml', 'offers/source.yaml', 'offers/openapi.json'}:
            self.assertFalse((prod / name).exists(), name)
        self.assertTrue((prod.parent / 'app.yaml').is_file(), 'another environment still owns the App record')
        self.assertNotIn('hello-prod', (self.root / render.RELOADER_FILE).read_text())
        self.assertIn('namespaces: [ingress, logging, console, hello-staging]', (self.root / render.RELOADER_FILE).read_text())
        self.assertNotIn('UPROD', (self.root / 'platform/nats/users.yaml').read_text())
        self.assertNotIn('hello-prod', (self.root / 'platform/egress/proxy.yaml').read_text())
        registry = (self.root / render.REGISTRY_FILE).read_text()
        self.assertNotIn('app-hello-prod.yaml', registry)
        self.assertIn('app-hello-staging.yaml', registry)
        self.assertIn('  - app-control.yaml # not an instance unit\n', registry)
        self.assertFalse(Instance('hello', 'prod').unit_file(self.root).exists())
        for name in ('release.yaml', 'instance.yaml'):
            (self.root / 'apps/hello/staging' / name).unlink()
        model_sync.sync(self.root)
        self.assertFalse((prod.parent / 'app.yaml').exists(), 'the last environment owns its deletion')
        self.assertFalse((self.root / 'apps/hello/staging').exists())
        self.assertEqual({name: (prod / name).read_text() for name in unrelated}, unrelated)
        before = self.snapshot()
        self.assertEqual(model_sync.sync(self.root), [])
        self.assertEqual(self.snapshot(), before, 'sync is byte-idempotent after a record-only removal')

    def test_record_only_removal_refuses_waiting_and_granted_callers_with_zero_writes(self):
        target, caller = Instance('hello', 'staging'), Instance('caller', 'staging')
        for granted in (False, True):
            for marker in ('folder', 'unit'):
                with self.subTest(granted=granted, marker=marker), tempfile.TemporaryDirectory() as tmp:
                    root = Path(tmp) / 'root'
                    shutil.copytree(self.root, root)
                    if granted:
                        model_sync.change(root, target, lambda entry: dataclasses.replace(
                            entry, settings=dataclasses.replace(entry.settings, grants=dataclasses.replace(entry.settings.grants, allows=('caller',)))))
                    model_sync.apply(root, model_sync.prepare(root, {
                        caller: model_sync.Create(Entry(App('demo'), web(), Settings(grants=Grants(calls=('hello',)))))}))
                    for name in ('release.yaml', 'instance.yaml'):
                        (target.folder(root) / name).unlink()
                    if marker == 'folder':
                        target.unit_file(root).unlink()
                    else:
                        for name in render.MANAGED_FILES:
                            (target.folder(root) / name).unlink(missing_ok=True)
                    before = {p.relative_to(root): p.read_bytes() for p in root.rglob('*') if p.is_file()}
                    with self.assertRaisesRegex(EditError, 'hello/staging is called by caller'):
                        model_sync.sync(root)
                    self.assertEqual({p.relative_to(root): p.read_bytes() for p in root.rglob('*') if p.is_file()}, before)

    def test_waiting_calls_to_arbitrary_absent_apps_are_not_orphan_removals(self):
        caller = Instance('caller', 'staging')
        model_sync.apply(self.root, model_sync.prepare(self.root, {
            caller: model_sync.Create(Entry(App('demo'), web(), Settings(grants=Grants(calls=('missing',)))))}))
        unrelated = self.root / 'apps/missing/staging/notes.yaml'
        unrelated.parent.mkdir(parents=True)
        unrelated.write_text('keep: true\n')
        before = self.snapshot()
        self.assertEqual(model_sync.sync(self.root), [])
        self.assertEqual(self.snapshot(), before)

    def test_partial_or_malformed_records_are_errors_without_any_writes(self):
        folder = self.root / 'apps/hello/prod'
        original = {name: (folder / name).read_bytes() for name in ('release.yaml', 'instance.yaml')}
        for name in original:
            for text in (None, b'invalid: [', b'version: 1\nunknown: true\n'):
                with self.subTest(name=name, text=text):
                    if text is None:
                        (folder / name).unlink()
                    else:
                        (folder / name).write_bytes(text)
                    before = self.snapshot()
                    with self.assertRaises(EditError):
                        model_sync.sync(self.root)
                    self.assertEqual(self.snapshot(), before)
                    (folder / name).write_bytes(original[name])

    def test_invalid_shared_files_abort_a_removal_before_any_write(self):
        for path, text in ((render.REGISTRY_FILE, 'resources: wrong\n'),
                           (render.REGISTRY_FILE, 'resources: &units []\n'),
                           (render.RELOADER_FILE, 'spec: {values: {reloader: {namespaces: wrong}}}\n'),
                           (render.RELOADER_FILE, 'spec: {values: {reloader: {namespaces: [{}]}}}\n')):
            with self.subTest(path=path, text=text):
                file = self.root / path
                original = file.read_bytes()
                file.write_text(text)
                before = self.snapshot()
                with self.assertRaises(EditError):
                    edit.remove(self.root, 'hello', 'prod')
                self.assertEqual(self.snapshot(), before, 'records, units and outputs are untouched')
                file.write_bytes(original)

    def test_explicit_remove_keeps_unrelated_files_even_for_the_last_environment(self):
        prod = self.root / 'apps/hello/prod'
        (prod / 'notes.yaml').write_text('keep: true\n')
        model_sync.apply(self.root, model_sync.prepare(self.root, {Instance('hello', 'staging'): model_sync.Remove()}))
        model_sync.apply(self.root, model_sync.prepare(self.root, {Instance('hello', 'prod'): model_sync.Remove()}))
        self.assertEqual((prod / 'notes.yaml').read_text(), 'keep: true\n')
        self.assertFalse((prod.parent / 'app.yaml').exists())
        self.assertFalse((prod / 'release.yaml').exists())
        self.assertFalse((prod / 'helmrelease.yaml').exists())

    def test_explicit_remove_of_records_only_instance_cannot_resurrect_it(self):
        for last_environment in (False, True):
            with self.subTest(last_environment=last_environment), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                target = Instance('fresh', 'prod')
                entry = Entry(App('demo'), web(), Settings())
                world = {target: entry}
                if not last_environment:
                    world[Instance('fresh', 'staging')] = entry
                for path, text in store.documents(world).items():
                    (root / path).parent.mkdir(parents=True, exist_ok=True)
                    (root / path).write_text(text)
                folder = target.folder(root)
                inputs = {'events.sops.yaml': 'sops: {}', 'secret.sops.yaml': 'sops: {}',
                          'offers/source.yaml': 'api: openapi.json\n', 'offers/openapi.json': '{}'}
                unrelated = {'notes.yaml': 'keep: true\n', 'data/db': 'keep data', 'offers/notes.txt': 'keep notes'}
                for name, text in (inputs | unrelated).items():
                    (folder / name).parent.mkdir(parents=True, exist_ok=True)
                    (folder / name).write_text(text)
                with redirect_stdout(io.StringIO()):
                    edit.remove(root, target.app, target.env)
                for name in inputs.keys() | {'release.yaml', 'instance.yaml'}:
                    self.assertFalse((folder / name).exists(), name)
                self.assertEqual({name: (folder / name).read_text() for name in unrelated}, unrelated)
                self.assertEqual((folder.parent / 'app.yaml').exists(), not last_environment)
                model_sync.sync(root)
                found, problems = store.world(root)
                self.assertEqual(problems, [])
                self.assertNotIn(target, found)
                self.assertFalse((folder / 'helmrelease.yaml').exists(), 'sync must not resurrect the removed instance')
                self.assertEqual(model_sync.sync(root), [], 'the result is idempotent')

    def test_other_instances_render_failure_refuses_removal_without_writes(self):
        record = self.root / 'apps/hello/staging/release.yaml'
        doc = yamlio.load(record.read_text())
        doc['database'] = 'sqlite'  # valid record, but cannot render without an allocation
        record.write_text(yamlio.dump(doc))
        before = self.snapshot()
        with self.assertRaisesRegex(EditError, 'has no database allocation'):
            model_sync.prepare(self.root, {Instance('hello', 'prod'): model_sync.Remove()})
        self.assertEqual(self.snapshot(), before)

    def test_a_root_without_a_registry_keeps_its_units_and_creates_still_get_one(self):
        each = Instance('fresh', 'staging')
        entry = Entry(App('demo'), web(), Settings())
        registry = self.root / render.REGISTRY_FILE
        model_sync.apply(self.root, model_sync.prepare(self.root, {each: model_sync.Create(entry)}))
        self.assertIn(f'{each.unit}.yaml', registry.read_text(), 'a create registers its unit')
        registry.unlink()
        each.unit_file(self.root).unlink()
        model_sync.sync(self.root)
        self.assertFalse(each.unit_file(self.root).exists(), 'scratch sync does not invent missing units')
        second = Instance('second', 'staging')
        model_sync.apply(self.root, model_sync.prepare(self.root, {second: model_sync.Create(entry)}))
        self.assertTrue(second.unit_file(self.root).is_file(), 'scratch creation still emits its unit')

    def test_every_target_is_validated_before_any_transition_or_write(self):
        touched = []
        before = self.snapshot()
        with self.assertRaisesRegex(EditError, 'no instance absent/staging'):
            model_sync.prepare(self.root, {
                Instance('hello', 'staging'): model_sync.Update(lambda entry: touched.append(entry) or entry),
                Instance('absent', 'staging'): model_sync.Remove()})
        self.assertEqual(touched, [])
        self.assertEqual(self.snapshot(), before)
        with self.assertRaisesRegex(EditError, 'already exists'):
            model_sync.prepare(self.root, {Instance('hello', 'staging'): model_sync.Create(Entry(App('demo'), web(), Settings()))})
        self.assertEqual(self.snapshot(), before)

    def test_sync_preserves_unrelated_yaml_beside_active_instance_outputs(self):
        folder = self.root / 'apps/hello/prod'
        unrelated = folder / 'notes.yaml'
        unrelated.write_text('kind: Note\n')
        model_sync.sync(self.root)
        self.assertEqual(unrelated.read_text(), 'kind: Note\n')

    def test_arbitrary_merged_job_records_with_invalid_schedule_do_not_render_or_write(self):
        folder = self.root / 'apps/nightly/staging'
        folder.mkdir(parents=True)
        (folder.parent / 'app.yaml').write_text(store.text(App('demo').document()))
        (folder / 'release.yaml').write_text(store.text(Release(IMAGE, 'job', 65532).document()))
        (folder / 'instance.yaml').write_text(store.text({'version': 1, 'schedule': '99 99 * * *'}))
        before = {path.relative_to(self.root): path.read_bytes() for path in self.root.rglob('*') if path.is_file()}
        self.assertIn(Instance('nightly', 'staging'), store.world(self.root)[0], 'records parse; validation belongs to rendering')
        with redirect_stdout(io.StringIO()), mock.patch('sys.stderr', new_callable=io.StringIO) as errors:
            self.assertEqual(cli.main_sync(['--root', str(self.root)]), 2, 'the command Render runs must stop')
        self.assertIn('minute', errors.getvalue())
        self.assertEqual(before, {path.relative_to(self.root): path.read_bytes() for path in self.root.rglob('*') if path.is_file()},
                         'invalid records never reach the write phase, including the registry')

    def test_manually_merged_records_cannot_bypass_secret_or_database_restrictions(self):
        folder = self.root / 'apps/fresh/staging'
        folder.mkdir(parents=True)
        (folder.parent / 'app.yaml').write_text(store.text(App('demo').document()))
        (folder / 'instance.yaml').write_text(store.text(Settings(
            allocations=Allocations(database=Storage('1Gi', 'local-path-retain'))).document()))
        for release in (web(secrets=('HOST_IP',)), web(secrets=('TOKEN', 'TOKEN')),
                        Release(IMAGE, 'worker', 65532, database='sqlite'),
                        Release(IMAGE, 'job', 65532, database='sqlite')):
            with self.subTest(release=release):
                (folder / 'release.yaml').write_text(store.text(release.document()))
                self.assertIn(Instance('fresh', 'staging'), store.world(self.root)[0], 'strictly parsed arbitrary records')
                before = {p.relative_to(self.root): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
                with redirect_stdout(io.StringIO()), mock.patch('sys.stderr', new_callable=io.StringIO):
                    self.assertEqual(cli.main_sync(['--root', str(self.root)]), 2)
                self.assertEqual(before, {p.relative_to(self.root): p.read_bytes() for p in self.root.rglob('*') if p.is_file()},
                                 'no rendered files or registry update after refused records')

    def test_a_hand_edit_the_model_cannot_express_is_reported_by_path(self):
        release = self.root / 'apps/hello/staging/helmrelease.yaml'
        release.write_text(release.read_text().replace('    controllers:', '    extra: true\n    controllers:', 1))
        self.assertEqual(self.problems(), ['apps/hello/staging/helmrelease.yaml: [0].spec.values.extra: committed True, not rendered'])
        (self.root / 'apps/hello/staging/stray.yaml').write_text('kind: ConfigMap\n')
        self.assertIn('apps/hello/staging/stray.yaml: committed, not rendered', self.problems())

    def test_manifest_edits_are_rejected_even_without_another_environment(self):
        """The old drift cases are contracts against this instance's records, not its sibling."""
        shutil.rmtree(self.root / 'apps/hello/prod')
        (self.root / 'clusters/home/app-hello-prod.yaml').unlink()
        registry = self.root / 'clusters/home/kustomization.yaml'
        registry.write_text(registry.read_text().replace('- app-hello-prod.yaml\n', ''))
        release = self.root / 'apps/hello/staging/helmrelease.yaml'
        original = release.read_text()
        cases = (
            (lambda v: v['ingress']['main']['annotations'].pop(
                'traefik.ingress.kubernetes.io/router.middlewares'), 'router.middlewares'),
            (lambda v: v['controllers']['main']['containers']['main']['image'].update(
                repository='docker.io/library/nginx'), 'image.repository'),
            (lambda v: v['ingress']['main']['hosts'][0]['paths'].append(
                {'path': '/admin', 'service': {'identifier': 'main', 'port': 'http'}}), 'hosts[0].paths'),
            (lambda v: v['defaultPodOptions'].update(automountServiceAccountToken=True), 'automountServiceAccountToken'),
            (lambda v: v['controllers']['main'].update(replicas=3), 'replicas'),
        )
        self.assertEqual(self.problems(), [])
        for change, where in cases:
            with self.subTest(where=where):
                doc = yamlio.load(original)
                change(doc['spec']['values'])
                release.write_text(yamlio.dump(doc))
                self.assertTrue(any(where in line for line in self.problems()), self.problems())
        release.write_text(original)

    def test_workload_kind_and_command_are_app_contracts_over_release_records(self):
        record = self.root / 'apps/hello/staging/release.yaml'
        original = record.read_text()
        for field, changes in (('kind', {'kind': 'worker', 'port': None, 'healthPath': ''}),
                               ('command', {'command': ['/app/start']})):
            with self.subTest(field=field):
                doc = yamlio.load(original)
                doc.update(changes)
                record.write_text(yamlio.dump(doc))
                _, problems = store.world(self.root)
                self.assertTrue(any(f'{field} must match hello/prod' in line for line in problems), problems)
        record.write_text(original)

    def test_a_new_release_can_precede_production_without_a_manifest_allowlist(self):
        record = self.root / 'apps/hello/staging/release.yaml'
        doc = yamlio.load(record.read_text())
        doc.update(port=9090, healthPath='/ready', uid=1000, telemetry=True,
                   description='A new release', apiPath='/openapi.json', resources={'memory': '64Mi'})
        record.write_text(yamlio.dump(doc))
        with redirect_stdout(io.StringIO()):
            edit.sync(self.root)
        self.assertEqual(self.problems(), [])
        world, problems = store.world(self.root)
        self.assertEqual(problems, [])
        self.assertNotEqual(world[Instance('hello', 'prod')].release, world[Instance('hello', 'staging')].release)

    def test_explicit_policy_paths_check_records_and_only_the_selected_manifest_files(self):
        staging = self.root / 'apps/hello/staging'
        prod = self.root / 'apps/hello/prod/helmrelease.yaml'
        prod.write_text(prod.read_text().replace('    controllers:', '    extra: true\n    controllers:', 1))
        # Kubernetes safety checks have their own tests; isolate the CLI's records check here.
        with mock.patch.object(policy, 'evaluate', return_value=[]), redirect_stdout(io.StringIO()) as out:
            self.assertEqual(policy.main([str(staging)]), 0, out.getvalue())
            release = staging / 'helmrelease.yaml'
            release.write_text(release.read_text().replace('    controllers:', '    extra: true\n    controllers:', 1))
            self.assertEqual(policy.main([str(staging)]), 1)
            self.assertIn('spec.values.extra', out.getvalue())
            record = staging / 'instance.yaml'
            record.write_text(record.read_text() + 'unknown: true\n')
            self.assertEqual(policy.main([str(staging)]), 1)
            self.assertIn('unknown field unknown', out.getvalue())

    def test_a_team_label_edited_by_hand_is_reported(self):
        """The team is the App record's (apps/<app>/app.yaml): a namespace that says otherwise was edited by hand."""
        namespace = self.root / 'apps/hello/prod/namespace.yaml'
        namespace.write_text(namespace.read_text().replace('platform.swhurl.com/team: demo', 'platform.swhurl.com/team: payments'))
        self.assertIn("apps/hello/prod/namespace.yaml: [0].metadata.labels.platform.swhurl.com/team: committed 'payments', "
                      "rendered 'demo'", self.problems())


if __name__ == '__main__':
    unittest.main()
