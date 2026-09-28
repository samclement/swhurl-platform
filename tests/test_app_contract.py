"""App contract: generator guards, fixture drift, and rendered-resource policy."""
import copy
import importlib.util
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[1]
FIXTURES = ROOT / 'tests/fixtures/apps'
WEB = ['--env', 'staging', '--exposure', 'authenticated-web', '--host', 'x.homelab.swhurl.com',
       '--image', 'repo/app:1.0', '--health-path', '/healthz']


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


app_new = load('app_new', ROOT / 'scripts/app-new.py')
app_policy = load('app_policy', ROOT / 'scripts/app_policy.py')


class GeneratorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        (self.tmp / 'clusters/home').mkdir(parents=True)
        (self.tmp / 'clusters/home/kustomization.yaml').write_text('resources:\n  - tenants.yaml\n')

    def gen(self, *args):
        return app_new.main(['--root', str(self.tmp), *args])

    def test_refuses_unsafe_or_ambiguous_instances(self):
        cases = {
            'worker with a route': ['w', '--env', 'staging', '--kind', 'worker', '--exposure', 'public',
                                    '--host', 'w.example.com', '--image', 'r/w:1'],
            'public inside cookie domain': ['p', '--env', 'staging', '--exposure', 'public',
                                            '--host', 'p.homelab.swhurl.com', '--image', 'r/p:1', '--health-path', '/'],
            'authenticated outside cookie domain': ['a', '--env', 'staging', '--exposure', 'authenticated-web',
                                                    '--host', 'a.example.com', '--image', 'r/a:1', '--health-path', '/'],
            'prod without digest': ['d', '--env', 'prod', '--kind', 'worker', '--image', 'r/d:1'],
            'latest tag': ['l', '--env', 'staging', '--kind', 'worker', '--image', 'r/l:latest'],
            'untagged image': ['u', '--env', 'staging', '--kind', 'worker', '--image', 'r/u'],
            'web without health path': ['h', '--env', 'staging', '--image', 'r/h:1'],
            'invalid name': ['Bad_Name', '--env', 'staging', '--kind', 'worker', '--image', 'r/b:1'],
        }
        for label, args in cases.items():
            with self.subTest(label):
                self.assertEqual(self.gen(*args), 2)
        self.assertFalse((self.tmp / 'tenants').exists(), 'a refused instance wrote files')

    def test_writes_registers_and_refuses_overwrite(self):
        self.assertEqual(self.gen('x', *WEB), 0)
        instance = self.tmp / 'tenants/apps/x/staging'
        self.assertEqual(sorted(p.name for p in instance.iterdir()),
                         ['helmrelease.yaml', 'kustomization.yaml', 'namespace.yaml'])
        unit = yaml.safe_load((self.tmp / 'clusters/home/app-x-staging.yaml').read_text())
        self.assertEqual(unit['metadata']['name'], 'homelab-app-x-staging')
        self.assertEqual({d['name'] for d in unit['spec']['dependsOn']}, {'homelab-cluster-base', 'homelab-auth'})
        self.assertNotIn('deletionPolicy', unit['spec'], 'app units must keep MirrorPrune')
        self.assertIn('- app-x-staging.yaml', (self.tmp / 'clusters/home/kustomization.yaml').read_text())
        self.assertEqual(self.gen('x', *WEB), 2, 'overwrite must be refused')

    def test_secret_stub_is_encrypted_and_namespace_watched(self):
        fake = self.tmp / 'bin'
        fake.mkdir()
        (fake / 'sops').write_text('#!/bin/sh\nfor f; do :; done\nprintf "sops:\\n  fake: true\\n" >> "$f"\n')
        (fake / 'sops').chmod(0o755)
        reloader = self.tmp / 'platform-services/reloader/base/helmrelease-reloader.yaml'
        reloader.parent.mkdir(parents=True)
        reloader.write_text('    reloader:\n      namespaces: [ingress, logging]\n')
        old_path = os.environ['PATH']
        os.environ['PATH'] = f'{fake}:{old_path}'
        try:
            self.assertEqual(self.gen('s', *WEB, '--secret-keys', 'A,B'), 0)
        finally:
            os.environ['PATH'] = old_path
        secret = (self.tmp / 'tenants/apps/s/staging/secret.sops.yaml').read_text()
        self.assertIn('sops:', secret)
        unit = yaml.safe_load((self.tmp / 'clusters/home/app-s-staging.yaml').read_text())
        self.assertEqual(unit['spec']['decryption']['secretRef']['name'], 'sops-age')
        self.assertIn('namespaces: [ingress, logging, s-staging]', reloader.read_text())

    def test_missing_sops_leaves_no_plaintext(self):
        old_path = os.environ['PATH']
        os.environ['PATH'] = str(self.tmp)  # no sops (and nothing else) on PATH
        try:
            self.assertEqual(self.gen('n', *WEB, '--secret-keys', 'A'), 2)
        finally:
            os.environ['PATH'] = old_path
        self.assertFalse(list(self.tmp.rglob('*.sops.yaml')), 'plaintext Secret stub left behind')
        self.assertFalse((self.tmp / 'tenants/apps/n').exists() and any((self.tmp / 'tenants/apps/n').rglob('*.yaml')))

    def test_committed_fixtures_match_generator(self):
        with tempfile.TemporaryDirectory(dir=FIXTURES.parent) as tmp:
            fake = Path(tmp) / 'bin'
            fake.mkdir()
            (fake / 'sops').write_text('#!/bin/sh\nfor f; do :; done\nprintf "sops: {}\\n" >> "$f"\n')
            (fake / 'sops').chmod(0o755)
            env = dict(os.environ, FIXTURE_ROOT=str(Path(tmp) / 'out'), PATH=f'{fake}:{os.environ["PATH"]}')
            subprocess.run([str(FIXTURES.parent / 'apps.sh')], env=env, check=True, capture_output=True)
            out = Path(tmp) / 'out'
            generated = {p.relative_to(out) for p in out.rglob('*.yaml')}
            committed = {p.relative_to(FIXTURES) for p in FIXTURES.rglob('*.yaml')}
            self.assertEqual(generated, committed, 'fixture file set drifted; run tests/fixtures/apps.sh')
            for rel in generated:
                if rel.name.endswith('.sops.yaml'):
                    self.assertIn('sops:', (FIXTURES / rel).read_text(), f'{rel} is not encrypted')
                    continue
                with self.subTest(file=str(rel)):
                    self.assertEqual((out / rel).read_text(), (FIXTURES / rel).read_text(),
                                     'fixture drifted from generator; run tests/fixtures/apps.sh')


