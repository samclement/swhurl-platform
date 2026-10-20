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

from swhurl import ROOT, yamlio
from swhurl.apps import derive, edit, policy
from swhurl.apps.contract import JOB_SCHEDULE
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
            derive.sync(self.root)
        self.assertTrue((prod / 'helmrelease.yaml').is_file())
        self.assertTrue(unit.is_file())
        self.assertIn('app-hello-prod.yaml', registry.read_text())
        self.assertEqual(self.problems(), [])
        registry.unlink()  # a tree without the registry (a test tree) gets no unit
        unit.unlink()
        with redirect_stdout(io.StringIO()):
            derive.sync(self.root)
        self.assertFalse(unit.exists())

    def test_arbitrary_merged_job_records_with_invalid_schedule_do_not_render_or_write(self):
        folder = self.root / 'apps/nightly/staging'
        folder.mkdir(parents=True)
        (folder.parent / 'app.yaml').write_text(store.text(App('demo').document()))
        (folder / 'release.yaml').write_text(store.text(Release(IMAGE, 'job', 65532).document()))
        (folder / 'instance.yaml').write_text(store.text({'version': 1, 'schedule': '99 99 * * *'}))
        before = {path.relative_to(self.root): path.read_bytes() for path in self.root.rglob('*') if path.is_file()}
        self.assertIn(Instance('nightly', 'staging'), store.world(self.root)[0], 'records parse; validation belongs to rendering')
        with redirect_stdout(io.StringIO()), mock.patch('sys.stderr', new_callable=io.StringIO) as errors:
            self.assertEqual(edit.main_sync(['--root', str(self.root)]), 2, 'the command Render runs must stop')
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
                    self.assertEqual(edit.main_sync(['--root', str(self.root)]), 2)
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
            derive.sync(self.root)
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
