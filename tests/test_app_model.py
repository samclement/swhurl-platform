"""The app model (docs/plan.md section 24): records, rendering, and the shadow that compares them with the manifests."""
import dataclasses
import io
import re
import shutil
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

from swhurl import ROOT
from swhurl.apps import derive, edit
from swhurl.apps.instance import Instance
from swhurl.apps.model import generated, render, store
from swhurl.apps.model.draft import EVENTS_SECRET, Entry, Platform, RenderError
from swhurl.apps.model.records import (
    Allocations,
    App,
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
        settings = Settings('public', 'orders.example.com', 'letsencrypt-staging', ('inventory',), ('shop.visited',),
                            ('api.example.com:443',), 2, Resources(memory='256Mi'), '', 'no-escalation=needs raw sockets',
                            Allocations(30101, 'UDFZ', Storage('1Gi', 'local-path-retain')))
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
        digest = files[path][0]['metadata']['annotations'][render.ANNOTATION]
        before_hash = deepcopy(files)
        del before_hash[path][0]['metadata']['annotations'][render.ANNOTATION]
        if not before_hash[path][0]['metadata']['annotations']:
            del before_hash[path][0]['metadata']['annotations']
        self.assertEqual(render.fingerprint(before_hash), digest, 'the hash does not include itself')
        self.assertEqual(render.fingerprint(dict(reversed(list(before_hash.items())))), digest)
        changed_team = self.render({each: dataclasses.replace(entry, app=App('another'))}, each)
        self.assertNotEqual(changed_team[path][0]['metadata']['annotations'][render.ANNOTATION], digest,
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
        self.assertEqual(resources(web(resources=floor), Settings(capacity=Resources(memory='256Mi', cpu='50m'))),
                         {'requests': {'cpu': '100m', 'memory': '256Mi'}, 'limits': {'memory': '384Mi'}})

    def test_records_that_cannot_be_rendered_together_are_refused(self):
        orders, shop = Instance('orders', 'staging'), Instance('shop', 'staging')
        cases = (
            ({orders: Entry(App('p'), web(), Settings(calls=('shop',)))}, orders, 'calls shop, which has no staging instance'),
            ({orders: Entry(App('p'), web(), Settings(calls=('shop',))),
              shop: Entry(App('p'), Release(IMAGE, 'worker', 1), Settings())}, orders, 'a worker: it has no address'),
            ({orders: Entry(App('p'), web(), Settings(subscriptions=('shop.paid',))),
              shop: Entry(App('p'), web(), Settings())}, orders, 'subscribes to shop.paid, which shop/staging does not publish'),
            ({orders: Entry(App('p'), web(publishes=('created',)), Settings())}, orders, 'declares events but has no NATS key'),
            ({orders: Entry(App('p'), web(database='sqlite'), Settings())}, orders, 'declares sqlite but has no database allocation'),
        )
        for world, each, message in cases:
            with self.subTest(message=message), self.assertRaisesRegex(RenderError, message):
                self.render(world, each)
        with self.assertRaisesRegex(RenderError, 'has no egress port allocated'):
            render.instance(orders, {orders: Entry(App('p'), web(), Settings())}, Platform())

    def test_a_publisher_and_its_subscriber_share_one_user_list(self):
        orders, shop = Instance('orders', 'staging'), Instance('shop', 'staging')
        key = frozenset({EVENTS_SECRET})
        world = {orders: Entry(App('p'), web(publishes=('created',)), Settings(allocations=Allocations(events_key='UORDERS')), key),
                 shop: Entry(App('p'), web(), Settings(subscriptions=('orders.created',),
                                                       allocations=Allocations(events_key='USHOP')), key)}
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

    def test_settings_written_by_the_commands_render_as_written(self):
        (self.root / 'platform/egress').mkdir(parents=True)
        shutil.copy(ROOT / 'platform/egress/proxy.yaml', self.root / 'platform/egress/proxy.yaml')
        with redirect_stdout(io.StringIO()):
            derive.sync(self.root)  # every instance gets its egress port, as on the cluster
            edit.expose(self.root, 'hello', 'prod', 'public', 'hello.example.com')
            edit.set_egress(self.root, 'hello', 'staging', add=['api.example.com', '*.example.org:8443'])
            edit.set_alerts(self.root, 'hello', {'errors': 'none', 'error-logs': '20'})
            edit.scale(self.root, 'hello', 'staging', replicas=2, memory='256Mi')
        settings = self.root / 'apps/hello/prod/instance.yaml'  # a reviewed exception, written in the record as documented
        settings.write_text(settings.read_text() + 'exceptions: no-escalation=needs raw sockets\n')
        with redirect_stdout(io.StringIO()):
            derive.sync(self.root)
        self.assertEqual(self.problems(), [])
        entries, _ = store.world(self.root)
        prod = entries[Instance('hello', 'prod')]
        self.assertEqual((prod.settings.exposure, prod.settings.host), ('public', 'hello.example.com'))
        self.assertEqual(prod.settings.exceptions, 'no-escalation=needs raw sockets')
        self.assertEqual(entries[Instance('hello', 'staging')].settings.replicas, 2)
        self.assertEqual(prod.app.alerts, entries[Instance('hello', 'staging')].app.alerts)

    def test_a_hand_edit_the_model_cannot_express_is_reported_by_path(self):
        release = self.root / 'apps/hello/staging/helmrelease.yaml'
        release.write_text(release.read_text().replace('    controllers:', '    extra: true\n    controllers:', 1))
        self.assertEqual(self.problems(), ['apps/hello/staging/helmrelease.yaml: [0].spec.values.extra: committed True, not rendered'])
        (self.root / 'apps/hello/staging/stray.yaml').write_text('kind: ConfigMap\n')
        self.assertIn('apps/hello/staging/stray.yaml: committed, not rendered', self.problems())

    def test_a_team_label_edited_by_hand_is_reported(self):
        """The team is the App record's (apps/<app>/app.yaml): a namespace that says otherwise was edited by hand."""
        namespace = self.root / 'apps/hello/prod/namespace.yaml'
        namespace.write_text(namespace.read_text().replace('platform.swhurl.com/team: demo', 'platform.swhurl.com/team: payments'))
        self.assertIn("apps/hello/prod/namespace.yaml: [0].metadata.labels.platform.swhurl.com/team: committed 'payments', "
                      "rendered 'demo'", self.problems())


if __name__ == '__main__':
    unittest.main()