@unittest.skipUnless(shutil.which('helm') or os.environ.get('REQUIRE_HELM'), 'helm not installed')
class PolicyTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.rendered = {p.parent.name + '/' + p.name: app_policy.render(p) for p in app_policy.instances()
                        if p.is_relative_to(FIXTURES)}

    def docs(self, key):
        return copy.deepcopy(self.rendered[key])

    def rules(self, docs):
        return {rule for rule, _ in app_policy.check(docs)}

    def find(self, docs, kind):
        return next(d for d in docs if d['kind'] == kind)

    def container(self, docs):
        return app_policy.pod_spec(self.find(docs, 'Deployment'))['containers'][0]

    def test_fixtures_pass(self):
        self.assertEqual(set(self.rendered), {'smoke-web/staging', 'smoke-worker/staging', 'smoke-data/prod'})
        for key, docs in self.rendered.items():
            with self.subTest(instance=key):
                self.assertEqual(app_policy.check(docs), [])

    def test_worker_has_no_route_and_web_has_sign_in(self):
        worker = self.rendered['smoke-worker/staging']
        self.assertFalse([d for d in worker if d['kind'] in ('Ingress', 'Service')])
        ingress = self.find(self.rendered['smoke-web/staging'], 'Ingress')
        self.assertIn(app_policy.AUTH_MIDDLEWARE,
                      ingress['metadata']['annotations']['traefik.ingress.kubernetes.io/router.middlewares'])

    def test_violations_are_caught(self):
        def web_without_middleware(d):
            del self.find(d, 'Ingress')['metadata']['annotations']['traefik.ingress.kubernetes.io/router.middlewares']
        def public_on_cookie_domain(d):
            self.find(d, 'Namespace')['metadata']['labels']['platform.swhurl.com/exposure'] = 'public'
        def private_with_ingress(d):
            self.find(d, 'Namespace')['metadata']['labels']['platform.swhurl.com/exposure'] = 'private'
        def prod_without_digest(d):
            c = self.container(d)
            c['image'] = c['image'].split('@')[0]
        def privileged(d):
            self.container(d)['securityContext']['privileged'] = True
        def root_user(d):
            app_policy.pod_spec(self.find(d, 'Deployment'))['securityContext'].pop('runAsNonRoot')
        def token(d):
            app_policy.pod_spec(self.find(d, 'Deployment'))['automountServiceAccountToken'] = True
        def host_path(d):
            app_policy.pod_spec(self.find(d, 'Deployment'))['volumes'].append({'name': 'h', 'hostPath': {'path': '/'}})
        def no_limits(d):
            self.container(d)['resources'].pop('limits')
        def latest(d):
            self.container(d)['image'] = 'docker.io/library/busybox:latest'
        def storage(d):
            self.find(d, 'PersistentVolumeClaim')['spec']['storageClassName'] = 'fast'
        def no_tls(d):
            self.find(d, 'Ingress')['spec']['tls'] = []
        cases = [
            ('smoke-web/staging', web_without_middleware, 'exposure'),
            ('smoke-web/staging', public_on_cookie_domain, 'exposure'),
            ('smoke-web/staging', private_with_ingress, 'exposure'),
            ('smoke-web/staging', no_tls, 'ingress-tls'),
            ('smoke-data/prod', prod_without_digest, 'prod-digest'),
            ('smoke-worker/staging', privileged, 'no-escalation'),
            ('smoke-worker/staging', root_user, 'non-root'),
            ('smoke-worker/staging', token, 'no-sa-token'),
            ('smoke-worker/staging', host_path, 'no-host-access'),
            ('smoke-worker/staging', no_limits, 'resources'),
            ('smoke-worker/staging', latest, 'image-pinned'),
            ('smoke-data/prod', storage, 'storage-class'),
        ]
        for key, mutate, rule in cases:
            with self.subTest(case=mutate.__name__):
                docs = self.docs(key)
                mutate(docs)
                self.assertIn(rule, self.rules(docs))

    def test_exceptions_need_a_reason(self):
        docs = self.docs('smoke-worker/staging')
        self.container(docs)['securityContext']['privileged'] = True
        release = self.find(docs, 'HelmRelease')
        release['metadata']['annotations'] = {app_policy.EXCEPTIONS_KEY: 'no-escalation=needs raw sockets'}
        allowed, problems = app_policy.exceptions(docs)
        self.assertEqual(problems, [])
        self.assertIn('no-escalation', allowed)
        release['metadata']['annotations'] = {app_policy.EXCEPTIONS_KEY: 'no-escalation'}
        _, problems = app_policy.exceptions(docs)
        self.assertTrue(problems)


if __name__ == '__main__':
    unittest.main()
