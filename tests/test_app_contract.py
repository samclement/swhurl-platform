"""App contract: generator guards, fixture drift, and rendered-resource policy."""
import argparse
import copy
import io
import os
import shutil
import subprocess
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import yaml

from swhurl import ROOT
from swhurl.apps import calling, contract, declaration, edit
from swhurl.apps import new as app_new
from swhurl.apps import policy as app_policy
from swhurl.apps.model import generated
from swhurl.run import CommandError, FakeRunner, Result

FIXTURES = ROOT / 'tests/fixtures/apps'
MANIFESTS = ROOT / 'tests/fixtures/manifests'
TEMPLATE_IMAGE = 'ghcr.io/samclement/w:12-abcdef0@sha256:' + 'b' * 64
WEB = ['--env', 'staging', '--exposure', 'authenticated-web', '--host', 'x.homelab.swhurl.com',
       '--image', 'repo/app:1.0', '--health-path', '/healthz']


class GeneratorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        (self.tmp / 'clusters/home').mkdir(parents=True)
        (self.tmp / 'clusters/home/kustomization.yaml').write_text('resources:\n  - tenants.yaml\n')

    def gen(self, *args):
        return app_new.main(['--root', str(self.tmp), '--no-policy-check', '--team', 'demo', *args])

    def test_requested_validation_cannot_silently_skip_missing_or_failed_tools(self):
        for error in (CommandError(['helm'], 'helm not found'),
                      CommandError(['helm', 'template'], 'render failed', 1)):
            with self.subTest(error=error), mock.patch.object(app_policy, 'evaluate', side_effect=error):
                out, err = io.StringIO(), io.StringIO()
                with redirect_stdout(out), redirect_stderr(err):
                    result = app_new.main(['worker', '--env=staging', '--kind=worker', '--image=r/w:1', '--team=demo',
                                           '--root', str(self.tmp)])
                self.assertEqual(result, 1)
                self.assertIn('could not validate', err.getvalue())
                self.assertNotIn('Next: commit', out.getvalue())
                self.assertTrue((self.tmp / 'apps/worker/staging/helmrelease.yaml').exists())
                shutil.rmtree(self.tmp / 'apps')
                (self.tmp / 'clusters/home/app-worker-staging.yaml').unlink()

    def test_policy_opt_out_is_explicit_and_does_not_call_validation(self):
        with mock.patch.object(app_policy, 'evaluate') as evaluate:
            with redirect_stdout(io.StringIO()):
                self.assertEqual(self.gen('worker', '--env=staging', '--kind=worker', '--image=r/w:1'), 0)
        evaluate.assert_not_called()

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
        self.assertFalse((self.tmp / 'apps').exists(), 'a refused instance wrote files')

    def test_sqlite_capability_wires_volume_path_and_a_single_writer(self):
        self.assertEqual(self.gen('notes', '--env', 'staging', '--health-path', '/healthz', '--image', 'r/notes:1', '--database', 'sqlite'), 0)
        values = yaml.safe_load((self.tmp / 'apps/notes/staging/helmrelease.yaml').read_text())['spec']['values']
        main = values['controllers']['main']
        self.assertEqual((main['replicas'], main['strategy']), (1, 'Recreate'))
        self.assertEqual(main['containers']['main']['env']['DATABASE_PATH'], '/data/app.db')
        data = values['persistence']['data']
        self.assertEqual((data['size'], data['storageClass'], data['globalMounts']), ('1Gi', 'local-path-retain', [{'path': '/data'}]))
        namespace = yaml.safe_load((self.tmp / 'apps/notes/staging/namespace.yaml').read_text())
        self.assertEqual(namespace['metadata']['annotations'], {'kustomize.toolkit.fluxcd.io/prune': 'disabled'}, 'data outlives the app')
        self.assertEqual(self.gen('sized', '--env', 'staging', '--health-path', '/healthz', '--image', 'r/s:1', '--database', 'sqlite',
                                  '--database-size', '5Gi'), 0)
        sized = yaml.safe_load((self.tmp / 'apps/sized/staging/helmrelease.yaml').read_text())['spec']['values']
        self.assertEqual(sized['persistence']['data']['size'], '5Gi')
        for kind in ('worker', 'job'):
            with self.subTest(kind=kind):
                self.assertNotEqual(self.gen(f'{kind}-db', '--env', 'staging', '--kind', kind, '--image', 'r/w:1',
                                             '--database', 'sqlite'), 0, 'only a web app keeps a database')

    def test_writes_registers_and_refuses_overwrite(self):
        self.assertEqual(self.gen('x', *WEB), 0)
        instance = self.tmp / 'apps/x/staging'
        self.assertEqual(sorted(p.name for p in instance.iterdir()),
                         ['helmrelease.yaml', 'instance.yaml', 'kustomization.yaml', 'namespace.yaml', 'networkpolicy.yaml',
                          'release.yaml', 'resourcequota.yaml'])
        unit = yaml.safe_load((self.tmp / 'clusters/home/app-x-staging.yaml').read_text())
        self.assertEqual(unit['metadata']['name'], 'app-x-staging')
        self.assertEqual({d['name'] for d in unit['spec']['dependsOn']},
                         {'infra-base', 'platform-oauth2-proxy'})
        self.assertNotIn('deletionPolicy', unit['spec'], 'app units must keep MirrorPrune')
        self.assertIn('- app-x-staging.yaml', (self.tmp / 'clusters/home/kustomization.yaml').read_text())
        self.assertEqual(self.gen('x', *WEB), 2, 'overwrite must be refused')

    def test_every_instance_gets_a_team_a_quota_and_a_network_policy(self):
        (self.tmp / 'apps').mkdir()
        (self.tmp / 'apps/teams.yaml').write_text('teams:\n  demo: {}\n  payments: {description: Orders}\n')
        self.assertEqual(self.gen('web', '--image', 'r/web:1', '--health-path', '/h', '--port', '9000',
                                  '--exposure', 'authenticated-web', '--team', 'payments'), 0)
        self.assertEqual(self.gen('job', '--image', 'r/job:1', '--kind', 'worker'), 0)

        def load(app, name):
            return yaml.safe_load((self.tmp / 'apps' / app / 'staging' / name).read_text())
        self.assertEqual(load('web', 'namespace.yaml')['metadata']['labels'][contract.TEAM], 'payments')
        self.assertEqual(load('job', 'namespace.yaml')['metadata']['labels'][contract.TEAM], 'demo')
        for app in ('web', 'job'):
            self.assertEqual(load(app, 'resourcequota.yaml')['spec']['hard'], contract.QUOTA)
            self.assertLessEqual({'resourcequota.yaml', 'networkpolicy.yaml'}, set(load(app, 'kustomization.yaml')['resources']))
            policy = load(app, 'networkpolicy.yaml')
            self.assertEqual((policy['metadata']['namespace'], policy['spec']['podSelector']),
                             (f'{app}-staging', {'matchLabels': {'app.kubernetes.io/instance': app}}))
        self.assertEqual(load('web', 'networkpolicy.yaml')['spec']['ingress'],
                         [{'from': [contract.TRAEFIK], 'ports': [{'port': 9000, 'protocol': 'TCP'}]}])
        self.assertEqual(load('job', 'networkpolicy.yaml')['spec']['ingress'], [], 'no route: nobody is admitted')
        # A web app without a route is closed too: Traefik has nothing to send it.
        self.assertEqual(self.gen('quiet', '--image', 'r/q:1', '--health-path', '/h'), 0)
        self.assertEqual(load('quiet', 'networkpolicy.yaml')['spec']['ingress'], [])

    def test_every_instance_is_told_who_it_is_and_a_web_app_gets_the_issuer_and_keys(self):
        (self.tmp / 'apps').mkdir()
        (self.tmp / 'apps/signing-keys.json').write_text('{"keys": [{"kid": "k1"}]}')
        manifest = self.tmp / 'swhurl.yaml'  # where it serves its API description comes only from provides.api
        manifest.write_text('version: 1\nkind: web\nhealthPath: /h\ntelemetry: otlp\nprovides: {api: /openapi.json}\n')
        self.assertEqual(self.gen('web', '--image', 'r/web:1', '--manifest', str(manifest), '--no-auto-deploy'), 0)
        self.assertEqual(self.gen('job', '--image', 'r/job:1', '--kind', 'worker'), 0)

        def env(app):
            release = yaml.safe_load((self.tmp / f'apps/{app}/staging/helmrelease.yaml').read_text())
            return release['spec']['values']['controllers']['main']['containers']['main']['env']
        self.assertEqual(list(env('web'))[:5], ['SWHURL_APP', 'SWHURL_ENV', 'SWHURL_NOTIFY_URL', 'SWHURL_AUTH_ISSUER', 'SWHURL_AUTH_KEYS'],
                         'first, so $(SWHURL_ENV) expands in what follows')
        self.assertEqual((env('web')['SWHURL_APP'], env('web')['SWHURL_ENV'], env('web')['SWHURL_AUTH_KEYS']),
                         ('web', 'staging', '{"keys":[{"kid":"k1"}]}'))
        self.assertNotIn('SWHURL_API_PATH', env('web'), 'no app reads it')
        web = yaml.safe_load((self.tmp / 'apps/web/staging/helmrelease.yaml').read_text())
        self.assertEqual(web['metadata']['annotations']['platform.swhurl.com/api-description'], '/openapi.json')
        self.assertEqual(env('job'), {'SWHURL_APP': 'job', 'SWHURL_ENV': 'staging', 'SWHURL_NOTIFY_URL': 'http://notify.notify.svc.cluster.local:8080'},
                         'a worker receives no calls')

    def test_calls_at_creation_reach_both_sides_or_nothing_is_left(self):
        (self.tmp / 'platform/reloader').mkdir(parents=True)
        self.assertEqual(self.gen('orders', '--image', 'r/orders:1', '--health-path', '/h', '--port', '9000'), 0)
        with redirect_stdout(io.StringIO()):
            edit.set_allows(self.tmp, 'orders', 'staging', add=['team:demo'])
        self.assertEqual(self.gen('reports', '--image', 'r/reports:1', '--health-path', '/h', '--calls', 'orders'), 0)

        def values(app):
            return yaml.safe_load((self.tmp / f'apps/{app}/staging/helmrelease.yaml').read_text())['spec']['values']
        caller = values('reports')
        self.assertEqual(caller['controllers']['main']['containers']['main']['env']['SWHURL_SERVICE_ORDERS_URL'],
                         'http://orders.orders-$(SWHURL_ENV).svc.cluster.local:9000')
        self.assertEqual(caller['persistence']['tokens'], calling.tokens_volume(['orders']))
        self.assertEqual(values('orders')['controllers']['main']['containers']['main']['env']['SWHURL_ALLOWED_CALLERS'],
                         'reports')
        err = io.StringIO()
        with redirect_stderr(err), redirect_stdout(io.StringIO()):
            self.assertEqual(self.gen('lost', '--image', 'r/lost:1', '--kind', 'worker', '--calls', 'nowhere'), 2)
        self.assertIn('lost/staging calls nowhere, which has no staging instance', err.getvalue())
        self.assertFalse((self.tmp / 'apps/lost').exists())
        self.assertFalse((self.tmp / 'clusters/home/app-lost-staging.yaml').exists())

    def test_an_unregistered_team_is_refused_before_writing(self):
        (self.tmp / 'apps').mkdir()
        (self.tmp / 'apps/teams.yaml').write_text('teams:\n  platform: {}\n')
        err = io.StringIO()
        with redirect_stderr(err):
            self.assertEqual(self.gen('web', '--image', 'r/web:1', '--health-path', '/h', '--team', 'nobody'), 2)
        self.assertIn("unknown team 'nobody'; registered in apps/teams.yaml: platform", err.getvalue())
        with redirect_stderr(err):
            self.assertEqual(app_new.main(['web', '--image', 'r/web:1', '--health-path', '/h', '--root', str(self.tmp),
                                           '--no-policy-check']), 2, 'there is no default team')
        self.assertIn('every app belongs to a team: give --team NAME (or team: in swhurl.yaml); registered in apps/teams.yaml: platform',
                      err.getvalue())
        self.assertFalse((self.tmp / 'apps/web').exists())
        self.assertFalse((self.tmp / 'apps/web').exists())

    def test_an_apps_own_alert_settings_go_on_its_namespace_and_bad_ones_are_refused(self):
        self.assertEqual(self.gen('web', '--image', 'r/web:1', '--health-path', '/h', '--alerts', 'error-logs=20,errors=none'), 0)
        namespace = yaml.safe_load((self.tmp / 'apps/web/staging/namespace.yaml').read_text())
        self.assertEqual(namespace['metadata']['annotations'], {'platform.swhurl.com/alerts': 'error-logs=20,errors=none'})
        err = io.StringIO()
        with redirect_stderr(err):
            self.assertEqual(self.gen('api', '--image', 'r/api:1', '--health-path', '/h', '--alerts', 'latency=5'), 2)
        self.assertIn("--alerts: 'latency' is not an alert: choose from error-logs, errors", err.getvalue())
        self.assertFalse((self.tmp / 'apps/api').exists())
        base = {'version': 1, 'kind': 'worker'}
        with self.assertRaisesRegex(declaration.DeclarationError, r'alerts: a grant, set on the platform, not in this file \(make app-alerts'):
            declaration.of(base | {'alerts': 'none'})
        self.assertNotIn('alerts', declaration.of(base).seed)

    def test_secret_names_without_values_and_namespace_watched(self):
        reloader = self.tmp / 'platform/reloader/helmrelease.yaml'
        reloader.parent.mkdir(parents=True)
        reloader.write_text('spec:\n  values:\n    reloader:\n      namespaces: [ingress, logging]\n')
        self.assertEqual(self.gen('s', *WEB, '--secret-keys', 'A,B'), 0)
        self.assertFalse((self.tmp / 'apps/s/staging/secret.sops.yaml').exists(), 'values are set later, with make app-secret')
        release = yaml.safe_load((self.tmp / 'apps/s/staging/helmrelease.yaml').read_text())
        self.assertEqual(release['metadata']['annotations']['platform.swhurl.com/secrets'], 'A,B')
        self.assertRegex(release['metadata']['annotations']['platform.swhurl.com/render'], r'^[0-9a-f]{64}$')
        self.assertEqual(release['spec']['values']['controllers']['main']['containers']['main']['envFrom'],
                         [{'secretRef': {'name': 's-secret', 'optional': True}}], 'the app starts before its first value')
        unit = yaml.safe_load((self.tmp / 'clusters/home/app-s-staging.yaml').read_text())
        self.assertNotIn('decryption', unit['spec'], 'nothing to decrypt yet')
        self.assertIn('namespaces: [ingress, logging, s-staging]', reloader.read_text())

    def test_otlp_points_the_sdk_at_the_node_collector(self):
        self.assertEqual(self.gen('o', *WEB, '--otlp'), 0)
        self.assertEqual(self.gen('p', *WEB), 0)

        def env(app):
            release = yaml.safe_load((self.tmp / f'apps/{app}/staging/helmrelease.yaml').read_text())
            found = release['spec']['values']['controllers']['main']['containers']['main'].get('env')
            return {key: value for key, value in found.items() if not key.startswith('SWHURL_')}
        self.assertEqual(env('o'), {
            'HOST_IP': {'valueFrom': {'fieldRef': {'fieldPath': 'status.hostIP'}}},
            'OTEL_EXPORTER_OTLP_ENDPOINT': 'http://$(HOST_IP):4318',
            'OTEL_EXPORTER_OTLP_PROTOCOL': 'http/protobuf',
            'OTEL_SERVICE_NAME': 'o',
        })
        self.assertEqual(env('p'), {}, 'without --otlp nothing is written')

    def test_a_template_manifest_fills_its_conventions_and_derives_the_host(self):
        self.assertEqual(app_new.main(['w', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'staging', '--image', TEMPLATE_IMAGE,
                                       '--root', str(self.tmp), '--no-policy-check']), 0)
        values = yaml.safe_load((self.tmp / 'apps/w/staging/helmrelease.yaml').read_text())['spec']['values']
        main = values['controllers']['main']['containers']['main']
        self.assertEqual(main['probes']['readiness']['spec']['httpGet'], {'path': '/healthz', 'port': 8080})
        self.assertEqual(values['defaultPodOptions']['securityContext']['runAsUser'], 65532)
        self.assertEqual(main['env']['OTEL_SERVICE_NAME'], 'w')
        self.assertEqual(values['ingress']['main']['hosts'][0]['host'], 'staging-w.homelab.swhurl.com')
        self.assertIn('middlewares', str(values['ingress']['main']['annotations']))

    def test_auto_deploy_watches_the_image_in_staging_only(self):
        self.assertEqual(app_new.main(['w', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'staging', '--image', TEMPLATE_IMAGE,
                                       '--root', str(self.tmp), '--no-policy-check']), 0)
        with redirect_stdout(io.StringIO()):
            edit.promote(self.tmp, 'w')
        staging, prod = self.tmp / 'apps/w/staging', self.tmp / 'apps/w/prod'
        repository, image_policy = yaml.safe_load_all((staging / contract.IMAGE_AUTOMATION_FILE).read_text())
        self.assertEqual((repository['kind'], repository['metadata']['namespace'], repository['spec']['image']),
                         ('ImageRepository', 'flux-system', 'ghcr.io/samclement/w'))
        self.assertEqual(image_policy['metadata']['name'], 'w-staging')
        self.assertEqual(image_policy['spec']['digestReflectionPolicy'], 'Always')
        self.assertIn('interval', image_policy['spec'], 'Flux rejects Always without an interval')
        self.assertEqual(image_policy['spec']['filterTags']['extract'], '$run')
        unit = yaml.safe_load((self.tmp / 'clusters/home/app-w-staging.yaml').read_text())
        self.assertIn({'name': 'platform-image-automation'}, unit['spec']['dependsOn'])
        self.assertIn('tag: 12-abcdef0\n', (staging / 'helmrelease.yaml').read_text(), 'the Deploy workflow writes it: no markers')
        self.assertIn(contract.IMAGE_AUTOMATION_FILE, (staging / 'kustomization.yaml').read_text())
        self.assertFalse((prod / contract.IMAGE_AUTOMATION_FILE).exists())
        self.assertEqual(generated.problems(self.tmp), [], 'each environment renders its own automation')

    def test_auto_deploy_needs_a_template_tag_and_digest(self):
        for image in ('ghcr.io/samclement/w:1.0@sha256:' + 'a' * 64, 'ghcr.io/samclement/w:12-abcdef0'):
            with self.subTest(image=image), redirect_stderr(io.StringIO()) as err:
                self.assertEqual(app_new.main(['w', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'staging', '--image', image,
                                               '--root', str(self.tmp), '--no-policy-check']), 2)
            self.assertIn('automatic deploys need', err.getvalue())
        self.assertEqual(app_new.main(['w', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'staging', '--no-auto-deploy',
                                       '--image', 'ghcr.io/samclement/w:1.0', '--root', str(self.tmp),
                                       '--no-policy-check']), 0)

    def test_in_the_platform_repository_only_a_platform_created_app_is_generated(self):
        def problem(*argv):
            return app_new.platform_origin_problem(app_new.parse_args(
                ['w', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'staging', *argv]))
        ours = argparse.Namespace(name='w', env='staging', from_repo='samclement/w', image=TEMPLATE_IMAGE,
                                  auto_deploy=True)
        self.assertIsNone(app_new.platform_origin_problem(ours))
        for change, expected in (({'from_repo': None}, '--from-repo samclement/w is required'),
                                 ({'from_repo': 'someone/w'}, '--from-repo samclement/w is required'),
                                 ({'from_repo': 'samclement/other'}, '--from-repo samclement/w is required'),
                                 ({'image': 'docker.io/library/nginx:1.27'}, 'the image must be ghcr.io/samclement/w'),
                                 ({'image': TEMPLATE_IMAGE.replace('/w:', '/other:')}, 'the image must be ghcr.io/samclement/w'),
                                 ({'auto_deploy': False}, 'must deploy to staging automatically')):
            with self.subTest(change=change):
                self.assertIn(expected, app_new.platform_origin_problem(argparse.Namespace(**{**vars(ours), **change})))
        self.assertIn('make app-repo NAME=w', problem('--image', TEMPLATE_IMAGE), 'a local manifest is not a repository')
        # main applies it to the platform's own tree only: fixtures and tests write to another --root
        with mock.patch.object(app_new, 'ROOT', self.tmp.resolve()), redirect_stderr(io.StringIO()) as err:
            self.assertEqual(self.gen('w', '--env', 'staging', '--image', 'r/w:1', '--kind', 'worker'), 2)
        self.assertIn('--from-repo samclement/w is required', err.getvalue())
        self.assertFalse((self.tmp / 'apps').exists())
        self.assertEqual(self.gen('w', '--env', 'staging', '--image', 'r/w:1', '--kind', 'worker'), 0)

    def test_explicit_flags_beat_the_manifest(self):
        args = app_new.parse_args(['w', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'prod', '--image', 'r/w:1@sha256:' + 'a' * 64,
                                   '--no-otlp', '--port', '3000', '--exposure', 'private'])
        self.assertEqual((args.otlp, args.port, args.exposure, args.health_path), (False, 3000, 'private', '/healthz'))
        worker = app_new.parse_args(['q', '--manifest', str(MANIFESTS / 'worker.yaml'), '--team', 'demo', '--env', 'staging', '--image', 'r/q:1'])
        self.assertEqual((worker.kind, worker.exposure, worker.otlp), ('worker', 'private', True))

    def test_from_repo_records_the_needs_at_the_images_commit_not_the_default_branch(self):
        web = (MANIFESTS / 'web.yaml').read_text()
        files = {'': web.replace('port: 8080', 'port: 3000'),  # the default branch has moved on
                 'abcdef0': web.replace('port: 8080', 'port: 9000').replace('uid: 65532', 'uid: 1000')}
        asked = []

        def github(request):
            ref = request.full_url.partition('?ref=')[2]
            asked.append(ref)
            return files[ref].encode()
        with redirect_stdout(io.StringIO()):
            self.assertEqual(app_new.main(['w', '--from-repo', 'samclement/w', '--env', 'staging', '--image', TEMPLATE_IMAGE,
                                           '--root', str(self.tmp), '--no-policy-check', '--team', 'demo'], github), 0)
        self.assertEqual(asked, ['abcdef0'], 'one read, at the commit the tag names')
        release = yaml.safe_load((self.tmp / 'apps/w/staging/release.yaml').read_text())
        text = yaml.safe_dump(release)
        self.assertIn('9000', text)
        self.assertIn('1000', text)
        self.assertNotIn('3000', text)

    def test_signed_in_host_is_derived_per_environment(self):
        self.assertEqual(contract.default_host('w', 'prod'), 'w.homelab.swhurl.com')
        self.assertEqual(contract.default_host('w', 'staging'), 'staging-w.homelab.swhurl.com')

    def test_secret_keys_the_platform_sets_are_refused_before_writing(self):
        for key in ('HOST_IP', 'SWHURL_X', 'KUBERNETES_SERVICE_HOST', 'api-token'):
            with self.subTest(key):
                self.assertEqual(self.gen('n', *WEB, '--secret-keys', f'API_TOKEN,{key}'), 2)
                self.assertFalse((self.tmp / 'apps/n').exists())
        self.assertEqual(contract.secret_key_problem(['API_TOKEN', 'OTELX', 'PORT']), '')

    def test_secret_names_need_no_sops(self):
        old_path = os.environ['PATH']
        os.environ['PATH'] = str(self.tmp)  # no sops (and nothing else) on PATH
        try:
            self.assertEqual(self.gen('n', *WEB, '--secret-keys', 'A'), 0)
        finally:
            os.environ['PATH'] = old_path
        self.assertFalse(list(self.tmp.rglob('*.sops.yaml')), 'no Secret file until a value is set')

    def test_committed_fixtures_match_generator(self):
        with tempfile.TemporaryDirectory(dir=FIXTURES.parent) as tmp:
            fake = Path(tmp) / 'bin'
            fake.mkdir()
            (fake / 'sops').write_text("#!/bin/sh\nsed 's/REPLACE_ME/ENC[placeholder]/g'\nprintf 'sops: {}\\n'\n")
            (fake / 'sops').chmod(0o755)
            env = dict(os.environ, FIXTURE_ROOT=str(Path(tmp) / 'out'), PATH=f'{fake}:{os.environ["PATH"]}')
            result = subprocess.run([str(FIXTURES.parent / 'apps.sh')], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
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
class ChartCacheTests(unittest.TestCase):
    def test_parallel_pull_of_the_same_chart_is_not_an_error(self):
        cache = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, cache)

        def pull(argv, _input):
            # This pull untars its chart, and another process finishes its own pull first.
            for chart in (Path(argv[argv.index('--untardir') + 1]) / 'c', cache / 'c-1.0.0'):
                chart.mkdir(parents=True)
                (chart / 'Chart.yaml').write_text('name: c\n')
            return Result(argv)

        with mock.patch.object(app_policy, 'CACHE', cache):
            path = app_policy.chart_dir('c', '1.0.0', 'https://charts', FakeRunner().on('helm', 'pull', handler=pull))
        self.assertEqual(path, cache / 'c-1.0.0')
        self.assertEqual([p.name for p in cache.iterdir()], ['c-1.0.0'], 'temporary pull directory left behind')


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

    def test_multiple_violations_preserve_messages_and_traversal_order(self):
        secure = {'runAsNonRoot': True, 'allowPrivilegeEscalation': False, 'capabilities': {'drop': ['ALL']}}
        resources = {'requests': {'cpu': '10m', 'memory': '16Mi'}, 'limits': {'memory': '32Mi'}}
        docs = [
            {'kind': 'Namespace', 'metadata': {'labels': {
                app_policy.ENVIRONMENT: 'prod', app_policy.EXPOSURE: 'public', app_policy.TEAM: 'demo'}}},
            {'kind': 'Deployment', 'metadata': {'name': 'example'}, 'spec': {
                'replicas': 2, 'template': {'spec': {
                    'hostPID': True, 'securityContext': {'runAsNonRoot': True},
                    'volumes': [{'persistentVolumeClaim': {'claimName': 'data'}}],
                    'initContainers': [{'name': 'setup', 'image': 'r/setup:1',
                                        'securityContext': secure, 'resources': resources}],
                    'containers': [{'name': 'main', 'image': 'r/main:latest',
                                    'securityContext': {'runAsNonRoot': False},
                                    'env': [{'name': 'OTEL_EXPORTER_OTLP_ENDPOINT', 'value': 'http://$(HOST_IP):4318'},
                                            {'name': 'HOST_IP', 'valueFrom': {'fieldRef': {'fieldPath': 'status.hostIP'}}}]}],
                }}}},
            {'kind': 'PersistentVolumeClaim', 'metadata': {'name': 'data'},
             'spec': {'accessModes': ['ReadWriteOnce'], 'storageClassName': 'unsupported'}},
            {'kind': 'Ingress', 'metadata': {'name': 'example'},
             'spec': {'rules': [{'host': 'example.homelab.swhurl.com'}]}},
        ]
        self.assertEqual(app_policy.check(docs, known_teams={'demo': ''}, keys=''), [
            ('no-sa-token', 'Deployment/example mounts a service-account token'),
            ('no-host-access', 'Deployment/example uses host namespaces or hostPath'),
            ('prod-digest', 'Deployment/example/setup image r/setup:1 must pin a digest in prod'),
            ('image-pinned', 'Deployment/example/main image r/main:latest has no version tag or digest'),
            ('prod-digest', 'Deployment/example/main image r/main:latest must pin a digest in prod'),
            ('non-root', 'Deployment/example/main does not set runAsNonRoot'),
            ('no-escalation', 'Deployment/example/main allows privilege escalation or extra capabilities'),
            ('otlp-host-ip', 'Deployment/example/main uses $(HOST_IP) in OTEL_EXPORTER_OTLP_ENDPOINT without '
             'defining HOST_IP from status.hostIP before it'),
            ('resources', 'Deployment/example/main lacks CPU/memory requests or a memory limit'),
            ('single-writer', 'Deployment/example mounts a ReadWriteOnce claim and runs 2 replicas; at most 1'),
            ('single-writer', 'Deployment/example mounts a ReadWriteOnce claim, so it must stop the old pod first '
             '(strategy Recreate)'),
            ('storage-class', f'PersistentVolumeClaim/data must use one of {sorted(app_policy.STORAGE_CLASSES)}'),
            ('exposure', 'Ingress/example: public hosts must be outside homelab.swhurl.com (shared sign-in cookie)'),
            ('exposure', 'Ingress/example: every route under homelab.swhurl.com needs sign-in'),
            ('ingress-tls', "Ingress/example: hosts without TLS: ['example.homelab.swhurl.com']"),
        ])

    def test_otlp_fixture_renders_host_ip_before_its_use(self):
        names = [v['name'] for v in self.container(self.docs('smoke-web/staging'))['env']]
        self.assertLess(names.index('HOST_IP'), names.index('OTEL_EXPORTER_OTLP_ENDPOINT'))

    def test_otlp_without_host_ip_is_caught(self):
        for broken in ([],  # HOST_IP missing
                       [{'name': 'HOST_IP', 'value': '10.0.0.1'}],  # defined, but not the node IP
                       'after'):  # defined after its use: Kubernetes leaves $(HOST_IP) literal
            docs = self.docs('smoke-web/staging')
            env = self.container(docs)['env']
            host_ip = next(v for v in env if v['name'] == 'HOST_IP')
            env.remove(host_ip)
            if broken == 'after':
                env.append(host_ip)
            else:
                env[:0] = broken
            with self.subTest(broken=broken):
                self.assertIn('otlp-host-ip', self.rules(docs))

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
        def two_writers(d):
            self.find(d, 'Deployment')['spec']['replicas'] = 2
        def rolling_update(d):
            self.find(d, 'Deployment')['spec']['strategy'] = {'type': 'RollingUpdate'}
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
            ('smoke-data/prod', two_writers, 'single-writer'),
            ('smoke-data/prod', rolling_update, 'single-writer'),
        ]
        for key, mutate, rule in cases:
            with self.subTest(case=mutate.__name__):
                docs = self.docs(key)
                mutate(docs)
                self.assertIn(rule, self.rules(docs))

    def test_team_quota_and_network_policy_rules(self):
        def unknown_team(d):
            self.find(d, 'Namespace')['metadata']['labels'][contract.TEAM] = 'nobody'
        def no_quota(d):
            d.remove(self.find(d, 'ResourceQuota'))
        def other_quota(d):
            self.find(d, 'ResourceQuota')['spec']['hard']['pods'] = '100'
        def too_many_replicas(d):
            self.find(d, 'Deployment')['spec']['replicas'] = 13
        def too_much_memory(d):
            self.container(d)['resources']['limits']['memory'] = '4Gi'  # leaves no room for a backup pod
        def no_network_policy(d):
            d.remove(self.find(d, 'NetworkPolicy'))
        def every_pod(d):
            self.find(d, 'NetworkPolicy')['spec']['podSelector'] = {}
        def open_to_all(d):
            self.find(d, 'NetworkPolicy')['spec']['ingress'] = [{}]
        def wrong_port(d):
            self.find(d, 'NetworkPolicy')['spec']['ingress'][0]['ports'][0]['port'] = 9999
        def wrong_identity(d):
            self.find(d, 'HelmRelease')['spec']['values']['controllers']['main']['containers']['main']['env'][
                'SWHURL_ENV'] = 'prod'
        def stale_keys(d):
            self.find(d, 'HelmRelease')['spec']['values']['controllers']['main']['containers']['main']['env'][
                'SWHURL_AUTH_KEYS'] = '{"keys":[]}'
        def api_token(d):  # no audience: a token the Kubernetes API itself would accept
            app_policy.pod_spec(self.find(d, 'Deployment')).setdefault('volumes', []).append(
                {'name': 't', 'projected': {'sources': [{'serviceAccountToken': {'path': 'token'}}]}})
        def events_without_what_follows(d):
            self.find(d, 'HelmRelease')['spec']['values']['controllers']['main']['containers']['main']['env'][
                'SWHURL_EVENTS_PUBLISH'] = 'created'
        def stray_stream(d):
            d.append({'apiVersion': 'jetstream.nats.io/v1beta2', 'kind': 'Stream', 'metadata': {'name': 'x'}, 'spec': {}})
        def env_used_before_defined(d):
            env = self.container(d)['env']
            env.insert(0, {'name': 'EARLY', 'value': 'http://x-$(SWHURL_ENV)'})
        cases = [('smoke-web/staging', unknown_team, 'team'), ('smoke-web/staging', no_quota, 'quota'),
                 ('smoke-web/staging', other_quota, 'quota'), ('smoke-web/staging', too_many_replicas, 'quota'),
                 ('smoke-web/staging', too_much_memory, 'quota'), ('smoke-web/staging', no_network_policy, 'network-policy'),
                 ('smoke-web/staging', every_pod, 'network-policy'), ('smoke-worker/staging', open_to_all, 'network-policy'),
                 ('smoke-web/staging', wrong_port, 'network-policy'), ('smoke-web/staging', wrong_identity, 'identity'),
                 ('smoke-web/staging', stale_keys, 'identity'), ('smoke-worker/staging', api_token, 'call-tokens'),
                 ('smoke-web/staging', env_used_before_defined, 'identity'),
                 ('smoke-web/staging', events_without_what_follows, 'events'), ('smoke-worker/staging', stray_stream, 'events')]
        for key, mutate, rule in cases:
            with self.subTest(case=mutate.__name__):
                docs = self.docs(key)
                mutate(docs)
                self.assertEqual(self.rules(docs), {rule})
        # The registry is the root's: a team this checkout does not know passes when the caller's does.
        docs = self.docs('smoke-web/staging')
        unknown_team(docs)
        self.assertEqual(app_policy.check(docs, {'nobody': ''}), [])
        docs = self.docs('smoke-web/staging')
        every_pod(docs)
        message = dict(app_policy.check(docs))['network-policy']
        self.assertIn("HTTP-01 solver", message)

    def test_generator_checks_its_own_output(self):
        instance = FIXTURES / 'apps/smoke-web/staging'
        out = io.StringIO()
        with redirect_stdout(out):
            self.assertEqual(app_new.check_generated(instance), 0)
        self.assertIn('[OK] generated instance passes the app policy', out.getvalue())
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp)
        broken = tmp / 'instance'
        shutil.copytree(instance, broken)
        release = broken / 'helmrelease.yaml'
        release.write_text(release.read_text().replace(
            "traefik.ingress.kubernetes.io/router.middlewares: ingress-oauth-auth-shared@kubernetescrd", ''))
        out = io.StringIO()
        with redirect_stdout(out):
            self.assertEqual(app_new.check_generated(broken), 1)
        self.assertIn('generator and policy disagree', out.getvalue())

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



