"""One Git relation meaning for rendering, consent, intent and declared removal dependencies."""
import io
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from dataclasses import replace
from pathlib import Path

from swhurl import interface, yamlio
from swhurl.apps import calling, connections, events, new
from swhurl.apps.errors import EditError
from swhurl.apps.instance import Instance
from swhurl.apps.model import generated, relations, render, store, write
from swhurl.apps.model.draft import EVENTS_SECRET, Entry, Platform, RenderError
from swhurl.apps.model.records import Allocations, App, Grants, Image, Release, Settings
from swhurl.run import FakeRunner


def entry(*, team='demo', kind='web', publishes=(), key='UAPP', **grants):
    release = Release(Image('ghcr.io/example/app', '2-abcdef0', 'sha256:' + 'a' * 64), kind, 1000,
                      port=8080 if kind == 'web' else None, health_path='/' if kind == 'web' else '', publishes=publishes)
    return Entry(App(team), release, Settings(Grants(**grants), allocations=Allocations(events_key=key)),
                 frozenset({EVENTS_SECRET}) if key else frozenset())


class RelationTests(unittest.TestCase):
    platform = Platform(signing_keys='{}', egress=False)

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)

    def save(self, world, *, rendered=True):
        for path, text in store.documents(world).items():
            path = self.root / path
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
        for each, found in world.items():
            if EVENTS_SECRET in found.encrypted:
                (each.folder(self.root) / events.SECRET_FILE).write_text('sops: {}')
        for path, value in ((events.CONTROLLER_KEY_FILE, 'UCONTROLLER'), (events.CONSOLE_KEY_FILE, 'UCONSOLE')):
            path = self.root / path
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(value)
        (self.root / calling.SIGNING_KEYS_FILE).write_text('{}')
        if rendered:
            write.apply(self.root, write.plan(self.root, world, self.platform, frozenset(world)))

    def snapshot(self):
        return {p.relative_to(self.root): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}

    def variables(self, each, world):
        files = render.instance(each, world, self.platform)
        release = files[Path(f'apps/{each.app}/{each.env}/helmrelease.yaml')][0]
        return release['spec']['values']['controllers']['main']['containers']['main']['env'], files

    def test_absence_and_app_or_team_consent_in_both_environments(self):
        for env in ('staging', 'prod'):
            caller, target = Instance('reader', env), Instance('orders', env)
            world = {caller: entry(calls=('orders',))}
            self.assertIn('to exist', relations.evaluate(caller, world)[0].pending)
            # The other environment cannot satisfy a declaration.
            world[Instance('orders', 'prod' if env == 'staging' else 'staging')] = entry(allows=('reader',))
            self.assertIn('to exist', relations.evaluate(caller, world)[0].pending)
            for grants, connected in (((), False), (('someone',), False), (('reader',), True),
                                       (('team:demo',), True), (('team:other',), False)):
                with self.subTest(env=env, grants=grants):
                    world[target] = entry(allows=grants)
                    relation = relations.evaluate(caller, world)[0]
                    self.assertEqual(not relation.pending, connected)
                    variables, files = self.variables(caller, world)
                    self.assertEqual(interface.callee_variable('orders') in variables, connected)
                    release = files[Path(f'apps/reader/{env}/helmrelease.yaml')][0]
                    tokens = release['spec']['values']['persistence']['tokens']['sources']
                    self.assertEqual([t['serviceAccountToken']['audience'] for t in tokens],
                                     ['orders', 'notify'] if connected else ['notify'])
                    variables, _ = self.variables(target, world)
                    self.assertEqual(variables.get(interface.CALLERS_VARIABLE), 'reader' if connected else None)

    def test_invalid_targets_are_errors_before_consent_or_identity(self):
        for env in ('staging', 'prod'):
            caller, target = Instance('reader', env), Instance('orders', env)
            for invalid in ('worker', 'job'):
                world = {caller: entry(calls=('orders',), key=''), target: entry(kind=invalid, key='')}
                with self.subTest(env=env, kind=invalid), self.assertRaisesRegex(RenderError, 'no address to call'):
                    relations.evaluate(caller, world)
            for grants in (dict(calls=('reader',)), dict(subscriptions=('reader.created',)),
                           dict(calls=('Bad',)), dict(subscriptions=('orders',)), dict(calls=('orders\n',))):
                with self.subTest(env=env, grants=grants), self.assertRaisesRegex(RenderError, 'itself|malformed'):
                    relations.evaluate(caller, {caller: entry(**grants)})
            with self.assertRaisesRegex(RenderError, 'does not publish'):
                relations.evaluate(caller, {caller: entry(subscriptions=('orders.created',)), target: entry()})

    def test_pending_events_have_no_consumer_variable_or_nats_subscription_permissions(self):
        for env in ('staging', 'prod'):
            each = Instance('reader', env)
            world = {each: entry(subscriptions=('missing.created',), publishes=('seen',))}
            variables, files = self.variables(each, world)
            self.assertNotIn(interface.SUBSCRIBE_VARIABLE, variables)
            self.assertEqual(variables[interface.PUBLISH_VARIABLE], 'seen')
            docs = files[Path(f'apps/reader/{env}/events.yaml')]
            self.assertEqual([doc['kind'] for doc in docs], ['Stream'])

    def test_connected_order_and_names_are_unchanged(self):
        each = Instance('reader', 'staging')
        world = {each: entry(calls=('zulu', 'orders'), subscriptions=('orders.paid', 'orders.created')),
                 Instance('orders', 'staging'): entry(publishes=('created', 'paid'), allows=('reader',), key='UORDERS'),
                 Instance('zulu', 'staging'): entry(allows=('team:demo',), key='UZULU')}
        variables, files = self.variables(each, world)
        self.assertEqual([name for name in variables if name.startswith('SWHURL_SERVICE_')],
                         ['SWHURL_SERVICE_ZULU_URL', 'SWHURL_SERVICE_ORDERS_URL'])
        self.assertEqual(variables[interface.SUBSCRIBE_VARIABLE], 'orders.paid,orders.created')
        docs = files[Path('apps/reader/staging/events.yaml')]
        self.assertEqual([(d['metadata']['name'], d['spec']['streamName'], d['spec']['durableName']) for d in docs],
                         [('orders-paid', 'staging-orders', 'reader_paid'),
                          ('orders-created', 'staging-orders', 'reader_created')])

    def test_consumer_collisions_include_pending_and_are_scoped_to_subscriber_namespace(self):
        base = self.root
        for env in ('staging', 'prod'):
            each = Instance('reader', env)
            for pending in (True, False):
                self.root = base / f'{env}-{pending}'
                world = {each: entry(subscriptions=('a-b.c', 'a.b-c'))}
                if not pending:
                    world.update({Instance('a-b', env): entry(publishes=('c',)),
                                  Instance('a', env): entry(publishes=('b-c',))})
                self.save(world, rendered=False)
                before = self.snapshot()
                with self.subTest(env=env, pending=pending), self.assertRaisesRegex(EditError, 'Consumer name a-b-c collides'):
                    write.prepare(self.root, {}, runner=FakeRunner())
                self.assertEqual(self.snapshot(), before)
                with self.assertRaisesRegex(RenderError, 'collides'):
                    relations.evaluate(each, world)
        # The same name in different subscriber namespaces (including environments) is unambiguous.
        world = {Instance('first', 'staging'): entry(subscriptions=('a-b.c',)),
                 Instance('second', 'staging'): entry(subscriptions=('a.b-c',)),
                 Instance('first', 'prod'): entry(subscriptions=('a.b-c',))}
        for each in world:
            self.assertEqual(len(relations.evaluate(each, world)), 1)

    def test_publisher_arrival_missing_event_and_event_removal_are_zero_write_failures(self):
        for env in ('staging', 'prod'):
            with self.subTest(env=env):
                # A fresh root for each environment makes snapshots cover the complete proposed change.
                root = self.root / env
                root.mkdir()
                old = self.root
                self.root = root
                subscriber, publisher = Instance('reader', env), Instance('orders', env)
                world = {subscriber: entry(subscriptions=('orders.created',))}
                self.save(world)
                before = self.snapshot()
                runner = FakeRunner()
                with self.assertRaisesRegex(EditError, 'does not publish'):
                    write.prepare(root, {publisher: write.Create(entry())}, runner=runner)
                self.assertEqual(self.snapshot(), before)
                self.assertEqual(runner.calls, [], 'validation precedes allocation/encryption')
                write.apply(root, write.prepare(root, {publisher: write.Create(replace(entry(publishes=('created',)), encrypted=frozenset(), pending={EVENTS_SECRET: 'sops: {}'}))}, runner=runner))
                self.assertEqual(generated.problems(root), [])
                before = self.snapshot()
                with self.assertRaisesRegex(EditError, 'does not publish'):
                    write.prepare(root, {publisher: write.Update(lambda e: replace(e, release=replace(e.release, publishes=())))}, runner=runner)
                self.assertEqual(self.snapshot(), before)
                self.root = old

    def test_removal_queries_all_declarations_including_pending_and_record_only(self):
        target = Instance('orders', 'staging')
        for grants, message in ((dict(calls=('orders',)), 'is called by reader'),
                                (dict(subscriptions=('orders.created',)), 'publishes events that reader subscribe to')):
            for recorded_only in (True, False):
                with self.subTest(grants=grants, record_only=recorded_only):
                    world = {target: entry(), Instance('reader', 'staging'): entry(**grants)}
                    # Pending event to an absent app is still a declared dependency of an orphan cleanup.
                    self.save(world, rendered=False)
                    before = self.snapshot()
                    with self.assertRaisesRegex(EditError, message):
                        write.prepare(self.root, {target: write.Remove()}, runner=FakeRunner())
                    self.assertEqual(self.snapshot(), before)
                    del world[target]
                    if not recorded_only:
                        target.unit_file(self.root).parent.mkdir(parents=True, exist_ok=True)
                        target.unit_file(self.root).write_text('stale unit')
                    with self.assertRaisesRegex(EditError, message):
                        write.check_removal(target, world)

    def test_merged_orphan_cleanup_blocks_pending_calls_and_events_before_writes(self):
        target = Instance('orders', 'staging')
        subscriber = Instance('reader', 'staging')
        for grants, message in ((dict(calls=('orders',)), 'is called by reader'),
                                (dict(subscriptions=('orders.created',)), 'publishes events that reader subscribe to')):
            self.save({subscriber: entry(**grants)}, rendered=False)
            target.unit_file(self.root).parent.mkdir(parents=True, exist_ok=True)
            target.unit_file(self.root).write_text('stale unit')
            before = self.snapshot()
            with self.assertRaisesRegex(EditError, message):
                write.prepare(self.root, {}, runner=FakeRunner())
            self.assertEqual(self.snapshot(), before)

    def test_generated_checks_cover_tampering_missing_keys_seed_and_users_file(self):
        each = Instance('reader', 'staging')
        world = {each: entry(subscriptions=('orders.created',)),
                 Instance('orders', 'staging'): entry(publishes=('created',), key='UORDERS')}
        self.save(world)
        self.assertEqual(generated.problems(self.root), [])
        consumer = each.folder(self.root) / 'events.yaml'
        consumer.write_text(consumer.read_text().replace('reader_created', 'reader_other'))
        self.assertTrue(any('durableName' in p for p in generated.problems(self.root)))
        write.apply(self.root, write.plan(self.root, world, self.platform))
        world[each] = entry()
        self.save(world)
        (each.folder(self.root) / events.SECRET_FILE).unlink()
        self.assertIn(f'{each}: has no NATS key; make app-sync creates it', generated.problems(self.root))
        world[each] = entry(key='')
        self.save(world)
        self.assertIn(f'{each}: has no NATS key; make app-sync creates it', generated.problems(self.root))

    def test_creation_accepts_an_absent_call_target(self):
        with redirect_stdout(io.StringIO()):
            args = new.parse_args(['created', '--env=staging', '--team=demo', '--image=example/app:1',
                                   '--calls=absent', '--health-path=/', '--no-auto-deploy', '--root', str(self.root)])
            new.generate(args, self.root, runner=FakeRunner())
        self.assertNotIn(interface.callee_variable('absent'), yamlio.load(
            (self.root / 'apps/created/staging/helmrelease.yaml').read_text())['spec']['values']['controllers']['main']['containers']['main']['env'])

    def test_evaluator_is_standard_library_only_and_policy_keeps_no_ruamel_boundary(self):
        for imports, forbidden in (('from swhurl.apps.model import relations', '("yaml", "ruamel")'),
                                   ('from swhurl.apps import policy', '("ruamel",)')):
            result = subprocess.run([sys.executable, *(['-S'] if 'relations' in imports else []), '-c',
                                     f'import sys; {imports}; assert not any(n.startswith({forbidden}) for n in sys.modules)'],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)


