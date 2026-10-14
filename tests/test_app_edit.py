"""app-promote, app-scale, app-remove: Git edits of generated instances, offline on a copy of the sample instance."""
import difflib
import io
import json
import re
import shutil
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import yaml

from swhurl import ROOT
from swhurl.apps import calling, contract, derive, edit, new, ops, policy, promotion
from swhurl.apps.instance import Instance
from swhurl.run import CommandError, FakeRunner

NEW_DIGEST = 'sha256:' + 'b' * 64
SAMPLE = ROOT / 'tests/fixtures/instance'  # frozen sample instance; see its README


def changed_lines(before: str, after: str) -> list[str]:
    return [line for line in difflib.unified_diff(before.splitlines(), after.splitlines(), lineterm='', n=0)
            if line[:1] in '+-' and not line.startswith(('+++', '---'))]


class EditTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        for base, rel in ((SAMPLE, 'apps'), (SAMPLE, 'clusters'), (ROOT, 'platform/reloader/helmrelease.yaml')):
            src, dst = base / rel, self.root / rel
            dst.parent.mkdir(parents=True, exist_ok=True)
            (shutil.copytree if src.is_dir() else shutil.copy)(src, dst)
        self.staging = self.root / 'apps/hello/staging/helmrelease.yaml'
        self.prod = self.root / 'apps/hello/prod/helmrelease.yaml'

    def quiet(self, fn, *args, **kwargs):
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()):
            result = fn(*args, **kwargs)
        return result, out.getvalue()

    def test_promote_copies_only_tag_and_digest(self):
        self.staging.write_text(self.staging.read_text().replace('tag: 1.27-alpine', 'tag: 1.28-alpine')
                                .replace(self.staging.read_text().split('digest: ')[1].split('\n')[0], NEW_DIGEST))
        before = self.prod.read_text()
        self.quiet(edit.promote, self.root, 'hello', 'staging', 'prod')
        self.assertEqual(sorted(changed_lines(before, self.prod.read_text())),
                         sorted(['-              tag: 1.27-alpine', '+              tag: 1.28-alpine',
                                 f'-              digest: {before.split("digest: ")[1].splitlines()[0]}',
                                 f'+              digest: {NEW_DIGEST}']))

    def test_promote_refusals(self):
        with self.assertRaisesRegex(edit.EditError, 'already runs'):
            edit.promote(self.root, 'hello', 'staging', 'prod')
        with self.assertRaisesRegex(edit.EditError, 'must differ'):
            edit.promote(self.root, 'hello', 'prod', 'prod')
        text = self.staging.read_text()
        self.staging.write_text(text.replace('docker.io/nginxinc/nginx-unprivileged', 'ghcr.io/other/image'))
        with self.assertRaisesRegex(edit.EditError, 'change the repository by hand'):
            edit.promote(self.root, 'hello', 'staging', 'prod')
        self.staging.write_text('\n'.join(line for line in text.splitlines() if 'digest:' not in line) + '\n')
        with self.assertRaisesRegex(edit.EditError, 'no image digest'):
            edit.promote(self.root, 'hello', 'staging', 'prod')

    def test_promotion_refuses_a_source_that_moved_after_review_without_writing(self):
        reviewed = ops.image_reference(derive.container(derive.load_release(self.staging.parent))['image'])
        self.staging.write_text(self.staging.read_text().replace('tag: 1.27-alpine', 'tag: 1.28-alpine').replace(
            derive.container(derive.load_release(self.staging.parent))['image']['digest'], NEW_DIGEST))
        before = self.prod.read_bytes()
        with self.assertRaisesRegex(edit.EditError, 'changed since it was reviewed'):
            edit.promote(self.root, 'hello', 'staging', 'prod', expect_image=reviewed)
        self.assertEqual(self.prod.read_bytes(), before)
        current = ops.image_reference(derive.container(derive.load_release(self.staging.parent))['image'])
        self.quiet(edit.promote, self.root, 'hello', 'staging', 'prod', expect_image=current)
        self.assertIn('tag: 1.28-alpine', self.prod.read_text())

    def test_cli_promotion_checks_the_expected_image(self):
        before = self.prod.read_bytes()
        code, _ = self.quiet(edit.main_promote, ['hello', '--root', str(self.root), '--expect-image=x:1'])
        self.assertEqual(code, 2)
        self.assertEqual(self.prod.read_bytes(), before)

    def test_scale_preserves_handwritten_comments_quotes_and_flow_style(self):
        text = '# tuned by hand\n' + self.prod.read_text()
        text = text.replace('memory: 128Mi', 'memory: "128Mi"  # measured limit')
        text = text.replace('cpu: 10m\n                memory: 32Mi', "cpu: '10m'\n                memory: 32Mi")
        text = text.replace('type: RuntimeDefault', 'type: RuntimeDefault  # leave this alone')
        text += '# operator footer\n'
        self.prod.write_text(text)
        self.quiet(edit.scale, self.root, 'hello', 'prod', memory_limit='256Mi')
        self.assertEqual(self.prod.read_text(), text.replace('"128Mi"', '"256Mi"'))
        text = self.prod.read_text()
        text = text.replace("requests:\n                cpu: '10m'\n                memory: 32Mi",
                            "requests: {cpu: '10m', memory: 32Mi}")
        self.prod.write_text(text)
        self.quiet(edit.scale, self.root, 'hello', 'prod', cpu='20m')
        self.assertEqual(self.prod.read_text(), text.replace("'10m'", "'20m'"))

    def test_scale_preserves_four_space_mapping_indentation(self):
        text = ('# four space mappings\n'
                'spec:\n    values:\n        controllers:\n            main:\n'
                '                containers:\n                    main:\n'
                '                        resources:\n                            limits:\n'
                '                                memory: 128Mi\n')
        # Only a mapping scalar changes; indentation and every other line survive.
        self.prod.write_text(text)
        self.quiet(edit.scale, self.root, 'hello', 'prod', memory_limit='256Mi')
        self.assertEqual(self.prod.read_text(), text.replace('memory: 128Mi', 'memory: 256Mi'))

    def test_promote_preserves_target_quotes_and_manual_comments(self):
        text = self.prod.read_text().replace('tag: 1.27-alpine', 'tag: "1.27-alpine"  # release approved')
        self.prod.write_text('# production settings\n' + text)
        old_digest = derive.container(derive.load_release(self.staging.parent))['image']['digest']
        self.staging.write_text(self.staging.read_text().replace('tag: 1.27-alpine', "tag: '1.28-alpine'").replace(old_digest, NEW_DIGEST))
        before = self.prod.read_text()
        self.quiet(edit.promote, self.root, 'hello', 'staging', 'prod')
        self.assertEqual(self.prod.read_text(), before.replace('"1.27-alpine"', '"1.28-alpine"').replace(old_digest, NEW_DIGEST))

    def test_scale_refuses_invalid_or_shared_target_without_writing(self):
        original = self.prod.read_text()
        cases = [original + '\n---\nextra: document\n', original + 'spec: {}\n',
                 original.replace('resources:', 'resources: &shared'),
                 original.replace('resources:', 'resources: [not-a-mapping]\n            ignored:')]
        for text in cases:
            with self.subTest(text=text[-80:]):
                self.prod.write_text(text)
                with self.assertRaises(edit.EditError):
                    edit.scale(self.root, 'hello', 'prod', memory_limit='256Mi')
                self.assertEqual(self.prod.read_text(), text)

    def test_requested_policy_failure_is_nonzero_after_edit(self):
        with mock.patch.object(new.policy, 'evaluate', side_effect=CommandError(['helm'], 'helm unavailable')):
            code, out = self.quiet(edit.main_scale, ['hello', 'prod', '--memory-limit=256Mi', '--root', str(self.root)])
        self.assertEqual(code, 1)
        self.assertIn('Files remain for review', out)
        self.assertNotIn('Next: commit', out)
        self.assertIn('memory: 256Mi', self.prod.read_text())

    def test_scale_changes_only_what_was_asked(self):
        before = self.prod.read_text()
        self.quiet(edit.scale, self.root, 'hello', 'prod', replicas=2, memory_limit='256Mi')
        self.assertEqual(sorted(changed_lines(before, self.prod.read_text())),
                         sorted(['+        replicas: 2', '-                memory: 128Mi', '+                memory: 256Mi']))
        with self.assertRaisesRegex(edit.EditError, 'already has'):
            edit.scale(self.root, 'hello', 'prod', replicas=2)

    def test_edits_keep_automatic_deploy_markers(self):
        self.staging.write_text(contract.add_image_markers(self.staging.read_text(), 'hello-staging'))
        self.quiet(edit.scale, self.root, 'hello', 'staging', replicas=2)
        text = self.staging.read_text()
        self.assertIn('replicas: 2', text)
        self.assertEqual(contract.strip_image_markers(text)[1], 'hello-staging')
        before = self.prod.read_text()
        self.staging.write_text(text.replace('tag: 1.27-alpine', 'tag: 1.28-alpine').replace(
            derive.container(derive.load_release(self.staging.parent))['image']['digest'], NEW_DIGEST))
        self.quiet(edit.promote, self.root, 'hello', 'staging', 'prod')
        self.assertIn('tag: 1.28-alpine\n', self.prod.read_text(), 'production gets the tag, never the markers')
        self.assertNotIn('imagepolicy', self.prod.read_text())
        self.assertNotEqual(before, self.prod.read_text())

    def expose(self, env, exposure, host=None):
        return self.quiet(edit.expose, self.root, 'hello', env, exposure, host)

    def state(self, env):
        instance = self.root / 'apps/hello' / env
        values = derive.load_release(instance)['spec']['values']
        route = (values.get('ingress') or {}).get('main')
        namespace = yaml.safe_load((instance / 'namespace.yaml').read_text())
        unit = yaml.safe_load((self.root / f'clusters/home/app-hello-{env}.yaml').read_text())
        return {'exposure': namespace['metadata']['labels'][contract.EXPOSURE],
                'host': route['hosts'][0]['host'] if route else None,
                'sign_in': bool(route) and contract.AUTH_MIDDLEWARE in str(route['annotations']),
                'depends': [d['name'] for d in unit['spec']['dependsOn']]}

    def test_expose_switches_route_sign_in_label_and_dependencies(self):
        self.expose('staging', 'public', 'hello.example.com')
        self.assertEqual(self.state('staging'), {'exposure': 'public', 'host': 'hello.example.com', 'sign_in': False,
                                                 'depends': ['infra-base']})
        self.expose('staging', 'private')
        self.assertEqual(self.state('staging'), {'exposure': 'private', 'host': None, 'sign_in': False,
                                                 'depends': ['infra-base']})
        self.expose('staging', 'authenticated-web')  # host derived
        self.assertEqual(self.state('staging'), {'exposure': 'authenticated-web', 'sign_in': True,
                                                 'host': 'staging-hello.homelab.swhurl.com',
                                                 'depends': ['infra-base', 'platform-oauth2-proxy']})
        instances = [self.root / 'apps/hello/staging', self.root / 'apps/hello/prod']
        self.expose('staging', 'public', 'hello.example.com')
        self.assertEqual(policy.drift(instances), [], 'exposure may differ between environments')

    def network_policy(self, env):
        return yaml.safe_load((self.root / 'apps/hello' / env / 'networkpolicy.yaml').read_text())['spec']['ingress']

    def test_expose_opens_and_closes_the_network_policy_with_the_route(self):
        traefik = [{'from': [contract.TRAEFIK], 'ports': [{'port': 8080, 'protocol': 'TCP'}]}]
        self.assertEqual(self.network_policy('staging'), traefik)
        self.expose('staging', 'private')
        self.assertEqual(self.network_policy('staging'), [], 'no route: Traefik is no longer admitted')
        self.expose('staging', 'public', 'hello.example.com')
        self.assertEqual(self.network_policy('staging'), traefik)
        self.assertEqual(self.network_policy('prod'), traefik, 'the other environment is untouched')

    def alert_settings(self, env):
        metadata = yaml.safe_load((self.root / 'apps/hello' / env / 'namespace.yaml').read_text())['metadata']
        return (metadata.get('annotations') or {}).get('platform.swhurl.com/alerts')

    def test_alert_settings_change_in_every_environment_and_go_back_to_the_defaults(self):
        _, out = self.quiet(edit.set_alerts, self.root, 'hello', {'error-logs': '20'})
        self.assertEqual((self.alert_settings('staging'), self.alert_settings('prod')), ('error-logs=20', 'error-logs=20'))
        self.assertIn('[OK] hello: alerts error-logs >= 20, errors >= 5 (staging, prod)', out)
        self.quiet(edit.set_alerts, self.root, 'hello', {'errors': 'none'})
        self.assertEqual(self.alert_settings('prod'), 'error-logs=20,errors=none', 'a change keeps what was set before')
        self.quiet(edit.set_alerts, self.root, 'hello', {'error-logs': '5'})
        self.assertEqual(self.alert_settings('staging'), 'errors=none', 'a default is not written down')
        self.quiet(edit.set_alerts, self.root, 'hello', none=True)
        self.assertEqual(self.alert_settings('staging'), 'none')
        self.quiet(edit.set_alerts, self.root, 'hello', default=True)
        self.assertEqual((self.alert_settings('staging'), self.alert_settings('prod')), (None, None))
        self.assertNotIn('annotations: {}', (self.root / 'apps/hello/staging/namespace.yaml').read_text())
        for kwargs, message in (({'changes': {'errors': 'many'}}, "errors='many': give a number from 1"),
                                ({'default': True}, 'hello already has those alert settings'),
                                ({}, 'give thresholds'), ({'changes': {'errors': '9'}, 'none': True}, 'give thresholds')):
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.set_alerts(self.root, 'hello', **kwargs)
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
            self.assertEqual(edit.main_alerts(['hello', '--errors', 'none', '--error-logs', '9', '--root', str(self.root)]), 0)
        self.assertEqual(self.alert_settings('prod'), 'error-logs=9,errors=none')

    def test_team_moves_every_environment_and_fills_in_missing_isolation(self):
        (self.root / 'apps/teams.yaml').write_text('teams:\n  platform: {}\n  payments: {}\n')
        for env in ('staging', 'prod'):  # an instance from before quotas and policies existed
            instance = self.root / 'apps/hello' / env
            for name in contract.ISOLATION_FILES:
                (instance / name).unlink()
            listing = instance / 'kustomization.yaml'
            listing.write_text(''.join(line for line in listing.read_text().splitlines(keepends=True)
                                       if not any(name in line for name in contract.ISOLATION_FILES)))
        self.quiet(edit.set_team, self.root, 'hello', 'payments')
        for env in ('staging', 'prod'):
            instance = self.root / 'apps/hello' / env
            labels = yaml.safe_load((instance / 'namespace.yaml').read_text())['metadata']['labels']
            self.assertEqual(labels[contract.TEAM], 'payments')
            resources = yaml.safe_load((instance / 'kustomization.yaml').read_text())['resources']
            self.assertEqual(resources[-2:], list(contract.ISOLATION_FILES))
            self.assertEqual(yaml.safe_load((instance / 'networkpolicy.yaml').read_text())['metadata']['namespace'],
                             f'hello-{env}')
        self.assertEqual(policy.drift([self.root / 'apps/hello/prod', self.root / 'apps/hello/staging']), [])
        with self.assertRaisesRegex(edit.EditError, 'already belongs to payments'):
            edit.set_team(self.root, 'hello', 'payments')
        before = (self.root / 'apps/hello/staging/namespace.yaml').read_text()
        with self.assertRaisesRegex(edit.EditError, "unknown team 'nobody'"):
            edit.set_team(self.root, 'hello', 'nobody')
        self.assertEqual((self.root / 'apps/hello/staging/namespace.yaml').read_text(), before)

    def env(self, app, env='staging'):
        release = yaml.safe_load((self.root / 'apps' / app / env / 'helmrelease.yaml').read_text())
        return release['spec']['values']['controllers']['main']['containers']['main']['env']

    def second_app(self, name='orders', port=9000):
        """Another web app beside hello, in both environments, made from hello's files."""
        for env in ('staging', 'prod'):
            shutil.copytree(self.root / 'apps/hello' / env, self.root / 'apps' / name / env)
            for path in (self.root / 'apps' / name / env).glob('*.yaml'):
                path.write_text(path.read_text().replace('hello', name).replace('port: 8080', f'port: {port}'))
        self.quiet(derive.sync, self.root)

    def test_calls_give_the_caller_an_address_and_token_and_the_callee_an_allowed_caller(self):
        self.second_app()
        self.quiet(edit.set_calls, self.root, 'hello', 'staging', add=['orders'])
        self.assertEqual(self.env('hello')['SWHURL_SERVICE_ORDERS_URL'],
                         'http://orders.orders-$(SWHURL_ENV).svc.cluster.local:9000')
        release = yaml.safe_load((self.root / 'apps/hello/staging/helmrelease.yaml').read_text())
        self.assertEqual(release['spec']['values']['persistence']['tokens'], calling.tokens_volume(['orders']))
        self.assertEqual(self.env('orders')['SWHURL_ALLOWED_CALLERS'], 'hello')
        admitted = yaml.safe_load((self.root / 'apps/orders/staging/networkpolicy.yaml').read_text())['spec']['ingress']
        self.assertEqual(admitted, [{'from': [contract.TRAEFIK, {
            'namespaceSelector': {'matchLabels': {'kubernetes.io/metadata.name': 'hello-staging'}},
            'podSelector': {'matchLabels': {'app.kubernetes.io/instance': 'hello'}}}],
            'ports': [{'port': 9000, 'protocol': 'TCP'}]}])
        self.assertNotIn('SWHURL_ALLOWED_CALLERS', self.env('orders', 'prod'), 'production is its own decision')
        instances = [self.root / 'apps' / app / env for app in ('hello', 'orders') for env in ('staging', 'prod')]
        self.assertEqual(policy.check_calls(instances), {})
        self.assertEqual(policy.drift([self.root / 'apps/hello/prod', self.root / 'apps/hello/staging'])
                         + policy.drift([self.root / 'apps/orders/prod', self.root / 'apps/orders/staging']), [],
                         'calls may differ between environments')
        self.assertEqual(policy.evaluate(self.root / 'apps/hello/staging') + policy.evaluate(self.root / 'apps/orders/staging'), [])

        with self.assertRaisesRegex(edit.EditError, 'is called by hello: remove those calls first'):
            edit.remove(self.root, 'orders', 'staging')
        self.quiet(edit.set_calls, self.root, 'hello', 'staging', remove=['orders'])
        self.assertNotIn('SWHURL_SERVICE_ORDERS_URL', self.env('hello'))
        self.assertNotIn('SWHURL_ALLOWED_CALLERS', self.env('orders'))
        release = yaml.safe_load((self.root / 'apps/hello/staging/helmrelease.yaml').read_text())
        self.assertEqual([source['serviceAccountToken']['audience'] for source in release['spec']['values']['persistence']['tokens']['sources']],
                         ['notify'], 'only the token every instance carries for the notify service is left')
        admitted = yaml.safe_load((self.root / 'apps/orders/staging/networkpolicy.yaml').read_text())['spec']['ingress']
        self.assertEqual(admitted[0]['from'], [contract.TRAEFIK])

    def test_calls_refusals_change_nothing(self):
        self.second_app()
        before = self.staging.read_text()
        cases = (({'add': ['hello']}, 'does not declare calls to itself'), ({'add': ['nowhere']}, 'no instance nowhere/staging'),
                 ({'remove': ['orders']}, 'does not call orders'), ({}, 'give --add and/or --remove'))
        for kwargs, message in cases:
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.set_calls(self.root, 'hello', 'staging', **kwargs)
        self.assertEqual(self.staging.read_text(), before)
        self.quiet(edit.set_calls, self.root, 'hello', 'staging', add=['orders'])
        with self.assertRaisesRegex(edit.EditError, 'already calls orders'):
            edit.set_calls(self.root, 'hello', 'staging', add=['orders'])

    def test_one_sided_calls_are_policy_violations(self):
        self.second_app()
        self.quiet(edit.set_calls, self.root, 'hello', 'staging', add=['orders'])
        orders = self.root / 'apps/orders/staging/helmrelease.yaml'
        orders.write_text(orders.read_text().replace('SWHURL_ALLOWED_CALLERS: hello', 'SWHURL_ALLOWED_CALLERS: stranger'))
        instances = [self.root / 'apps/hello/staging', self.root / 'apps/orders/staging']
        problems = policy.check_calls(instances)
        self.assertIn('calls: calls orders, which does not allow it; make app-sync rewrites both sides',
                      problems[instances[0]])
        self.assertIn('calls: allows stranger, which does not declare a call to it; make app-sync rewrites both sides',
                      problems[instances[1]])
        self.quiet(derive.sync, self.root)
        self.assertEqual(policy.check_calls(instances), {})

    def test_signing_keys_refresh_rewrites_the_file_and_every_web_app(self):
        from swhurl.apps import keys
        from swhurl.run import FakeRunner
        (self.root / 'apps/signing-keys.json').write_text('{"keys": [{"kid": "old"}]}')
        self.quiet(derive.sync, self.root)
        self.assertEqual(self.env('hello')['SWHURL_AUTH_KEYS'], '{"keys":[{"kid":"old"}]}')
        runner = FakeRunner().on(*keys.JWKS, stdout='{"keys": [{"kid": "new"}]}')
        code, out = self.quiet(keys.main, ['--root', str(self.root)], runner)
        self.assertEqual(code, 0, out)
        self.assertEqual(json.loads((self.root / 'apps/signing-keys.json').read_text()), {'keys': [{'kid': 'new'}]})
        for env in ('staging', 'prod'):
            self.assertEqual(self.env('hello', env)['SWHURL_AUTH_KEYS'], '{"keys":[{"kid":"new"}]}')
        code, out = self.quiet(keys.main, ['--root', str(self.root)], runner)
        self.assertIn('already holds', out)
        code, out = self.quiet(keys.main, ['--root', str(self.root)], FakeRunner().on(*keys.JWKS, returncode=1, stderr='refused'))
        self.assertEqual(code, 1)
        self.assertIn('could not read the signing keys', out)

    def sops(self):
        """A runner whose sops marks the file encrypted, as the real one does, and hides the seed."""
        from swhurl.run import FakeRunner, Result

        def encrypt(args, _input):
            path = Path(args[-1])
            self.seeds.append(yaml.safe_load(path.read_text())['stringData']['SWHURL_EVENTS_SEED'])
            path.write_text(path.read_text().replace(self.seeds[-1], 'ENC[...]') + 'sops:\n  age: []\n')
            return Result(args, 0, '')
        self.seeds = []
        return FakeRunner().on('sops', handler=encrypt)

    def with_units(self, name='orders'):
        self.second_app(name)
        for env in ('staging', 'prod'):
            source = self.root / f'clusters/home/app-hello-{env}.yaml'
            (self.root / f'clusters/home/app-{name}-{env}.yaml').write_text(source.read_text().replace('hello', name))
        key = self.root / 'platform/nats/controller-nkey.pub'
        key.parent.mkdir(parents=True, exist_ok=True)
        key.write_text('UCONTROLLER\n')

    def test_events_give_a_publisher_a_stream_and_a_subscriber_a_consumer_and_both_a_nats_user(self):
        from swhurl.apps import events
        self.with_units()
        runner = self.sops()
        self.quiet(edit.set_events, self.root, 'orders', 'staging', publish=['created', 'cancelled'], runner=runner)
        self.quiet(edit.set_events, self.root, 'hello', 'staging', subscribe=['orders.created'], runner=runner)

        publisher, subscriber = self.env('orders'), self.env('hello')
        self.assertEqual((publisher['SWHURL_EVENTS_PUBLISH'], publisher['SWHURL_EVENTS_URL']), ('created,cancelled', events.URL))
        self.assertEqual(subscriber['SWHURL_EVENTS_SUBSCRIBE'], 'orders.created')
        for app in ('orders', 'hello'):
            key = self.env(app)['SWHURL_EVENTS_NKEY']
            self.assertTrue(key.startswith('U') and len(key) == 56)
            secret = (self.root / 'apps' / app / 'staging/events.sops.yaml').read_text()
            self.assertIn('sops:', secret)
            self.assertFalse(any(seed in secret for seed in self.seeds), 'the seed is only ever in the encrypted file')
            release = yaml.safe_load((self.root / 'apps' / app / 'staging/helmrelease.yaml').read_text())
            main = release['spec']['values']['controllers']['main']
            self.assertIn({'secretRef': {'name': f'{app}-events'}}, main['containers']['main']['envFrom'])
            self.assertIn(f'{app}-events', main['annotations']['secret.reloader.stakater.com/reload'].split(','))
            unit = yaml.safe_load((self.root / f'clusters/home/app-{app}-staging.yaml').read_text())['spec']
            self.assertIn({'name': 'platform-nats'}, unit['dependsOn'])
            self.assertEqual(unit['decryption']['provider'], 'sops')
            listed = yaml.safe_load((self.root / 'apps' / app / 'staging/kustomization.yaml').read_text())['resources']
            self.assertLessEqual({'events.yaml', 'events.sops.yaml'}, set(listed))
        self.assertIn('orders-staging', (self.root / 'platform/reloader/helmrelease.yaml').read_text())
        self.assertEqual(len(self.seeds), 2, 'a later change reuses the key')
        self.quiet(edit.set_events, self.root, 'orders', 'staging', publish=['created', 'cancelled', 'paid'], runner=runner)
        self.assertEqual(len(self.seeds), 2)

        stream, = yaml.safe_load_all((self.root / 'apps/orders/staging/events.yaml').read_text())
        self.assertEqual((stream['kind'], stream['spec']['name'], stream['spec']['subjects'], stream['spec']['maxAge']),
                         ('Stream', 'staging-orders', ['staging.orders.>'], '168h'))
        consumer, = yaml.safe_load_all((self.root / 'apps/hello/staging/events.yaml').read_text())
        self.assertEqual((consumer['kind'], consumer['spec']['streamName'], consumer['spec']['durableName'],
                          consumer['spec']['filterSubject']),
                         ('Consumer', 'staging-orders', 'hello_created', 'staging.orders.created'))

        users = yaml.safe_load(yaml.safe_load((self.root / 'platform/nats/users.yaml').read_text())['data']['values.yaml'])
        users = {u['nkey']: u['permissions'] for u in users['config']['merge']['authorization']['users']}
        self.assertEqual(users['UCONTROLLER']['publish']['allow'], ['$JS.API.>'])
        self.assertEqual(users[self.env('orders')['SWHURL_EVENTS_NKEY']], {
            # Its own flag overrides, and its whole prefix: a new image may carry a new event.
            'publish': {'allow': [*events.flag_permissions('staging', 'orders'), 'staging.orders.>']},
            'subscribe': {'allow': ['_INBOX.staging.orders.>']}})
        self.assertEqual(users[self.env('hello')['SWHURL_EVENTS_NKEY']], {
            'publish': {'allow': [*events.flag_permissions('staging', 'hello'),
                                  '$JS.API.CONSUMER.INFO.staging-orders.hello_created',
                                  '$JS.API.CONSUMER.MSG.NEXT.staging-orders.hello_created',
                                  '$JS.ACK.staging-orders.hello_created.>',
                                  '$JS.API.STREAM.INFO.staging-orders']},  # the Java client asks before it binds
            'subscribe': {'allow': ['_INBOX.staging.hello.>']}})
        self.assertNotIn('SWHURL_EVENTS_PUBLISH', self.env('orders', 'prod'), 'production is its own decision')
        self.assertEqual(policy.drift([self.root / 'apps/orders/prod', self.root / 'apps/orders/staging'])
                         + policy.drift([self.root / 'apps/hello/prod', self.root / 'apps/hello/staging']), [])
        instances = [self.root / 'apps' / app / 'staging' for app in ('hello', 'orders')]
        self.assertEqual(policy.check_calls(instances), {})
        self.assertCountEqual(policy.check_event_users(self.root), [  # every instance needs its own key
            f'event-users: {app}/prod has no NATS key; make app-sync creates it' for app in ('hello', 'orders')])

        with self.assertRaisesRegex(edit.EditError, 'publishes events that hello subscribe to'):
            edit.remove(self.root, 'orders', 'staging')
        with self.assertRaisesRegex(edit.EditError, 'subscribes to orders.created, which orders/staging does not publish'):
            edit.set_events(self.root, 'orders', 'staging', publish=['paid'], runner=runner)
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_PUBLISH'], 'created,cancelled,paid', 'a refused change is undone')

        # Without events an instance keeps its NATS identity (its flag overrides), and loses its consumers.
        self.quiet(edit.set_events, self.root, 'hello', 'staging', subscribe=[], runner=runner)
        self.assertNotIn('SWHURL_EVENTS_SUBSCRIBE', self.env('hello'))
        self.assertEqual(self.env('hello')['SWHURL_EVENTS_URL'], events.URL)
        self.assertFalse((self.root / 'apps/hello/staging/events.yaml').exists())
        self.assertTrue((self.root / 'apps/hello/staging/events.sops.yaml').exists())
        listed = yaml.safe_load((self.root / 'apps/hello/staging/kustomization.yaml').read_text())['resources']
        self.assertIn('events.sops.yaml', listed)
        self.assertNotIn('events.yaml', listed)
        unit = yaml.safe_load((self.root / 'clusters/home/app-hello-staging.yaml').read_text())['spec']
        self.assertIn({'name': 'platform-nats'}, unit['dependsOn'])
        users = yaml.safe_load(yaml.safe_load((self.root / 'platform/nats/users.yaml').read_text())['data']['values.yaml'])
        users = {u['nkey']: u['permissions'] for u in users['config']['merge']['authorization']['users']}
        self.assertEqual(users[self.env('hello')['SWHURL_EVENTS_NKEY']]['publish']['allow'],
                         events.flag_permissions('staging', 'hello'))
        self.assertEqual(len(users), 3, 'the controller, orders and hello')

    def test_app_sync_gives_every_instance_its_own_nats_key_and_the_console_a_writer(self):
        from swhurl.apps import events
        self.with_units()
        (self.root / events.CONSOLE_KEY_FILE).write_text('UCONSOLE\n')
        runner = self.sops()
        for app in ('hello', 'orders'):
            for env in ('staging', 'prod'):
                self.assertTrue(edit.nats_key(self.root, app, env, runner=runner))
                self.assertFalse(edit.nats_key(self.root, app, env, runner=runner), 'an instance keeps its key')
        self.quiet(derive.sync, self.root)
        self.assertEqual(policy.check_event_users(self.root), [])
        self.assertEqual(policy.check_events([], yaml.safe_load((self.root / 'apps/hello/prod/helmrelease.yaml').read_text()),
                                             {'platform.swhurl.com/environment': 'prod'}), [])
        users = yaml.safe_load(yaml.safe_load((self.root / 'platform/nats/users.yaml').read_text())['data']['values.yaml'])
        users = {u['nkey']: u['permissions'] for u in users['config']['merge']['authorization']['users']}
        self.assertEqual(users[self.env('hello', 'prod')['SWHURL_EVENTS_NKEY']]['publish']['allow'],
                         events.flag_permissions('prod', 'hello'), 'its own keys in its own environment only')
        console = users['UCONSOLE']
        self.assertIn('$KV.flags-staging.>', console['publish']['allow'])
        self.assertIn('$KV.flags-prod.>', console['publish']['allow'])
        self.assertEqual(console['subscribe']['allow'], ['_INBOX.console.>'])
        self.assertFalse([rule for rule in console['publish']['allow'] if 'staging.' in rule and 'flags' not in rule],
                         'the console touches no event subjects')

    def test_events_are_added_and_removed_one_at_a_time_like_calls(self):
        self.with_units()
        runner = self.sops()
        self.quiet(edit.set_events, self.root, 'orders', 'staging', add_publish=['created'], runner=runner)
        self.quiet(edit.set_events, self.root, 'orders', 'staging', add_publish=['paid'], runner=runner)
        self.quiet(edit.set_events, self.root, 'hello', 'staging', add_subscribe=['orders.created'], runner=runner)
        self.quiet(edit.set_events, self.root, 'hello', 'staging', add_subscribe=['orders.paid'],
                   remove_subscribe=['orders.created'], runner=runner)
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_PUBLISH'], 'created,paid', 'what was there stays')
        self.assertEqual(self.env('hello')['SWHURL_EVENTS_SUBSCRIBE'], 'orders.paid')
        cases = (('orders', {'add_publish': ['paid']}, 'already there: paid'),
                 ('orders', {'remove_publish': ['refunded']}, 'not there to remove: refunded'),
                 ('orders', {'remove_publish': ['paid']}, 'subscribes to orders.paid, which orders/staging does not publish'),
                 ('orders', {'publish': ['x'], 'add_publish': ['y']}, '--publish replaces the list'),
                 ('hello', {'add_subscribe': ['orders.refunded']}, 'which orders/staging does not publish'))
        for app, kwargs, message in cases:
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.set_events(self.root, app, 'staging', runner=runner, **kwargs)
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_PUBLISH'], 'created,paid', 'a refused change is undone')
        self.quiet(edit.set_events, self.root, 'orders', 'staging', remove_publish=['created'], runner=runner)
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_PUBLISH'], 'paid')

    ASYNCAPI = """asyncapi: 3.0.0
info: {title: orders, version: 0.1.0}
channels:
  created:
    address: created
    description: An order was accepted
    messages:
      created: {$ref: '#/components/messages/created'}
  paid:
    address: paid
    description: An order was paid
    messages:
      paid: {payload: {type: object}, examples: [{payload: {id: 1}}]}
  heard:
    address: heard
operations:
  sendCreated: {action: send, channel: {$ref: '#/channels/created'}}
  sendPaid: {action: send, channel: {$ref: '#/channels/paid'}}
  receiveHeard: {action: receive, channel: {$ref: '#/channels/heard'}}
components:
  messages:
    created:
      payload: {type: object, required: [id], properties: {id: {type: string}}}
      examples: [{payload: {id: o-1}}]
"""

    def repository(self, files):
        """An opener serving one app repository at one commit, and the requests it saw."""
        import urllib.error
        seen = []

        def opener(request):
            seen.append(request.full_url)
            path = request.full_url.split('/contents/')[1].split('?')[0]
            if path not in files:
                raise urllib.error.HTTPError(request.full_url, 404, 'Not Found', {}, None)
            return files[path].encode()
        return opener, seen

    def built_from_a_repository(self, app='orders', tag='7-abc1234'):
        for env in ('staging', 'prod'):
            path = self.root / 'apps' / app / env / 'helmrelease.yaml'
            path.write_text(path.read_text().replace('docker.io/nginxinc/nginx-unprivileged', f'ghcr.io/samclement/{app}')
                            .replace('tag: 1.27-alpine', f'tag: {tag}' if env == 'staging' else 'tag: 6-0000000'))

    def test_offers_are_read_at_the_images_commit_and_say_what_the_app_publishes(self):
        from swhurl.apps import offers
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        manifest = 'version: 1\nkind: web\nhealthPath: /healthz\nprovides: {api: /openapi.json, apiFile: openapi.json, eventsFile: asyncapi.yaml}\n'
        api = '{"openapi": "3.1.0", "info": {"title": "orders", "version": "0.1.0"}, "paths": {"/": {}}}\n'
        opener, seen = self.repository({'swhurl.yaml': manifest, 'openapi.json': api, 'asyncapi.yaml': self.ASYNCAPI})
        changed, _ = self.quiet(edit.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)
        self.assertTrue(changed)
        self.assertTrue(all('/repos/samclement/orders/contents/' in url and url.endswith('?ref=abc1234') for url in seen), seen)
        instance = self.root / 'apps/orders/staging'
        self.assertEqual({p.name for p in (instance / 'offers').iterdir()}, {'source.yaml', 'openapi.json', 'asyncapi.yaml'})
        self.assertEqual((instance / 'offers/asyncapi.yaml').read_text(), self.ASYNCAPI, 'kept as the repository has it')
        found = offers.read(instance)
        self.assertEqual((found.repository, found.commit, found.api, found.events), ('samclement/orders', 'abc1234', 'openapi.json', 'asyncapi.yaml'))
        self.assertEqual([(e.name, e.description, e.example) for e in found.described],
                         [('created', 'An order was accepted', {'id': 'o-1'}), ('paid', 'An order was paid', {'id': 1})],
                         'the channels it sends to; a reference is followed')
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_PUBLISH'], 'created,paid', 'what it publishes follows the document')
        self.assertEqual(len(self.seeds), 1, 'its NATS key is made the first time')
        release = yaml.safe_load((instance / 'helmrelease.yaml').read_text())
        self.assertEqual(release['metadata']['annotations'][offers.ANNOTATION], 'abc1234')
        self.assertNotIn('offers', yaml.safe_load((instance / 'kustomization.yaml').read_text())['resources'], 'documents, not resources')
        self.assertEqual(self.env('orders')['SWHURL_API_PATH'], '/openapi.json', 'where it serves its description, from swhurl.yaml')
        self.assertEqual(offers.problems(instance, ['created', 'paid'], 'abc1234'), [])
        self.assertIn('describes created, paid', offers.problems(instance, ['created'], 'abc1234')[0])
        self.assertIn('the release says its offers are from 0000000', offers.problems(instance, ['created', 'paid'], '0000000')[0])
        self.assertEqual(self.quiet(edit.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)[0], False,
                         'reading the same commit again changes nothing')

        # Another app subscribes; a later image that drops the event is refused and nothing changes.
        self.quiet(edit.set_events, self.root, 'hello', 'staging', add_subscribe=['orders.paid'], runner=runner)
        dropped = self.ASYNCAPI.replace("  sendPaid: {action: send, channel: {$ref: '#/channels/paid'}}\n", '')
        opener, _ = self.repository({'swhurl.yaml': manifest, 'openapi.json': api, 'asyncapi.yaml': dropped})
        before = {p: p.read_text() for p in instance.rglob('*') if p.is_file()}
        with self.assertRaisesRegex(edit.EditError, 'hello/staging subscribes to orders.paid, which orders/staging does not publish'):
            edit.set_offers(self.root, 'orders', 'staging', opener=opener, runner=runner)
        self.assertEqual({p: p.read_text() for p in instance.rglob('*') if p.is_file()}, before)

        # An app that stops describing itself keeps what it publishes; only the documents go.
        opener, _ = self.repository({'swhurl.yaml': 'version: 1\nkind: web\nhealthPath: /healthz\n'})
        self.quiet(edit.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)
        self.assertFalse((instance / 'offers').exists())
        self.assertNotIn('annotations', yaml.safe_load((instance / 'helmrelease.yaml').read_text())['metadata'])
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_PUBLISH'], 'created,paid')

    def test_declared_flags_are_kept_with_the_images_commit_and_carried_by_promotion(self):
        import json

        from swhurl.apps import offers
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        manifest = ('version: 1\nkind: web\nhealthPath: /healthz\n'
                    'flags: {shout: {type: boolean, description: Say hello in capitals, default: false, staging: true}}\n')
        opener, _ = self.repository({'swhurl.yaml': manifest})
        self.quiet(edit.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)
        instance = self.root / 'apps/orders/staging'
        self.assertEqual({p.name for p in (instance / 'offers').iterdir()}, {'source.yaml', 'flags.json'})
        found = offers.read(instance)
        self.assertEqual((found.api, found.events, found.flags), ('', '', 'flags.json'), 'not mistaken for an API description')
        self.assertEqual(found.declared, {'shout': {'type': 'boolean', 'description': 'Say hello in capitals', 'default': False, 'staging': True}})
        self.assertEqual(offers.problems(instance, [], 'abc1234'), [])
        staging = instance / 'helmrelease.yaml'
        staging.write_text(staging.read_text().replace(staging.read_text().split('digest: ')[1].split('\n')[0], NEW_DIGEST))
        self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod', runner=runner)
        self.assertEqual(offers.read(self.root / 'apps/orders/prod').declared, found.declared, 'production gets the image\'s flags')
        (instance / 'offers/flags.json').write_text(json.dumps({'shout': {'type': 'boolean', 'default': 'yes'}}))
        self.assertIn("flags.shout.default must be a boolean, not 'yes'", offers.problems(instance, [], 'abc1234')[0])

    def test_offers_that_cannot_be_used_are_refused_and_change_nothing(self):
        self.with_units()
        self.built_from_a_repository()
        instance = self.root / 'apps/orders/staging'
        before = (instance / 'helmrelease.yaml').read_text()
        manifest = 'version: 1\nkind: web\nhealthPath: /healthz\nprovides: {apiFile: openapi.json, eventsFile: asyncapi.yaml}\n'
        api = 'openapi: 3.0.1\npaths: {}\n'
        cases = (({}, 'has no swhurl.yaml'),
                 ({'swhurl.yaml': manifest, 'openapi.json': api}, r'names asyncapi.yaml \(provides.eventsFile\), which samclement/orders@abc1234 does not have'),
                 ({'swhurl.yaml': manifest, 'openapi.json': 'swagger: "2.0"', 'asyncapi.yaml': self.ASYNCAPI}, 'must be an OpenAPI 3 document'),
                 ({'swhurl.yaml': manifest, 'openapi.json': api, 'asyncapi.yaml': 'asyncapi: 2.6.0'}, 'must be an AsyncAPI 3 document'),
                 ({'swhurl.yaml': manifest, 'openapi.json': api, 'asyncapi.yaml': self.ASYNCAPI.replace('address: paid', 'address: Paid.Up')},
                  'an event is a lowercase name'),
                 ({'swhurl.yaml': manifest, 'openapi.json': api, 'asyncapi.yaml': '{asyncapi: 3.0.0, channels: [oops'}, 'not valid YAML'),
                 ({'swhurl.yaml': manifest.replace('openapi.json', '../../etc/passwd')}, 'provides.apiFile is a file in the repository'),
                 ({'swhurl.yaml': 'version: 1\nkind: web\n'}, 'samclement/orders@abc1234:swhurl.yaml: healthPath is required'))
        for files, message in cases:
            with self.subTest(message=message), self.assertRaisesRegex(edit.EditError, message):
                edit.set_offers(self.root, 'orders', 'staging', opener=self.repository(files)[0], runner=self.sops())
        self.assertEqual((instance / 'helmrelease.yaml').read_text(), before)
        self.assertFalse((instance / 'offers').exists())
        (self.root / 'apps/hello/staging/helmrelease.yaml').write_text(
            (self.root / 'apps/hello/staging/helmrelease.yaml').read_text().replace('tag: 1.27-alpine', 'tag: latest'))
        with self.assertRaisesRegex(edit.EditError, 'only an app published from its own GitHub repository has offers'):
            edit.set_offers(self.root, 'hello', 'staging', opener=self.repository({})[0])

    def test_every_instance_of_an_environment_is_read_and_one_failure_does_not_stop_the_rest(self):
        self.with_units()
        self.built_from_a_repository('orders')
        self.second_app('billing', port=9100)
        self.built_from_a_repository('billing')
        import urllib.error

        def opener(request):
            if '/repos/samclement/billing/' in request.full_url:
                raise urllib.error.HTTPError(request.full_url, 500, 'Server Error', {}, None)
            return b'version: 1\nkind: web\nhealthPath: /healthz\n'
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = edit.main_offers(['--all', 'staging', '--root', str(self.root)], opener=opener)
        self.assertEqual(code, 1)
        self.assertIn('[OK] orders/staging at abc1234 offers nothing', out.getvalue())
        self.assertIn('[ERROR] billing/staging: could not read swhurl.yaml from samclement/billing@abc1234: 500', err.getvalue())
        self.assertNotIn('hello', out.getvalue() + err.getvalue(), 'an image no app repository built is skipped')

    def test_promotion_carries_what_the_promoted_image_offers(self):
        from swhurl.apps import offers
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        manifest = 'version: 1\nkind: web\nhealthPath: /healthz\nprovides: {eventsFile: asyncapi.yaml}\n'
        self.quiet(edit.set_offers, self.root, 'orders', 'staging',
                   opener=self.repository({'swhurl.yaml': manifest, 'asyncapi.yaml': self.ASYNCAPI})[0], runner=runner)
        staging = self.root / 'apps/orders/staging/helmrelease.yaml'
        staging.write_text(staging.read_text().replace(staging.read_text().split('digest: ')[1].split('\n')[0], NEW_DIGEST))
        self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod', runner=runner)
        prod = self.root / 'apps/orders/prod'
        self.assertEqual(offers.read(prod).commit, 'abc1234')
        self.assertEqual((prod / 'offers/asyncapi.yaml').read_text(), self.ASYNCAPI)
        self.assertEqual(self.env('orders', 'prod')['SWHURL_EVENTS_PUBLISH'], 'created,paid', 'no second declaration for production')
        self.assertEqual(len(self.seeds), 2, 'production has its own key')
        self.assertEqual(offers.problems(prod, ['created', 'paid'], 'abc1234'), [])
        self.assertEqual(policy.drift([prod, self.root / 'apps/orders/staging']), [], 'offers follow each image')
        # A first promotion carries them too: production is made from staging's release, then given the documents.
        shutil.rmtree(prod)
        (self.root / 'clusters/home/app-orders-prod.yaml').unlink()
        registry = self.root / 'clusters/home/kustomization.yaml'
        registry.write_text(registry.read_text().replace('  - app-orders-prod.yaml\n', ''))
        with mock.patch.object(policy, 'evaluate', side_effect=lambda instance, runner=None: [
                f'offers: {p}' for p in offers.problems(instance, [], str((yaml.safe_load((instance / 'helmrelease.yaml').read_text())['metadata']
                                                                           .get('annotations') or {}).get(offers.ANNOTATION, '')))
                if 'annotated' in p]):
            self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod', runner=runner)
        self.assertEqual((offers.read(prod).commit, self.env('orders', 'prod')['SWHURL_EVENTS_PUBLISH']), ('abc1234', 'created,paid'))
        release = self.root / 'apps/orders/staging/helmrelease.yaml'
        release.write_text(release.read_text().replace('SWHURL_APP: orders', 'SWHURL_APP: orders\n              SWHURL_API_PATH: /openapi.json', 1))
        self.assertEqual(policy.drift([prod, self.root / 'apps/orders/staging']), [],
                         'staging describes its API before production runs that image')

    def test_event_refusals_change_nothing(self):
        self.with_units()
        before = self.staging.read_text()
        cases = (({}, 'give --publish and/or --subscribe'), ({'publish': ['Created']}, 'lowercase names'),
                 ({'subscribe': ['created']}, '<app>.<event>'), ({'publish': ['a', 'a']}, 'each once'),
                 ({'subscribe': ['orders.created']}, 'which orders/staging does not publish'))
        for kwargs, message in cases:
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.set_events(self.root, 'hello', 'staging', runner=self.sops(), **kwargs)
        self.assertEqual(self.staging.read_text(), before)
        self.assertFalse((self.root / 'apps/hello/staging/events.sops.yaml').exists())
        from swhurl.run import FakeRunner
        with self.assertRaisesRegex(edit.EditError, 'could not SOPS-encrypt'):
            edit.set_events(self.root, 'hello', 'staging', publish=['created'],
                        runner=FakeRunner().on('sops', returncode=1, stderr='no key'))
        self.assertFalse((self.root / 'apps/hello/staging/events.sops.yaml').exists(), 'no plaintext seed is left')
        self.assertEqual(self.staging.read_text(), before)

    def test_expose_refusals(self):
        cases = (('public', None, 'needs --host'), ('public', 'x.homelab.swhurl.com', 'outside'),
                 ('authenticated-web', 'hello.example.com', 'must be under'), ('private', 'x.example.com', 'drop --host'),
                 ('authenticated-web', None, 'already authenticated-web'))
        for exposure, host, message in cases:
            with self.subTest(exposure=exposure, host=host), self.assertRaisesRegex(edit.EditError, message):
                edit.expose(self.root, 'hello', 'staging', exposure, host)

    def test_expose_refuses_a_worker_route(self):
        with redirect_stdout(io.StringIO()):
            new.main(['job', '--env', 'staging', '--kind', 'worker', '--image', 'r/job:1', '--root', str(self.root),
                      '--no-policy-check', '--team', 'demo'])
        with self.assertRaisesRegex(edit.EditError, 'worker'):
            edit.expose(self.root, 'job', 'staging', 'public', 'job.example.com')
    def test_expose_preserves_custom_annotations_dependencies_and_comments(self):
        namespace = self.root / 'apps/hello/staging/namespace.yaml'
        unit = self.root / 'clusters/home/app-hello-staging.yaml'
        namespace.write_text('# namespace notes\n' + namespace.read_text())
        unit.write_text('# Flux notes\n' + unit.read_text().replace('  interval: 10m',
                       '  - name: platform-mongodb  # database readiness\n  interval: 10m'))
        text = self.staging.read_text().replace(
            contract.AUTH_MIDDLEWARE, contract.AUTH_MIDDLEWARE + ',ingress-rate-limit@kubernetescrd')
        text = text.replace('className: traefik', 'className: traefik  # custom route\n        labels: {owner: sam}')
        text = text.replace('secretName: hello-tls', 'secretName: "custom-tls"')
        self.staging.write_text(text)
        self.expose('staging', 'public', 'hello.example.com')
        route = derive.load_release(self.staging.parent)['spec']['values']['ingress']['main']
        self.assertEqual(route['labels'], {'owner': 'sam'})
        self.assertEqual(route['tls'][0]['secretName'], 'custom-tls')
        self.assertEqual(route['annotations']['traefik.ingress.kubernetes.io/router.middlewares'],
                         'ingress-rate-limit@kubernetescrd')
        self.assertIn('className: traefik  # custom route', self.staging.read_text())
        self.assertIn('secretName: "custom-tls"', self.staging.read_text())
        self.assertTrue(namespace.read_text().startswith('# namespace notes\n'))
        self.assertTrue(unit.read_text().startswith('# Flux notes\n'))
        self.assertIn('platform-mongodb  # database readiness', unit.read_text())
        self.expose('staging', 'authenticated-web')
        self.assertIn(contract.AUTH_MIDDLEWARE + ',ingress-rate-limit@kubernetescrd', self.staging.read_text())
        self.assertIn('platform-mongodb  # database readiness', unit.read_text())

    def test_expose_refuses_ambiguous_routes_without_any_writes(self):
        namespace = self.root / 'apps/hello/staging/namespace.yaml'
        unit = self.root / 'clusters/home/app-hello-staging.yaml'
        self.staging.write_text(self.staging.read_text().replace('    ingress:',
                               '    ingress:\n      extra: {enabled: true}'))
        before = {path: path.read_bytes() for path in (self.staging, namespace, unit)}
        with self.assertRaisesRegex(edit.EditError, 'additional ingress routes'):
            edit.expose(self.root, 'hello', 'staging', 'private')
        self.assertEqual({path: path.read_bytes() for path in before}, before)

    def test_expose_refuses_bad_dependencies_without_any_writes(self):
        unit = self.root / 'clusters/home/app-hello-staging.yaml'
        original = unit.read_text()
        for replacement, message in (('  dependsOn: {name: infra-base}\n', 'expected a list'),
                                     ('  dependsOn: &shared\n  - name: infra-base\n', 'uses a YAML anchor')):
            with self.subTest(message=message):
                unit.write_text(original.replace(
                    '  dependsOn:\n  - name: infra-base\n  - name: platform-oauth2-proxy\n', replacement))
                before = {path: path.read_bytes() for path in self.root.rglob('*') if path.is_file()}
                with self.assertRaisesRegex(edit.EditError, message):
                    edit.expose(self.root, 'hello', 'staging', 'public', 'hello.example.com')
                self.assertEqual({path: path.read_bytes() for path in before}, before)
                self.assertEqual({path for path in self.root.rglob('*') if path.is_file()}, set(before))

    def test_scale_refuses_bad_values(self):
        for kwargs, message in (({}, 'at least one'), ({'replicas': 11}, '0 to 10'), ({'replicas': -1}, '0 to 10'),
                                ({'cpu': '1core'}, 'quantity'), ({'memory': '64M'}, 'quantity'),
                                ({'memory_limit': '1Ti'}, 'quantity')):
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.scale(self.root, 'hello', 'prod', **kwargs)

    def test_remove_leaves_no_trace(self):
        reloader = self.root / 'platform/reloader/helmrelease.yaml'
        # Whatever the real list holds today (apps with secrets or events), start from a known one.
        reloader.write_text(re.sub(r'namespaces: \[[^\]]*\]', 'namespaces: [ingress, logging, console, hello-prod]',
                                   reloader.read_text()))
        _, out = self.quiet(edit.remove, self.root, 'hello', 'prod')
        self.assertFalse((self.root / 'apps/hello/prod').exists())
        self.assertTrue((self.root / 'apps/hello/staging').exists())
        self.assertFalse((self.root / 'clusters/home/app-hello-prod.yaml').exists())
        self.assertNotIn('app-hello-prod.yaml', (self.root / 'clusters/home/kustomization.yaml').read_text())
        self.assertIn('app-hello-staging.yaml', (self.root / 'clusters/home/kustomization.yaml').read_text())
        self.assertIn('namespaces: [ingress, logging, console]\n', reloader.read_text())
        self.assertNotIn('[WARN]', out)
        self.quiet(edit.remove, self.root, 'hello', 'staging')
        self.assertFalse((self.root / 'apps/hello').exists())

    def test_remove_warns_when_data_is_kept(self):
        ns = self.root / 'apps/hello/prod/namespace.yaml'
        ns.write_text(ns.read_text().replace('metadata:\n', 'metadata:\n  annotations:\n    kustomize.toolkit.fluxcd.io/prune: disabled\n'))
        _, out = self.quiet(edit.remove, self.root, 'hello', 'prod')
        self.assertIn('[WARN] hello-prod has a retained volume', out)

    def test_unknown_or_invalid_instances(self):
        for app, env in (('nope', 'prod'), ('Hello', 'prod'), ('hello', 'dev'), ('../x', 'prod')):
            with self.subTest(app=app, env=env), self.assertRaises(edit.EditError):
                edit.remove(self.root, app, env)
        self.assertTrue((self.root / 'apps/hello/prod').exists())

    def test_promote_asks_what_changed_only_when_staging_is_behind(self):
        """Behind main by a commit: the caller's word, else this checkout's history, says whether it changed staging."""
        self.staging.write_text(self.staging.read_text().replace('tag: 1.27-alpine', 'tag: 1.28-alpine')
                                .replace(self.staging.read_text().split('digest: ')[1].split('\n')[0], NEW_DIGEST))
        image = promotion.decide(*promotion.read(self.root, 'hello')).image
        behind = ops.InstanceStatus(Instance('hello', 'staging'), 'main@sha1:def', 'main@sha1:abc', ('True', ''), ('True', ''),
                                    image, [image.split(':')[0] + '@' + NEW_DIGEST], [ops.Replicas('Deployment/hello', 1, 1)], [], [], [])
        diff = ('git', '-C', str(self.root.resolve()), 'diff', '--name-only', 'abc', 'def')

        def refusal(argv=(), git=None, history=True):
            (self.root / '.git').mkdir(exist_ok=True) if history else None
            runner = FakeRunner()
            if git is not None:
                runner.on('git', **git)
            with mock.patch.object(ops, 'gather_status', return_value=behind), mock.patch.object(edit, 'promote', return_value=None), \
                    redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as err:
                edit.main_promote(['hello', '--root', str(self.root), *argv], runner=runner)
            return err.getvalue(), runner.calls
        self.assertEqual(refusal(history=False)[1], [], 'a tree without history is not asked')
        self.assertIn('Flux is still applying', refusal(history=False)[0])
        self.assertEqual(refusal(['--source-unchanged=abc..def'], history=False), ('', []))
        self.assertIn('Flux is still applying', refusal(['--source-unchanged=abc..other'], history=False)[0], 'a word about other commits')
        self.assertEqual(refusal(git={'stdout': 'docs/apps.md\napps/hello/prod/helmrelease.yaml\n'}), ('', [diff]))
        self.assertIn('a change to staging is in Git', refusal(git={'stdout': 'apps/hello/staging/helmrelease.yaml\n'})[0])
        self.assertIn('Flux is still applying', refusal(git={'returncode': 128, 'stderr': 'bad object def'})[0], 'a commit not fetched here')

    def test_cli_refusals_exit_2(self):
        code, _ = self.quiet(edit.main_scale, ['hello', 'prod', '--root', str(self.root)])
        self.assertEqual(code, 2)
        code, _ = self.quiet(edit.main_promote, ['hello', '--root', str(self.root)])
        self.assertEqual(code, 2)


if __name__ == '__main__':
    unittest.main()