class PlatformImageTests(unittest.TestCase):
    """Rule platform-image: an instance under apps/ is an app made from a stack template."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        (self.tmp / 'clusters/home').mkdir(parents=True)
        (self.tmp / 'clusters/home/kustomization.yaml').write_text('resources: []\n')
        self.assertEqual(app_new.main(['w', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'staging', '--image',
                                       TEMPLATE_IMAGE, '--root', str(self.tmp), '--no-policy-check']), 0)
        self.staging = self.tmp / 'apps/w/staging'

    def test_a_template_app_passes_and_real_instances_do(self):
        self.assertEqual(app_policy.platform_image(self.staging), [])
        for instance in sorted((ROOT / 'apps').glob('*/*/')):
            with self.subTest(instance=instance.relative_to(ROOT)):
                self.assertEqual(app_policy.platform_image(instance), [])

    def test_a_foreign_image_or_a_staging_without_automation_fails(self):
        release = self.staging / 'helmrelease.yaml'
        original = release.read_text()
        release.write_text(original.replace('ghcr.io/samclement/w', 'docker.io/nginxinc/nginx-unprivileged'))
        self.assertIn('platform-image: image docker.io/nginxinc/nginx-unprivileged is not ghcr.io/samclement/w',
                      app_policy.platform_image(self.staging)[0])
        release.write_text(original)
        automation = self.staging / contract.IMAGE_AUTOMATION_FILE
        kept = automation.read_text()
        automation.write_text(kept.replace('name: w-staging', 'name: other'))
        self.assertIn('image-automation.yaml has no ImagePolicy w-staging', app_policy.platform_image(self.staging)[0])
        automation.write_text(kept)
        (self.staging / contract.IMAGE_AUTOMATION_FILE).unlink()
        self.assertIn('new images would not deploy', app_policy.platform_image(self.staging)[0])

    def test_it_applies_to_the_platform_tree_only(self):
        (self.staging / contract.IMAGE_AUTOMATION_FILE).unlink()
        with mock.patch.object(app_policy, 'render', return_value=[]), mock.patch.object(app_policy, 'check', return_value=[]):
            self.assertEqual(app_policy.evaluate(self.staging), [], 'fixtures and scratch trees are exempt')
            with mock.patch.object(app_policy, 'ROOT', self.tmp):
                self.assertEqual(len(app_policy.evaluate(self.staging)), 1)


if __name__ == '__main__':
    unittest.main()
