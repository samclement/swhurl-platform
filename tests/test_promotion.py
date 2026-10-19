"""Promotion decisions use Git state and fail closed on incomplete or stale observations."""
import dataclasses
import json
import shutil
import tempfile
import unittest
from pathlib import Path

from swhurl import ROOT
from swhurl.apps import contract, ops, promotion, state
from swhurl.apps.instance import Instance
from swhurl.apps.yaml_file import EditError

DIGEST = 'sha256:' + 'a' * 64
MANIFESTS = ROOT / 'tests/fixtures/manifests'


def release(tag='2-abcdef0', digest=DIGEST):
    return {'spec': {'values': {'controllers': {'main': {'containers': {'main': {
        'image': {'repository': 'ghcr.io/example/app', 'tag': tag, 'digest': digest}}}}}}}}


class PromotionTests(unittest.TestCase):
    def test_create_same_update_and_rollback(self):
        for target, expected, rollback in [(None, 'create', False), (release('1-abcdef0'), 'same', False),
                                          (release('1-abcdef0', 'sha256:' + 'b' * 64), 'update', False),
                                          (release('3-abcdef0', 'sha256:' + 'b' * 64), 'update', True)]:
            with self.subTest(expected=expected, rollback=rollback):
                result = promotion.decide(release(), target)
                self.assertEqual((result.outcome, result.rollback), (expected, rollback))

    def test_unpinned_and_ambiguous_image_refused(self):
        for source in (release(digest=''), {'spec': {'values': {'controllers': {}}}}):
            with self.assertRaises(EditError):
                promotion.decide(source, None)

    def test_partial_production_is_not_missing(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            shutil.copytree(ROOT / 'tests/fixtures/instance/apps/hello/staging', root / 'apps/hello/staging')
            source, target = promotion.read(root, 'hello')
            self.assertEqual(promotion.decide(source, target).outcome, 'create')
            (root / 'apps/hello/prod').mkdir()
            with self.assertRaises(EditError):
                promotion.read(root, 'hello')

    def test_readiness_and_review_failures(self):
        image = 'ghcr.io/example/app:2-abcdef0@' + DIGEST
        status = ops.InstanceStatus(Instance('example', 'staging'), 'main@sha1:abc', 'main@sha1:abc',
                                    ('True', ''), ('True', ''), image, ['ghcr.io/example/app@' + DIGEST],
                                    [ops.Replicas('Deployment/example', 1, 1)], [], [], [])
        self.assertEqual(promotion.source_problem(status, image), '')
        for changes in ({'unit_suspended': True}, {'release_suspended': True}, {'running_images': []},
                        {'release': None}, {'unit': ('False', 'failed')}, {'replicas': []},
                        {'replicas': [ops.Replicas('Deployment/example', 0, 0)]},
                        {'desired_revision': 'main@sha1:def'}, {'desired_image': image.replace('2-', '3-')}):
            with self.subTest(changes=changes):
                self.assertTrue(promotion.source_problem(dataclasses.replace(status, **changes), image))
        self.assertTrue(promotion.source_problem(None, image))
        behind = dataclasses.replace(status, desired_revision='main@sha1:def')
        self.assertIn('Flux is still applying the latest commit on main', promotion.source_problem(behind, image),
                      "whether the unapplied commit changes this app is not known: say so, not 'unhealthy'")

    def test_a_commit_that_leaves_staging_alone_does_not_hold_its_promotion(self):
        """Any commit to main puts every unit behind for a few seconds; only one to the instance's own files is its change."""
        image = 'ghcr.io/example/app:2-abcdef0@' + DIGEST
        behind = ops.InstanceStatus(Instance('example', 'staging'), 'main@sha1:def', 'main@sha1:abc',
                                    ('True', ''), ('True', ''), image, ['ghcr.io/example/app@' + DIGEST],
                                    [ops.Replicas('Deployment/example', 1, 1)], [], [], [])
        asked = []

        def changed(paths):
            return lambda applied, newest: asked.append((applied, newest)) or paths
        for paths, problem in ((['docs/apps.md', 'apps/other/staging/helmrelease.yaml', 'apps/example/prod/helmrelease.yaml',
                                 'apps/example-two/staging/helmrelease.yaml', 'clusters/home/app-example-prod.yaml'], ''),
                               ([], ''),
                               (['apps/example/staging/helmrelease.yaml'], 'a change to staging is in Git'),
                               (['apps/example/staging/offers/openapi.json'], 'a change to staging is in Git'),
                               (['clusters/home/app-example-staging.yaml'], 'a change to staging is in Git'),
                               (None, 'Flux is still applying the latest commit on main')):
            with self.subTest(paths=paths):
                found = promotion.source_problem(promotion.settled(behind, changed(paths)), image)
                self.assertIn(problem, found) if problem else self.assertEqual(found, '')
        self.assertEqual(set(asked), {('abc', 'def')}, 'the two commits, without the branch')
        # Its unit waits for a dependency or re-applies the same files after such a commit: that is not about this instance.
        for unit, reason in ((('False', "dependency 'flux-system/platform-nats' is not ready"), 'DependencyNotReady'),
                             (('Unknown', 'Reconciliation in progress'), 'Progressing')):
            waiting = dataclasses.replace(behind, unit=unit, unit_reason=reason)
            with self.subTest(unit=unit):
                self.assertEqual(state.verdict(waiting).key, 'updating', 'unasked, every reader still sees it updating')
                self.assertEqual(promotion.source_problem(promotion.settled(waiting, changed([])), image), '')
                self.assertTrue(promotion.source_problem(promotion.settled(waiting, changed(['apps/example/staging/namespace.yaml'])), image))
            with self.subTest(unit=unit, render_applied=True):
                self.assertEqual(state.verdict(waiting, render_applied=True).key, 'healthy',
                                 'a proved render hash says the waiting is not about this instance')
                same = dataclasses.replace(waiting, desired_revision='main@sha1:abc')
                self.assertEqual(state.verdict(same, render_applied=True).key, 'healthy', 'nor is re-applying the same revision')
        failing = dataclasses.replace(behind, unit=('False', 'kustomize build failed'), unit_reason='BuildFailed')
        self.assertIn('healthy', promotion.source_problem(promotion.settled(failing, changed([])), image))
        self.assertEqual(state.verdict(failing, render_applied=True).key, 'failing', 'a failing unit still counts')
        self.assertEqual(state.verdict(dataclasses.replace(behind, unit_suspended=True), render_applied=True).key, 'suspended')
        self.assertEqual(state.verdict(dataclasses.replace(behind, replicas=[ops.Replicas('Deployment/example', 1, 0)]),
                                       render_applied=True).key, 'updating', 'workload facts still count')
        unhealthy = dataclasses.replace(behind, release=('False', 'upgrade failed'))
        self.assertIn('healthy', promotion.source_problem(promotion.settled(unhealthy, changed([])), image))
        applied = dataclasses.replace(behind, desired_revision='main@sha1:abc')
        asked.clear()
        self.assertIs(promotion.settled(applied, changed(['apps/example/staging/helmrelease.yaml'])), applied)
        self.assertEqual(asked, [], 'an applied instance needs no comparison')


class FirstProductionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for rel in ('clusters/home/kustomization.yaml', 'platform/reloader/helmrelease.yaml', '.sops.yaml'):
            dst = self.root / rel
            dst.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / rel, dst)

    def staging(self, kind='web', *extra):
        from swhurl.apps import new
        args = new.parse_args(['example', '--manifest', str(MANIFESTS / f'{kind}.yaml'), '--team', 'demo', '--env', 'staging',
                               '--image', 'ghcr.io/example/app:2-abcdef0@' + DIGEST,
                               '--no-auto-deploy', *extra])
        new.generate(args, self.root)
        return promotion.read(self.root, 'example')[0]

    def promote(self, runner=None, **kwargs):
        import io
        from contextlib import redirect_stdout

        from swhurl.apps import edit
        with redirect_stdout(io.StringIO()):
            return edit.promote(self.root, 'example', runner=runner, **kwargs)

    def encrypting(self):
        """sops as a FakeRunner: marks the file encrypted, and the placeholders never hold a staging value."""
        from swhurl.run import FakeRunner, Result

        def encrypt(args, _):
            path = Path(args[-1])
            self.assertNotIn('staging-value', path.read_text())
            path.write_text(path.read_text().replace('REPLACE_ME', 'ENC[new-placeholder]') + 'sops: {}\n')
            return Result(args)
        return FakeRunner().on('sops', handler=encrypt)

    def test_web_worker_and_sqlite_conversion_preserves_settings(self):
        for kind, extra in [('web', []), ('worker', []), ('web', ['--database', 'sqlite']),
                            ('job', ['--schedule', '*/10 * * * *'])]:
            with self.subTest(kind=kind, extra=extra):
                source = self.staging(kind, *extra)
                values = source['spec']['values']
                dst = self.promote()
                production = promotion.YamlFile(dst / 'helmrelease.yaml').data
                self.assertEqual(promotion.image_of(production), promotion.image_of(source))
                self.assertEqual(production['metadata']['namespace'], 'example-prod')
                expected = json.loads(json.dumps(values['controllers']['main']))
                expected['containers']['main']['env']['SWHURL_ENV'] = 'prod'  # production's own identity
                self.assertEqual(production['spec']['values']['controllers']['main'], expected)
                if kind == 'web':
                    self.assertEqual(production['spec']['values']['ingress']['main']['hosts'][0]['host'],
                                     'example.homelab.swhurl.com')
                else:
                    self.assertNotIn('ingress', production['spec']['values'])
                if kind == 'job':
                    self.assertEqual(production['spec']['values']['controllers']['main']['cronjob']['schedule'], '*/10 * * * *',
                                     "production starts with staging's schedule")
                elif extra:
                    self.assertTrue(production['spec']['values']['persistence']['data']['retain'])
                    self.assertNotIn('existingClaim', production['spec']['values']['persistence']['data'])
                self.assertIn('app-example-prod.yaml', (self.root / 'clusters/home/kustomization.yaml').read_text(),
                              'its unit is registered')
                shutil.rmtree(self.root / 'apps')
                for unit in (self.root / 'clusters/home').glob('app-example-*.yaml'):
                    unit.unlink()
                shutil.copyfile(ROOT / 'clusters/home/kustomization.yaml', self.root / 'clusters/home/kustomization.yaml')

    def test_production_keeps_the_team_and_gets_its_own_quota_and_network_policy(self):
        import yaml

        from swhurl.apps import policy
        self.staging('web', '--team', 'payments', '--port', '9000')
        dst = self.promote()
        self.assertEqual(yaml.safe_load((dst / 'namespace.yaml').read_text())['metadata']['labels'][contract.TEAM],
                         'payments')
        network = yaml.safe_load((dst / 'networkpolicy.yaml').read_text())
        self.assertEqual(network['metadata']['namespace'], 'example-prod')
        self.assertEqual(network['spec']['ingress'][0]['ports'], [{'port': 9000, 'protocol': 'TCP'}])
        self.assertEqual(yaml.safe_load((dst / 'resourcequota.yaml').read_text())['metadata']['namespace'], 'example-prod')
        self.assertEqual(policy.drift([dst, self.root / 'apps/example/staging']), [])

    def test_first_promotion_of_a_caller_needs_its_callee_in_production_and_links_them_there(self):
        import yaml

        from swhurl.apps import edit, new, policy
        for name, extra in (('orders', []), ('reports', ['--calls', 'orders'])):
            args = new.parse_args([name, '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'staging',
                                   '--image', f'ghcr.io/example/{name}:2-abcdef0@' + DIGEST, '--no-auto-deploy', *extra])
            new.generate(args, self.root)
        edit.set_events(self.root, 'orders', 'staging', publish=['created'], runner=self.encrypting())
        edit.set_events(self.root, 'reports', 'staging', subscribe=['orders.created'], runner=self.encrypting())

        def env(app, environment):
            release = yaml.safe_load((self.root / 'apps' / app / environment / 'helmrelease.yaml').read_text())
            return release['spec']['values']['controllers']['main']['containers']['main']['env']
        with self.assertRaisesRegex(EditError, 'reports calls orders, which has no production instance: promote that first'):
            edit.promote(self.root, 'reports', runner=self.encrypting())
        self.assertFalse((self.root / 'apps/reports/prod').exists())
        edit.promote(self.root, 'orders', runner=self.encrypting())
        self.assertNotIn('SWHURL_ALLOWED_CALLERS', env('orders', 'prod'), 'nobody calls it in production yet')
        self.assertEqual(env('orders', 'prod')['SWHURL_EVENTS_PUBLISH'], 'created',
                         'it publishes what its image does, with a NATS key of its own')
        self.assertNotEqual(env('orders', 'prod')['SWHURL_EVENTS_NKEY'], env('orders', 'staging')['SWHURL_EVENTS_NKEY'])
        edit.promote(self.root, 'reports', runner=self.encrypting())
        self.assertEqual(env('reports', 'prod')['SWHURL_ENV'], 'prod')
        self.assertNotIn('SWHURL_EVENTS_SUBSCRIBE', env('reports', 'prod'), 'what it receives is declared in production')
        self.assertEqual(policy.drift([self.root / 'apps/orders/prod', self.root / 'apps/orders/staging']), [])
        self.assertEqual(env('reports', 'prod')['SWHURL_SERVICE_ORDERS_URL'], env('reports', 'staging')['SWHURL_SERVICE_ORDERS_URL'])
        self.assertEqual(env('orders', 'prod')['SWHURL_ALLOWED_CALLERS'], 'reports')
        instances = [self.root / 'apps' / app / e for app in ('orders', 'reports') for e in ('staging', 'prod')]
        self.assertEqual(policy.check_calls(instances), {})
        self.assertEqual(policy.drift([self.root / 'apps/reports/prod', self.root / 'apps/reports/staging']), [])

    def test_a_staging_the_model_cannot_express_writes_nothing(self):
        source = self.staging()
        doc = promotion.YamlFile(self.root / 'apps/example/staging/helmrelease.yaml')
        doc.data['spec']['values']['controllers']['sidecar'] = {}
        doc.save()
        before = sorted(self.root.rglob('*'))
        with self.assertRaisesRegex(EditError, 'differ from what the platform renders'):
            self.promote()
        self.assertEqual(sorted(self.root.rglob('*')), before)
        del source

    def test_secret_placeholders_are_new_and_failure_leaves_no_target(self):
        from swhurl.apps import edit
        from swhurl.run import FakeRunner
        self.staging('worker', '--secret-keys', 'API_TOKEN')
        encrypted = 'apiVersion: v1\nkind: Secret\nstringData:\n  API_TOKEN: ENC[staging-value]\nsops: {}\n'
        edit.set_secret(self.root, 'example', 'staging', 'API_TOKEN', 'staging-value',
                        runner=FakeRunner().on('sops', stdout=encrypted))
        with self.assertRaisesRegex(EditError, 'SOPS-encrypt'):
            self.promote(runner=FakeRunner().on('sops', returncode=1, stderr='encryption failed'))
        self.assertFalse((self.root / 'apps/example/prod').exists())
        dst = self.promote(runner=self.encrypting())
        secret = (dst / 'secret.sops.yaml').read_text()
        self.assertNotIn('staging-value', secret)
        self.assertIn('namespace: example-prod', secret)
        self.assertIn('example-prod', (self.root / 'platform/reloader/helmrelease.yaml').read_text())

    def test_public_host_refusals(self):
        from swhurl.apps.model import sync as model_sync
        self.staging('web', '--exposure', 'public', '--host', 'staging.example.com')
        world, _ = model_sync.read(self.root)
        with self.assertRaisesRegex(EditError, 'distinct production'):
            promotion.first_production(self.root, world, 'example')
        with self.assertRaisesRegex(EditError, 'outside'):
            promotion.first_production(self.root, world, 'example', host='x.homelab.swhurl.com')
        self.assertEqual(promotion.first_production(self.root, world, 'example', host='example.com').host, 'example.com')