class IntentTests(unittest.TestCase):
    def source(self, world):
        docs = {str(path): text for path, text in store.documents(world).items()}
        return docs, docs.get

    def test_git_pending_consent_lag_and_stale_live_are_distinct_and_removable(self):
        for env in ('staging', 'prod'):
            each, orders = Instance('reader', env), Instance('orders', env)
            world = {each: entry(calls=('orders', 'missing'), subscriptions=('absent.created',)),
                     orders: entry(team='payments')}
            _, source = self.source(world)
            rows, problem = connections.intent(each, (), source)
            self.assertEqual(problem, '')
            self.assertEqual([r.status for r in rows], ['waiting for orders to allow it',
                                                       f'waiting for missing/{env} to exist',
                                                       f'waiting for absent/{env} to exist'])
            self.assertTrue(all(r.direction == 'out' and r.role != 'publishes' for r in rows),
                            'pending calls and subscriptions keep the ordinary removal controls')
            world[orders] = entry(allows=('team:demo',), team='payments')
            _, source = self.source(world)
            rows, _ = connections.intent(each, (), source)
            self.assertEqual(rows[0].what, 'its API, connected in Git, not yet observed live')
            live = connections.rows(calls=('orders',), teams={'orders': 'payments'})
            rows, _ = connections.intent(each, live, source)
            self.assertEqual(rows[0].what, 'its API')
            world[orders] = entry()
            _, source = self.source(world)
            rows, _ = connections.intent(each, live, source)
            self.assertEqual(rows[0].status, 'waiting for orders to allow it; still observed live')
            world[each] = entry()
            _, source = self.source(world)
            rows, _ = connections.intent(each, live, source)
            self.assertEqual(rows[0].status, 'removal in Git, still observed live')

    def test_unavailable_or_invalid_records_never_mean_missing_consent(self):
        each = Instance('reader', 'staging')
        world = {each: entry(calls=('orders',)), Instance('orders', 'staging'): entry(allows=('reader',))}
        docs, _ = self.source(world)
        live = connections.rows(calls=('orders',))
        for bad in (None, lambda path: None,
                    {**docs, 'apps/orders/staging/release.yaml': None}.get,
                    {**docs, 'apps/orders/staging/release.yaml': 'invalid: ['}.get,
                    {**docs, 'apps/orders/staging/release.yaml': yamlio.dump(entry(kind='worker').release.document())}.get):
            with self.subTest(source=bad):
                rows, problem = connections.intent(each, live, bad)
                self.assertEqual(rows, live)
                self.assertEqual(problem, 'Git relation records unavailable; showing live connections only.')
                self.assertFalse(any(r.status for r in rows))
        def failed(path):
            raise OSError('do not expose API details')
        self.assertEqual(connections.intent(each, live, failed)[1],
                         'Git relation records unavailable; showing live connections only.')
