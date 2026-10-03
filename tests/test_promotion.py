"""Promotion decisions use Git state and fail closed on incomplete or stale observations."""
import dataclasses
import shutil
import tempfile
import unittest
from pathlib import Path

from swhurl import ROOT
from swhurl.apps import ops, promotion
from swhurl.apps.yaml_file import EditError

DIGEST = 'sha256:' + 'a' * 64


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
            shutil.copytree(ROOT / 'apps/hello/staging', root / 'apps/hello/staging')
            source, target = promotion.read(root, 'hello')
            self.assertEqual(promotion.decide(source, target).outcome, 'create')
            (root / 'apps/hello/prod').mkdir()
            with self.assertRaises(EditError):
                promotion.read(root, 'hello')

    def test_readiness_and_review_failures(self):
        image = 'ghcr.io/example/app:2-abcdef0@' + DIGEST
        status = ops.InstanceStatus(ops.Instance('example', 'staging'), 'main@sha1:abc', 'main@sha1:abc',
                                    ('True', ''), ('True', ''), image, ['ghcr.io/example/app@' + DIGEST],
                                    [ops.Replicas('Deployment/example', 1, 1)], [], [], [])
        self.assertEqual(promotion.source_problem(status, image, status.applied_revision), '')
        for changes in ({'unit_suspended': True}, {'release_suspended': True}, {'running_images': []},
                        {'release': None}, {'unit': ('False', 'failed')}, {'replicas': []},
                        {'replicas': [ops.Replicas('Deployment/example', 0, 0)]},
                        {'desired_revision': 'main@sha1:def'}, {'desired_image': image.replace('2-', '3-')}):
            with self.subTest(changes=changes):
                self.assertTrue(promotion.source_problem(dataclasses.replace(status, **changes),
                                                         image, status.applied_revision))
        self.assertTrue(promotion.source_problem(None, image, 'abc'))


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
        args = new.parse_args(['example', '--preset', 'swhurl-' + kind, '--env', 'staging',
                               '--image', 'ghcr.io/example/app:2-abcdef0@' + DIGEST,
                               '--no-auto-deploy', *extra])
        new.generate(args, self.root)
        return promotion.read(self.root, 'example')[0]

    def test_web_worker_and_sqlite_conversion_preserves_settings(self):
        from unittest import mock

        from swhurl.apps import policy
        from swhurl.run import FakeRunner
        for kind, extra in [('web', []), ('worker', []), ('worker', ['--database', 'sqlite'])]:
            with self.subTest(kind=kind, extra=extra):
                source = self.staging(kind, *extra)
                values = source['spec']['values']
                values['controllers']['main']['containers']['main']['resources']['requests']['cpu'] = '75m'
                doc = promotion.YamlFile(self.root / 'apps/example/staging/helmrelease.yaml')
                doc.data['spec']['values'] = values
                doc.save()
                from ruamel.yaml.scalarstring import DoubleQuotedScalarString
                namespace = promotion.YamlFile(self.root / 'apps/example/staging/namespace.yaml')
                namespace.data['metadata']['labels']['platform.swhurl.com/promotion-test'] = DoubleQuotedScalarString('true')
                namespace.data['metadata'].setdefault('annotations', {})['custom'] = DoubleQuotedScalarString('3')
                namespace.save()
                runner = FakeRunner()
                with mock.patch.object(policy, 'evaluate', return_value=[]) as evaluate:
                    dst = promotion.create(self.root, 'example', source, runner=runner)
                self.assertIs(evaluate.call_args.args[1], runner)
                production = promotion.YamlFile(dst / 'helmrelease.yaml').data
                self.assertEqual(promotion.image_of(production), promotion.image_of(source))
                self.assertEqual(production['metadata']['namespace'], 'example-prod')
                namespace = promotion.YamlFile(dst / 'namespace.yaml').data['metadata']
                self.assertEqual(namespace['labels']['platform.swhurl.com/promotion-test'], 'true')
                self.assertEqual(namespace['annotations']['custom'], '3')
                self.assertEqual(production['spec']['values']['controllers']['main'], values['controllers']['main'])
                if kind == 'web':
                    self.assertEqual(production['spec']['values']['ingress']['main']['hosts'][0]['host'],
                                     'example.homelab.swhurl.com')
                else:
                    self.assertNotIn('ingress', production['spec']['values'])
                if extra:
                    self.assertTrue(production['spec']['values']['persistence']['data']['retain'])
                    self.assertNotIn('existingClaim', production['spec']['values']['persistence']['data'])
                shutil.rmtree(self.root / 'apps')
                for unit in (self.root / 'clusters/home').glob('app-example-*.yaml'):
                    unit.unlink()

    def test_unsupported_shape_and_policy_failure_write_nothing(self):
        from unittest import mock

        from swhurl.apps import policy
        source = self.staging()
        before = promotion.fingerprint(self.root, 'example', 'prod')
        source['spec']['values']['controllers']['sidecar'] = {}
        with self.assertRaisesRegex(EditError, 'unsupported fields sidecar'):
            promotion.create(self.root, 'example', source)
        source['spec']['values']['controllers'].pop('sidecar')
        with mock.patch.object(policy, 'evaluate', return_value=['non-root: invalid']), \
                self.assertRaisesRegex(EditError, 'policy refused'):
            promotion.create(self.root, 'example', source)
        self.assertFalse((self.root / 'apps/example/prod').exists())
        self.assertEqual(promotion.fingerprint(self.root, 'example', 'prod'), before)
        self.assertFalse((self.root / 'clusters/home/app-example-prod.yaml').exists())

    def test_secret_stub_is_new_and_failure_leaves_no_target(self):
        from unittest import mock

        from swhurl.apps import policy
        from swhurl.run import FakeRunner, Result
        source = self.staging('worker')
        instance = self.root / 'apps/example/staging'
        doc = promotion.YamlFile(instance / 'helmrelease.yaml')
        doc.data['spec']['values']['controllers']['main']['containers']['main']['envFrom'] = [
            {'secretRef': {'name': 'example-secret'}}]
        doc.save()
        source = doc.data
        (instance / 'secret.sops.yaml').write_text('stringData:\n  API_TOKEN: ENC[staging-value]\nsops: {}\n')
        kustomization = promotion.YamlFile(instance / 'kustomization.yaml')
        kustomization.data['resources'].append('secret.sops.yaml')
        kustomization.save()
        runner = FakeRunner().on('sops', returncode=1, stderr='encryption failed')
        with self.assertRaisesRegex(EditError, 'SOPS-encrypt'):
            promotion.create(self.root, 'example', source, runner=runner)
        self.assertFalse((self.root / 'apps/example/prod').exists())

        def encrypt(args, _):
            path = Path(args[-1])
            self.assertNotIn('staging-value', path.read_text())
            self.assertIn('REPLACE_ME', path.read_text())
            path.write_text(path.read_text().replace('REPLACE_ME', 'ENC[new-placeholder]') + 'sops: {}\n')
            return Result(args)

        runner = FakeRunner().on('sops', handler=encrypt)
        with mock.patch.object(policy, 'evaluate', return_value=[]):
            dst = promotion.create(self.root, 'example', source, runner=runner)
        secret = (dst / 'secret.sops.yaml').read_text()
        self.assertNotIn('staging-value', secret)
        self.assertIn('namespace: example-prod', secret)
        self.assertIn('example-prod', (self.root / 'platform/reloader/helmrelease.yaml').read_text())

    def test_public_host_and_staging_reference_refusals(self):
        source = self.staging('web', '--exposure', 'public', '--host', 'staging.example.com')
        with self.assertRaisesRegex(EditError, 'distinct production'):
            promotion.first_args(self.root, 'example', source)
        self.assertEqual(promotion.first_args(self.root, 'example', source, host='example.com').host, 'example.com')
