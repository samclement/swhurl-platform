"""app-promote, app-scale, app-remove: Git edits of generated instances, offline on a copy of the sample instance."""
import dataclasses
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

from swhurl import ROOT, interface
from swhurl.apps import calling, cli, contract, edit, new, ops, policy, promotion
from swhurl.apps import release as intake
from swhurl.apps.instance import Instance
from swhurl.apps.model import generated
from swhurl.apps.model import write as model_sync
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
        reloader = self.root / 'platform/reloader/helmrelease.yaml'
        reloader.write_text(re.sub(r'namespaces: \[[^\]]*\]', 'namespaces: [ingress, logging, console]', reloader.read_text()))
        self.staging = self.root / 'apps/hello/staging/helmrelease.yaml'
        self.prod = self.root / 'apps/hello/prod/helmrelease.yaml'

    def quiet(self, fn, *args, **kwargs):
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()):
            result = fn(*args, **kwargs)
        return result, out.getvalue()

    def new_staging_image(self):
        """Staging runs 1.28-alpine: its Release record, and what it renders."""
        old_digest = self.staging.read_text().split('digest: ')[1].split('\n')[0]
        for path in (self.staging, self.staging.parent / 'release.yaml'):
            path.write_text(path.read_text().replace('tag: 1.27-alpine', 'tag: 1.28-alpine').replace(old_digest, NEW_DIGEST))

    def test_promote_copies_only_the_image(self):
        self.new_staging_image()
        before = yaml.safe_load(self.prod.read_text())
        self.quiet(edit.promote, self.root, 'hello', 'staging', 'prod')
        before['spec']['values']['controllers']['main']['containers']['main']['image'].update(tag='1.28-alpine', digest=NEW_DIGEST)
        after = yaml.safe_load(self.prod.read_text())
        self.assertNotEqual(after['metadata']['annotations'].pop('platform.swhurl.com/render'),
                            before['metadata']['annotations'].pop('platform.swhurl.com/render'))
        self.assertEqual(after, before)

    def test_a_promotion_carries_a_new_port_to_production_and_its_callers(self):
        """Release B listens on 9090, A on 8080: staging renders B, production keeps A until the
        promotion copies B's release.yaml."""
        self.with_units()
        for env in ('staging', 'prod'):
            self.quiet(edit.set_allows, self.root, 'hello', env, add=['orders'])
            self.quiet(edit.set_calls, self.root, 'orders', env, add=['hello'])
        self.new_staging_image()
        record = self.staging.parent / 'release.yaml'
        record.write_text(record.read_text().replace('port: 8080', 'port: 9090'))
        model_sync.apply(self.root, model_sync.prepare(self.root, {}))

        def listens(env):
            release = yaml.safe_load((self.root / 'apps/hello' / env / 'helmrelease.yaml').read_text())['spec']['values']
            probes = release['controllers']['main']['containers']['main']['probes']
            network = yaml.safe_load((self.root / 'apps/hello' / env / contract.NETWORK_POLICY_FILE).read_text())
            return {release['service']['main']['ports']['http']['port'],
                    *(probe['spec']['httpGet']['port'] for probe in probes.values()),
                    *(port['port'] for rule in network['spec']['ingress'] for port in rule['ports']),
                    int(self.env('orders', env)['SWHURL_SERVICE_HELLO_URL'].rsplit(':', 1)[1])}
        self.assertEqual((listens('staging'), listens('prod')), ({9090}, {8080}))
        self.assertEqual(generated.problems(self.root, only=frozenset(model_sync.read(self.root)[0])), [], 'staging may run a newer release')

        self.quiet(edit.promote, self.root, 'hello', 'staging', 'prod')
        self.assertEqual(listens('prod'), {9090})
        self.assertIn('tag: 1.28-alpine', self.prod.read_text(), 'in the same commit as the image')
        self.assertEqual(yaml.safe_load((self.prod.parent / 'release.yaml').read_text()), yaml.safe_load(record.read_text()))
        self.assertEqual(generated.problems(self.root, only=frozenset(model_sync.read(self.root)[0])), [])
        instances = [self.root / 'apps' / app / env for app in ('hello', 'orders') for env in ('staging', 'prod')]
        self.assertEqual(generated.problems(self.root, only=frozenset(Instance(p.parent.name, p.name) for p in instances)), [])

    def test_promote_refusals(self):
        with self.assertRaisesRegex(edit.EditError, 'already runs'):
            edit.promote(self.root, 'hello', 'staging', 'prod')
        with self.assertRaisesRegex(edit.EditError, 'must differ'):
            edit.promote(self.root, 'hello', 'prod', 'prod')
        record = self.staging.parent / 'release.yaml'
        original = record.read_text()
        record.write_text(original.replace('docker.io/nginxinc/nginx-unprivileged', 'ghcr.io/other/image'))
        with self.assertRaisesRegex(edit.EditError, 'change the repository by hand'):
            edit.promote(self.root, 'hello', 'staging', 'prod')
        record.write_text('\n'.join(line for line in original.splitlines() if 'digest:' not in line) + '\n')
        with self.assertRaisesRegex(edit.EditError, 'no image digest'):
            edit.promote(self.root, 'hello', 'staging', 'prod')

    def test_promotion_refuses_a_source_that_moved_after_review_without_writing(self):
        reviewed = ops.image_reference(yaml.safe_load(self.staging.read_text())['spec']['values']['controllers']['main']['containers']['main']['image'])
        self.new_staging_image()
        before = self.prod.read_bytes()
        with self.assertRaisesRegex(edit.EditError, 'changed since it was reviewed'):
            edit.promote(self.root, 'hello', 'staging', 'prod', expect_image=reviewed)
        self.assertEqual(self.prod.read_bytes(), before)
        current = ops.image_reference(yaml.safe_load(self.staging.read_text())['spec']['values']['controllers']['main']['containers']['main']['image'])
        self.quiet(edit.promote, self.root, 'hello', 'staging', 'prod', expect_image=current)
        self.assertIn('tag: 1.28-alpine', self.prod.read_text())

    def test_update_promotion_handles_malformed_production_output_without_writing(self):
        from swhurl.console import promotion as console_promotion

        self.new_staging_image()
        self.prod.write_text('invalid: [')
        world, _ = model_sync.read(self.root)
        decision = promotion.select(self.root, world, 'hello')
        self.assertEqual(decision.outcome, 'update', 'exercise the real update guard, not the same-image short circuit')
        before = self.snapshot()
        problem = 'apps/hello/prod/helmrelease.yaml: cannot read committed YAML'
        with self.assertRaisesRegex(edit.EditError, problem):
            edit.promote(self.root, 'hello')
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as err:
            code = cli.run(lambda: edit.promote(self.root, 'hello'), False)
        self.assertEqual(code, 2)
        self.assertIn(problem, err.getvalue())
        self.assertEqual(self.snapshot(), before)
        status = ops.InstanceStatus(Instance('hello', 'staging'), 'main@sha1:abc', 'main@sha1:abc',
                                    ('True', ''), ('True', ''), decision.image,
                                    ['docker.io/nginxinc/nginx-unprivileged@' + NEW_DIGEST],
                                    [ops.Replicas('Deployment/hello', 1, 1)], [], [], [])
        reviewed = console_promotion.review(self.root, 'hello', status, FakeRunner())
        self.assertEqual(reviewed['outcome'], 'update')
        self.assertIn(problem, reviewed['diff_problem'])
        self.assertEqual(reviewed['diff_files'], 0)
        self.assertEqual(self.snapshot(), before)

    def test_cli_promotion_checks_the_expected_image(self):
        before = self.prod.read_bytes()
        code, _ = self.quiet(cli.main_promote, ['hello', '--root', str(self.root), '--expect-image=x:1'])
        self.assertEqual(code, 2)
        self.assertEqual(self.prod.read_bytes(), before)

    def test_a_command_refuses_files_the_model_cannot_express_without_writing(self):
        unit = self.root / 'clusters/home/app-hello-staging.yaml'
        route = self.staging.read_text().replace('className: traefik', 'className: traefik\n        labels: {owner: sam}')
        cases = ((self.staging, route, lambda: edit.scale(self.root, 'hello', 'staging', memory_limit='256Mi'), 'labels'),
                 (self.staging, self.staging.read_text().replace('    ingress:', '    ingress:\n      extra: {enabled: true}'),
                  lambda: edit.expose(self.root, 'hello', 'staging', 'private'), 'ingress.extra'),
                 (unit, unit.read_text().replace('  interval: 10m', '  - name: platform-mongodb\n  interval: 10m'),
                  lambda: edit.set_team(self.root, 'hello', 'platform'), 'dependsOn'),
                 (self.prod, self.prod.read_text() + '\n---\nextra: document\n',
                  lambda: edit.scale(self.root, 'hello', 'prod', memory_limit='256Mi'), 'differ from what the platform renders'))
        (self.root / 'apps/teams.yaml').write_text('teams:\n  platform: {}\n')
        for path, text, command, message in cases:
            with self.subTest(message=message):
                original = path.read_text()
                path.write_text(text)
                before = {file: file.read_bytes() for file in self.root.rglob('*') if file.is_file()}
                with self.assertRaisesRegex(edit.EditError, message):
                    command()
                self.assertEqual({file: file.read_bytes() for file in self.root.rglob('*') if file.is_file()}, before)
                path.write_text(original)

    def test_requested_policy_failure_is_nonzero_after_edit(self):
        with mock.patch.object(new.policy, 'evaluate', side_effect=CommandError(['helm'], 'helm unavailable')):
            code, out = self.quiet(cli.main_scale, ['hello', 'prod', '--memory-limit=256Mi', '--root', str(self.root)])
        self.assertEqual(code, 1)
        self.assertIn('Files remain for review', out)
        self.assertNotIn('Next: commit', out)
        self.assertIn('memory: 256Mi', self.prod.read_text())

    def test_scale_changes_only_what_was_asked(self):
        before = yaml.safe_load(self.prod.read_text())
        self.quiet(edit.scale, self.root, 'hello', 'prod', replicas=2, memory_limit='256Mi')
        main = before['spec']['values']['controllers']['main']
        main['replicas'] = 2
        main['containers']['main']['resources']['limits']['memory'] = '256Mi'
        after = yaml.safe_load(self.prod.read_text())
        self.assertNotEqual(after['metadata']['annotations'].pop('platform.swhurl.com/render'),
                            before['metadata']['annotations'].pop('platform.swhurl.com/render'))
        self.assertEqual(after, before)
        with self.assertRaisesRegex(edit.EditError, 'already has'):
            edit.scale(self.root, 'hello', 'prod', replicas=2)
        with self.assertRaisesRegex(edit.EditError, r"below the platform's default resources: --cpu 5m \(at least 10m\)"):
            edit.scale(self.root, 'hello', 'prod', cpu='5m')

    def expose(self, env, exposure, host=None):
        return self.quiet(edit.expose, self.root, 'hello', env, exposure, host)

    def state(self, env):
        instance = self.root / 'apps/hello' / env
        values = yaml.safe_load((instance / 'helmrelease.yaml').read_text())['spec']['values']
        route = (values.get('ingress') or {}).get('main')
        namespace = yaml.safe_load((instance / 'namespace.yaml').read_text())
        unit = yaml.safe_load((self.root / f'clusters/home/app-hello-{env}.yaml').read_text())
        return {'exposure': namespace['metadata']['labels'][interface.EXPOSURE],
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
        self.expose('staging', 'public', 'hello.example.com')
        self.assertEqual(generated.problems(self.root), [], 'exposure may differ between environments')

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
            self.assertEqual(cli.main_alerts(['hello', '--errors', 'none', '--error-logs', '9', '--root', str(self.root)]), 0)
        self.assertEqual(self.alert_settings('prod'), 'error-logs=9,errors=none')

    def test_team_moves_every_environment(self):
        (self.root / 'apps/teams.yaml').write_text('teams:\n  platform: {}\n  payments: {}\n')
        self.quiet(edit.set_team, self.root, 'hello', 'payments')
        for env in ('staging', 'prod'):
            instance = self.root / 'apps/hello' / env
            labels = yaml.safe_load((instance / 'namespace.yaml').read_text())['metadata']['labels']
            self.assertEqual(labels[interface.TEAM], 'payments')
        self.assertEqual(generated.problems(self.root), [])
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
            shutil.copy(self.root / 'apps/hello/app.yaml', self.root / 'apps' / name / 'app.yaml')
            for path in (self.root / 'apps' / name / env).glob('*.yaml'):
                path.write_text(path.read_text().replace('hello', name).replace('port: 8080', f'port: {port}'))
        model_sync.apply(self.root, model_sync.prepare(self.root, {}))

    def test_calls_give_the_caller_an_address_and_token_and_the_callee_an_allowed_caller(self):
        self.second_app()
        self.quiet(edit.set_allows, self.root, 'orders', 'staging', add=['hello'])
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
        self.assertEqual(generated.problems(self.root, only=frozenset(Instance(p.parent.name, p.name) for p in instances)), [])
        self.assertEqual(generated.problems(self.root), [], 'calls may differ between environments')
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
        cases = (({'add': ['hello']}, 'does not declare calls to itself'), ({'add': ['Bad']}, 'malformed calls'),
                 ({'remove': ['orders']}, 'does not call orders'), ({}, 'give --add and/or --remove'))
        for kwargs, message in cases:
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.set_calls(self.root, 'hello', 'staging', **kwargs)
        self.assertEqual(self.staging.read_text(), before)
        self.quiet(edit.set_calls, self.root, 'hello', 'staging', add=['orders'])
        with self.assertRaisesRegex(edit.EditError, 'already calls orders'):
            edit.set_calls(self.root, 'hello', 'staging', add=['orders'])

    def test_allows_refusals_change_nothing_and_a_call_waits_for_its_grant(self):
        self.second_app()
        before = (self.root / 'apps/orders/staging/instance.yaml').read_text()
        cases = (({'add': ['orders']}, 'does not allow itself'), ({'add': ['team:nobody']}, "unknown team 'nobody'"),
                 ({'add': ['*']}, 'not an app name or team:<team>'), ({'remove': ['hello']}, 'does not allow hello'),
                 ({}, 'give --add and/or --remove'))
        for kwargs, message in cases:
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.set_allows(self.root, 'orders', 'staging', **kwargs)
        self.assertEqual((self.root / 'apps/orders/staging/instance.yaml').read_text(), before)
        out = self.quiet(edit.set_calls, self.root, 'hello', 'staging', add=['orders'])
        self.assertIn('hello -> orders waits for orders to allow it', str(out))
        self.assertNotIn('SWHURL_SERVICE_ORDERS_URL', self.env('hello'), 'an ungranted call connects nothing')
        self.assertNotIn('SWHURL_ALLOWED_CALLERS', self.env('orders'))
        self.quiet(edit.set_allows, self.root, 'orders', 'staging', add=['hello'])
        self.assertIn('SWHURL_SERVICE_ORDERS_URL', self.env('hello'), 'the grant connects it')
        self.assertEqual(self.env('orders')['SWHURL_ALLOWED_CALLERS'], 'hello')
        with self.assertRaisesRegex(edit.EditError, 'already allows hello'):
            edit.set_allows(self.root, 'orders', 'staging', add=['hello'])

    def test_one_sided_calls_are_policy_violations(self):
        self.second_app()
        self.quiet(edit.set_allows, self.root, 'orders', 'staging', add=['hello'])
        self.quiet(edit.set_calls, self.root, 'hello', 'staging', add=['orders'])
        orders = self.root / 'apps/orders/staging/helmrelease.yaml'
        orders.write_text(orders.read_text().replace('SWHURL_ALLOWED_CALLERS: hello', 'SWHURL_ALLOWED_CALLERS: stranger'))
        instances = [self.root / 'apps/hello/staging', self.root / 'apps/orders/staging']
        problems = generated.problems(self.root)
        self.assertTrue(any('SWHURL_ALLOWED_CALLERS' in p and 'stranger' in p for p in problems), problems)
        model_sync.apply(self.root, model_sync.prepare(self.root, {}))
        self.assertEqual(generated.problems(self.root, only=frozenset(Instance(p.parent.name, p.name) for p in instances)), [])

    def test_signing_keys_refresh_rewrites_the_file_and_every_web_app(self):
        from swhurl.apps import keys
        from swhurl.run import FakeRunner
        (self.root / 'apps/signing-keys.json').write_text('{"keys": [{"kid": "old"}]}')
        model_sync.apply(self.root, model_sync.prepare(self.root, {}))
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

        def encrypt(args, given):
            self.assertEqual(args[-1], '/dev/stdin')
            self.seeds.append(yaml.safe_load(given)['stringData']['SWHURL_EVENTS_SEED'])
            return Result(args, 0, given.replace(self.seeds[-1], 'ENC[...]') + 'sops:\n  age: []\n')
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

    def publish_events(self, app, names, runner, env='staging'):
        """Intake a document for a fixture image: publications have no manual edit command."""
        from swhurl.apps import offers
        text = yaml.safe_dump({
            'asyncapi': '3.0.0', 'info': {'title': app, 'version': '1.0.0'},
            'channels': {name: {'address': name, 'messages': {'message': {
                'payload': {'type': 'object'}, 'examples': [{'payload': {}}]}}} for name in names},
            'operations': {f'send_{name}': {'action': 'send', 'channel': {'$ref': f'#/channels/{name}'}} for name in names},
        }, sort_keys=False)
        return self.quiet(intake.write_offers, self.root, app, env, f'samclement/{app}', 'abc1234',
                          {offers.EVENTS_NAME: text}, offers.events(text), runner=runner)

    def test_events_give_a_publisher_a_stream_and_a_subscriber_a_consumer_and_both_a_nats_user(self):
        from swhurl.apps import events
        self.with_units()
        runner = self.sops()
        self.publish_events('orders', ['created', 'cancelled'], runner)
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
        self.publish_events('orders', ['created', 'cancelled', 'paid'], runner)
        self.assertEqual(len(self.seeds), 2)

        stream, = yaml.safe_load_all((self.root / 'apps/orders/staging/events.yaml').read_text())
        self.assertTrue(stream['spec']['preventDelete'], 'stopping publication must not delete retained messages')
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
        instances = [self.root / 'apps' / app / 'staging' for app in ('hello', 'orders')]
        self.assertEqual(generated.problems(self.root, only=frozenset(Instance(p.parent.name, p.name) for p in instances)), [])
        self.assertCountEqual(generated.problems(self.root), [  # every instance needs its own key
            f'{app}/prod: has no NATS key; make app-sync creates it' for app in ('hello', 'orders')])

        with self.assertRaisesRegex(edit.EditError, 'publishes events that hello subscribe to'):
            edit.remove(self.root, 'orders', 'staging')
        with self.assertRaisesRegex(edit.EditError, 'subscribes to orders.created, which orders/staging does not publish'):
            self.publish_events('orders', ['paid'], runner)
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
        self.quiet(edit.sync, self.root, runner=runner)
        self.assertEqual(len(runner.calls), 4)
        self.assertEqual(model_sync.sync(self.root, runner=FakeRunner()), [], 'an instance keeps its key')
        self.assertEqual(generated.problems(self.root), [])
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
        self.publish_events('orders', ['created'], runner)
        self.publish_events('orders', ['created', 'paid'], runner)
        self.quiet(edit.set_events, self.root, 'hello', 'staging', add_subscribe=['orders.created'], runner=runner)
        self.quiet(edit.set_events, self.root, 'hello', 'staging', add_subscribe=['orders.paid'],
                   remove_subscribe=['orders.created'], runner=runner)
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_PUBLISH'], 'created,paid', 'what was there stays')
        self.assertEqual(self.env('hello')['SWHURL_EVENTS_SUBSCRIBE'], 'orders.paid')
        cases = (('hello', {'add_subscribe': ['orders.paid']}, 'already there: orders.paid'),
                 ('hello', {'remove_subscribe': ['orders.refunded']}, 'not there to remove: orders.refunded'),
                 ('hello', {'subscribe': [], 'add_subscribe': ['orders.paid']}, '--subscribe replaces the list'),
                 ('hello', {'add_subscribe': ['orders.refunded']}, 'which orders/staging does not publish'))
        for app, kwargs, message in cases:
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.set_events(self.root, app, 'staging', runner=runner, **kwargs)
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_PUBLISH'], 'created,paid', 'a refused change is undone')
        self.publish_events('orders', ['paid'], runner)
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
            for name in ('helmrelease.yaml', 'release.yaml'):  # the record, and what it renders
                path = self.root / 'apps' / app / env / name
                path.write_text(path.read_text().replace('docker.io/nginxinc/nginx-unprivileged', f'ghcr.io/samclement/{app}')
                                .replace('tag: 1.27-alpine', f'tag: {tag}' if env == 'staging' else 'tag: 6-0000000'))
        model_sync.apply(self.root, model_sync.prepare(self.root, {}))  # a platform image's staging follows new images

    def test_offers_are_read_at_the_images_commit_and_say_what_the_app_publishes(self):
        from swhurl.apps import offers
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        manifest = 'version: 1\nkind: web\nhealthPath: /healthz\nprovides: {api: /openapi.json, apiFile: openapi.json, eventsFile: asyncapi.yaml}\n'
        api = '{"openapi": "3.1.0", "info": {"title": "orders", "version": "0.1.0"}, "paths": {"/": {}}}\n'
        opener, seen = self.repository({'swhurl.yaml': manifest, 'openapi.json': api, 'asyncapi.yaml': self.ASYNCAPI})
        changed, _ = self.quiet(intake.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)
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
        self.assertEqual(release['metadata']['annotations'][interface.OFFERS_ANNOTATION], 'abc1234')
        self.assertNotIn('offers', yaml.safe_load((instance / 'kustomization.yaml').read_text())['resources'], 'documents, not resources')
        self.assertEqual(release['metadata']['annotations'][interface.API_ANNOTATION], '/openapi.json',
                         'where it serves its description, from swhurl.yaml: an annotation, since no app reads it')
        self.assertNotIn('SWHURL_API_PATH', self.env('orders'))
        self.assertEqual(offers.problems(instance, ['created', 'paid'], 'abc1234'), [])
        self.assertIn('describes created, paid', offers.problems(instance, ['created'], 'abc1234')[0])
        self.assertIn('the release says its offers are from 0000000', offers.problems(instance, ['created', 'paid'], '0000000')[0])
        self.assertEqual(self.quiet(intake.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)[0], False,
                         'reading the same commit again changes nothing')

        # Another app subscribes; a later image that drops the event is refused and nothing changes.
        self.quiet(edit.set_events, self.root, 'hello', 'staging', add_subscribe=['orders.paid'], runner=runner)
        dropped = self.ASYNCAPI.replace("  sendPaid: {action: send, channel: {$ref: '#/channels/paid'}}\n", '')
        opener, _ = self.repository({'swhurl.yaml': manifest, 'openapi.json': api, 'asyncapi.yaml': dropped})
        before = {p: p.read_text() for p in instance.rglob('*') if p.is_file()}
        with self.assertRaisesRegex(edit.EditError, 'hello/staging subscribes to orders.paid, which orders/staging does not publish'):
            intake.set_offers(self.root, 'orders', 'staging', opener=opener, runner=runner)
        self.assertEqual({p: p.read_text() for p in instance.rglob('*') if p.is_file()}, before)

        # Removing the document cannot bypass dependencies; a refused read changes nothing.
        opener, _ = self.repository({'swhurl.yaml': 'version: 1\nkind: web\nhealthPath: /healthz\n'})
        with self.assertRaisesRegex(edit.EditError, 'hello/staging subscribes to orders.paid'):
            intake.set_offers(self.root, 'orders', 'staging', opener=opener, runner=runner)
        self.assertEqual({p: p.read_text() for p in instance.rglob('*') if p.is_file()}, before)
        self.quiet(edit.set_events, self.root, 'hello', 'staging', subscribe=[], runner=runner)
        self.quiet(intake.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)
        self.assertFalse((instance / 'offers').exists())
        self.assertNotIn(interface.API_ANNOTATION, yaml.safe_load((instance / 'helmrelease.yaml').read_text())['metadata'].get('annotations', {}),
                         'no provides.api: no API description to link')
        self.assertNotIn('platform.swhurl.com/offers', yaml.safe_load((instance / 'helmrelease.yaml').read_text())['metadata'].get('annotations', {}))
        self.assertNotIn('SWHURL_EVENTS_PUBLISH', self.env('orders'))
        self.assertFalse((instance / 'events.yaml').exists(), 'the protected Stream CR is removed, not the broker history')
        self.assertIn('SWHURL_EVENTS_NKEY', self.env('orders'), 'its flags identity remains')
        users = yaml.safe_load(yaml.safe_load((self.root / 'platform/nats/users.yaml').read_text())['data']['values.yaml'])
        user = next(u for u in users['config']['merge']['authorization']['users'] if u['nkey'] == self.env('orders')['SWHURL_EVENTS_NKEY'])
        self.assertNotIn('staging.orders.>', user['permissions']['publish']['allow'])
        self.assertFalse(self.quiet(intake.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)[0])

    def test_stopping_publications_keeps_subscriptions_and_checks_production_on_promotion(self):
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        self.publish_events('orders', ['created'], runner)
        self.publish_events('orders', ['created'], runner, env='prod')
        self.publish_events('hello', ['seen'], runner)
        self.quiet(edit.set_events, self.root, 'orders', 'staging', subscribe=['hello.seen'], runner=runner)
        self.quiet(edit.set_events, self.root, 'hello', 'prod', subscribe=['orders.created'], runner=runner)
        folder = self.root / 'apps/orders/staging'
        stream = next(d for d in yaml.safe_load_all((folder / 'events.yaml').read_text()) if d['kind'] == 'Stream')
        self.assertTrue(stream['spec']['preventDelete'], 'protection is present before the Stream CR is removed')
        key = self.env('orders')['SWHURL_EVENTS_NKEY']
        self.publish_events('orders', [], runner)
        self.assertNotIn('SWHURL_EVENTS_PUBLISH', self.env('orders'))
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_SUBSCRIBE'], 'hello.seen')
        self.assertEqual(self.env('orders')['SWHURL_EVENTS_NKEY'], key)
        self.assertEqual([d['kind'] for d in yaml.safe_load_all((folder / 'events.yaml').read_text())], ['Consumer'])
        # A different image with no publications can be valid in staging but break production.
        for name in ('helmrelease.yaml', 'release.yaml'):
            path = folder / name
            path.write_text(path.read_text().replace(path.read_text().split('digest: ')[1].split('\n')[0], NEW_DIGEST))
        before = {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        with self.assertRaisesRegex(edit.EditError, 'hello/prod subscribes to orders.created'):
            edit.promote(self.root, 'orders', runner=runner)
        self.assertEqual({p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}, before)
        self.quiet(edit.set_events, self.root, 'hello', 'prod', subscribe=[], runner=runner)
        self.quiet(edit.promote, self.root, 'orders', runner=runner)
        self.assertNotIn('SWHURL_EVENTS_PUBLISH', self.env('orders', 'prod'))

    def test_removing_the_last_send_operation_clears_publications_even_with_channels_left(self):
        from swhurl.apps import offers
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        manifest = 'version: 1\nkind: web\nhealthPath: /healthz\nprovides: {eventsFile: asyncapi.yaml}\n'
        document = yaml.safe_load(self.ASYNCAPI)
        for operations in (None, {}, {'receiveCreated': {'action': 'receive', 'channel': {'$ref': '#/channels/created'}}}):
            with self.subTest(operations=operations):
                self.publish_events('orders', ['created'], runner)
                key = self.env('orders')['SWHURL_EVENTS_NKEY']
                document.pop('operations', None)
                if operations is not None:
                    document['operations'] = operations
                text = yaml.safe_dump(document)
                self.assertEqual(offers.events(text), [], 'channels alone never authorize publication')
                opener, _ = self.repository({'swhurl.yaml': manifest, 'asyncapi.yaml': text})
                # Even removing the final send operation must honor existing subscribers.
                self.quiet(edit.set_events, self.root, 'hello', 'staging', subscribe=['orders.created'], runner=runner)
                before = {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
                with self.assertRaisesRegex(edit.EditError, 'hello/staging subscribes to orders.created'):
                    intake.set_offers(self.root, 'orders', 'staging', opener=opener, runner=runner)
                self.assertEqual({p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}, before)
                self.quiet(edit.set_events, self.root, 'hello', 'staging', subscribe=[], runner=runner)
                self.assertTrue(self.quiet(intake.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)[0])
                self.assertNotIn('SWHURL_EVENTS_PUBLISH', self.env('orders'))
                self.assertEqual(self.env('orders')['SWHURL_EVENTS_NKEY'], key)
                self.assertFalse((self.root / 'apps/orders/staging/events.yaml').exists())
                self.assertFalse(self.quiet(intake.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)[0])

    def test_invalid_event_operations_preserve_the_whole_tree(self):
        from swhurl.apps import offers
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        self.publish_events('orders', ['created'], runner)
        before = {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        manifest = 'version: 1\nkind: web\nhealthPath: /healthz\nprovides: {eventsFile: asyncapi.yaml}\n'
        cases = (
            ([], 'operations must be a mapping'),
            (None, 'operations must be a mapping'),
            ({'broken': 'send'}, 'operation broken must be a mapping'),
            ({'broken': {}}, 'action must be send or receive'),
            ({'broken': {'action': 'snd', 'channel': {'$ref': '#/channels/created'}}}, 'action must be send or receive'),
            ({'broken': {'action': 'send', 'channel': {'$ref': '#/channels/missing'}}}, 'channel must reference an existing'),
            ({'broken': {'action': 'receive', 'channel': {'$ref': '#/channels/missing'}}}, 'channel must reference an existing'),
            ({'broken': {'action': 'send', 'channel': 'created'}}, 'channel must reference an existing'),
            ({'broken': {'action': 'send'}}, 'channel must reference an existing'),
            ({'broken': {'$ref': '#/components/operations/missing'}}, 'points at nothing'),
        )
        for operations, message in cases:
            with self.subTest(operations=operations):
                document = yaml.safe_load(self.ASYNCAPI)
                document['operations'] = operations
                text = yaml.safe_dump(document)
                with self.assertRaisesRegex(offers.OfferError, message):
                    offers.events(text)
                with self.assertRaisesRegex(edit.EditError, message):
                    intake.set_offers(self.root, 'orders', 'staging',
                                      opener=self.repository({'swhurl.yaml': manifest, 'asyncapi.yaml': text})[0], runner=runner)
                self.assertEqual({p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}, before)

    def test_failed_event_document_reads_preserve_an_existing_release_and_shared_configuration(self):
        import urllib.error
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        self.publish_events('orders', ['created'], runner)
        before = {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        manifest = 'version: 1\nkind: web\nhealthPath: /healthz\nprovides: {eventsFile: asyncapi.yaml}\n'
        for files in ({'swhurl.yaml': manifest}, {'swhurl.yaml': manifest, 'asyncapi.yaml': 'not: [valid'}):
            with self.subTest(files=files), self.assertRaises(edit.EditError):
                intake.set_offers(self.root, 'orders', 'staging', opener=self.repository(files)[0], runner=runner)
            self.assertEqual({p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}, before)

        def unreachable(request):
            if '/swhurl.yaml?' in request.full_url:
                return manifest.encode()
            raise urllib.error.URLError('offline')
        with self.assertRaisesRegex(edit.EditError, 'could not reach GitHub'):
            intake.set_offers(self.root, 'orders', 'staging', opener=unreachable, runner=runner)
        self.assertEqual({p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}, before)

    def test_policy_rejects_publications_without_an_events_document(self):
        from swhurl.apps import offers
        folder = self.staging.parent
        self.assertIn('provides.eventsFile', offers.problems(folder, ['created'])[0])
        self.with_units()
        self.quiet(intake.write_offers, self.root, 'orders', 'staging', 'samclement/orders', 'abc1234',
                   {offers.FLAGS_NAME: '{"shout": {"type": "boolean", "default": false}}'}, None)
        folder = self.root / 'apps/orders/staging'
        self.assertIn('provides.eventsFile', offers.problems(folder, ['created'], 'abc1234')[0])
        self.assertEqual(offers.problems(folder, [], 'abc1234'), [])

    def test_obsolete_publication_flags_are_refused_without_writes(self):
        before = {p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}
        for flag in ('--publish=created', '--add-publish=created', '--remove-publish=created'):
            error = io.StringIO()
            with self.subTest(flag=flag), redirect_stderr(error), self.assertRaises(SystemExit) as raised:
                cli.main_events(['hello', 'staging', '--root', str(self.root), flag])
            self.assertEqual(raised.exception.code, 2)
            self.assertIn('provides.eventsFile', error.getvalue())
        with redirect_stderr(io.StringIO()):
            result, _ = self.quiet(new.main, ['hello', '--root', str(self.root), '--publish=created'])
        self.assertEqual(result, 2)
        self.assertEqual({p: p.read_bytes() for p in self.root.rglob('*') if p.is_file()}, before)

    def test_declared_flags_are_kept_with_the_images_commit_and_carried_by_promotion(self):
        import json

        from swhurl.apps import offers
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        manifest = ('version: 1\nkind: web\nhealthPath: /healthz\n'
                    'flags: {shout: {type: boolean, description: Say hello in capitals, default: false, staging: true}}\n')
        opener, _ = self.repository({'swhurl.yaml': manifest})
        self.quiet(intake.set_offers, self.root, 'orders', 'staging', opener=opener, runner=runner)
        instance = self.root / 'apps/orders/staging'
        self.assertEqual({p.name for p in (instance / 'offers').iterdir()}, {'source.yaml', 'flags.json'})
        found = offers.read(instance)
        self.assertEqual((found.api, found.events, found.flags), ('', '', 'flags.json'), 'not mistaken for an API description')
        self.assertEqual(found.declared, {'shout': {'type': 'boolean', 'description': 'Say hello in capitals', 'default': False, 'staging': True}})
        self.assertEqual(offers.problems(instance, [], 'abc1234'), [])
        staging = instance / 'helmrelease.yaml'
        record = staging.parent / 'release.yaml'
        record.write_text(record.read_text().replace(yaml.safe_load(record.read_text())['image']['digest'], NEW_DIGEST))
        model_sync.apply(self.root, model_sync.prepare(self.root, {}))
        self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod', runner=runner)
        self.assertEqual(offers.read(self.root / 'apps/orders/prod').declared, found.declared, 'production gets the image\'s flags')
        (instance / 'offers/flags.json').write_text(json.dumps({'shout': {'type': 'boolean', 'default': 'yes'}}))
        self.assertIn("flags.shout.default must be a boolean, not 'yes'", offers.problems(instance, [], 'abc1234')[0])

    def test_an_events_document_with_no_events_is_accepted(self):
        """An app that publishes nothing keeps its template's asyncapi.yaml with no channels."""
        from swhurl.apps import offers
        self.with_units()
        self.built_from_a_repository()
        manifest = 'version: 1\nkind: web\nhealthPath: /healthz\nprovides: {eventsFile: asyncapi.yaml}\n'
        empty = 'asyncapi: 3.0.0\ninfo: {title: orders, version: 0.1.0}\nchannels: {}\n'
        opener, _ = self.repository({'swhurl.yaml': manifest, 'asyncapi.yaml': empty})
        changed, _ = self.quiet(intake.set_offers, self.root, 'orders', 'staging', opener=opener, runner=self.sops())
        self.assertTrue(changed)
        self.assertEqual(tuple(offers.read(self.root / 'apps/orders/staging').described), ())
        self.assertFalse(self.env('orders').get('SWHURL_EVENTS_PUBLISH'), 'publishes nothing')

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
                intake.set_offers(self.root, 'orders', 'staging', opener=self.repository(files)[0], runner=self.sops())
        self.assertEqual((instance / 'helmrelease.yaml').read_text(), before)
        self.assertFalse((instance / 'offers').exists())
        (self.root / 'apps/hello/staging/helmrelease.yaml').write_text(
            (self.root / 'apps/hello/staging/helmrelease.yaml').read_text().replace('tag: 1.27-alpine', 'tag: latest'))
        with self.assertRaisesRegex(edit.EditError, 'only an app published from its own GitHub repository has offers'):
            intake.set_offers(self.root, 'hello', 'staging', opener=self.repository({})[0])

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
            code = cli.main_offers(['--all', 'staging', '--root', str(self.root)], opener=opener)
        self.assertEqual(code, 1)
        self.assertIn('[OK] orders/staging at abc1234 offers nothing', out.getvalue())
        self.assertIn('[ERROR] billing/staging: could not read swhurl.yaml from samclement/billing@abc1234: 500', err.getvalue())
        self.assertNotIn('hello', out.getvalue() + err.getvalue(), 'an image no app repository built is skipped')

        # One instance: a change is checked against the app policy, on the instance's folder.
        manifest = b'version: 1\nkind: web\nhealthPath: /healthz\nflags: {shout: {type: boolean, default: false}}\n'
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as err, \
                mock.patch.object(cli, 'check_generated', return_value=0) as checked:
            code = cli.main_offers(['orders', 'staging', '--root', str(self.root)], opener=lambda request: manifest)
        self.assertEqual((code, err.getvalue()), (0, ''))
        checked.assert_called_once_with(self.root.resolve() / 'apps/orders/staging')

    def registry(self, app, tags, files):
        """An opener serving GHCR (a pull token, the tags in pages, a manifest per tag) and the app repository."""
        import json
        from urllib.parse import parse_qs, urlparse
        repository, _ = self.repository(files)

        def opener(request):
            url = urlparse(request.full_url)
            if url.netloc == 'api.github.com':
                return repository(request)
            if url.path == '/token':
                self.assertEqual(parse_qs(url.query)['scope'], [f'repository:samclement/{app}:pull'])
                return b'{"token": "t"}'
            self.assertEqual(request.get_header('Authorization'), 'Bearer t')
            if url.path.endswith('/tags/list'):
                query = parse_qs(url.query)
                start = tags.index(query['last'][0]) + 1 if 'last' in query else 0
                return json.dumps({'tags': tags[start:start + int(query['n'][0])]}).encode()
            self.assertIn('application/vnd.oci.image.index.v1+json', request.get_header('Accept'))
            return f'manifest of {url.path.rsplit("/", 1)[1]}'.encode()
        return opener

    def automatic(self, app='orders'):
        """Staging follows the app's images: rendered for a platform image (built_from_a_repository)."""
        self.assertTrue((self.root / 'apps' / app / 'staging/image-automation.yaml').is_file())

    def test_staging_gets_the_newest_image_and_what_it_offers_in_one_change(self):
        import hashlib

        from swhurl.apps import images, offers
        self.with_units()
        self.built_from_a_repository()
        self.automatic()
        # What the instance already runs with (port, health path, user): orders has production, which
        # a promotion does not yet bring a changed need to.
        manifest = 'version: 1\nkind: web\nport: 9000\nhealthPath: /\nuid: 101\nflags: {shout: {type: boolean, default: false}}\n'
        tags = ['7-abc1234', 'latest', '10-def5678', '9-0000000', 'sha256-x']  # 10 beats 9 by number, not by text
        release = self.root / 'apps/orders/staging/helmrelease.yaml'
        with mock.patch.object(images, 'PAGE', 2):  # three pages
            changed, out = self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry('orders', tags, {'swhurl.yaml': manifest}))
        self.assertTrue(changed)
        self.assertIn('[OK] orders/staging: image 7-abc1234 -> 10-def5678', out)
        image = yaml.safe_load(release.read_text())['spec']['values']['controllers']['main']['containers']['main']['image']
        self.assertEqual((image['tag'], image['digest']), ('10-def5678', 'sha256:' + hashlib.sha256(b'manifest of 10-def5678').hexdigest()),
                         'the digest is the hash of the manifest the registry stores')
        self.assertEqual(offers.read(release.parent).commit, 'def5678', 'offers read at the new image\'s commit')
        changed, out = self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry('orders', tags, {}))
        self.assertFalse(changed, 'nothing newer: nothing changes')
        self.assertIn('its needs and offers were read at def5678', out, 'and the repository is not read again (it would 404 here)')

        # A newer image whose offers are refused is held: staging keeps the image and offers it had.
        before = {p: p.read_bytes() for p in release.parent.rglob('*') if p.is_file()}
        with self.assertRaisesRegex(edit.EditError, 'healthPath is required'):
            edit.deploy_staging(self.root, 'orders', opener=self.registry('orders', [*tags, '11-1111111'], {'swhurl.yaml': 'version: 1\nkind: web\n'}))
        self.assertEqual({p: p.read_bytes() for p in release.parent.rglob('*') if p.is_file()}, before)
        with self.assertRaisesRegex(edit.EditError, 'samclement/orders has no tag matching'):
            edit.deploy_staging(self.root, 'orders', opener=self.registry('orders', ['latest'], {}))

        # Every staging instance with an ImagePolicy; one failure does not stop the rest.
        self.second_app('billing', port=9100)
        self.built_from_a_repository('billing')
        self.automatic('billing')
        files = {'swhurl.yaml': manifest}

        def opener(request):
            if 'samclement/billing' in request.full_url:
                raise urllib.error.HTTPError(request.full_url, 503, 'Unavailable', {}, None)
            return self.registry('orders', [*tags, '12-2222222'], files)(request)
        import urllib.error
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = cli.main_deploy(['--root', str(self.root)], opener=opener)
        self.assertEqual(code, 1)
        self.assertIn('[OK] orders/staging: image 10-def5678 -> 12-2222222', out.getvalue())
        self.assertIn('[ERROR] billing/staging: could not read a pull token for samclement/billing: 503', err.getvalue())
        self.assertNotIn('hello', out.getvalue() + err.getvalue(), 'an instance without an ImagePolicy is skipped')

    def without_production(self, app='orders'):
        shutil.rmtree(self.root / 'apps' / app / 'prod')
        (self.root / f'clusters/home/app-{app}-prod.yaml').unlink()
        registry = self.root / 'clusters/home/kustomization.yaml'
        registry.write_text(registry.read_text().replace(f'  - app-{app}-prod.yaml\n', ''))

    def test_staging_gets_what_each_new_image_needs(self):
        self.with_units()
        self.built_from_a_repository()
        self.automatic()
        self.without_production()
        self.quiet(edit.set_allows, self.root, 'orders', 'staging', add=['hello'])
        self.quiet(edit.set_calls, self.root, 'hello', 'staging', add=['orders'])
        release = self.root / 'apps/orders/staging/helmrelease.yaml'
        manifest = ('version: 1\nstack: kotlin\nkind: web\nport: 9090\nhealthPath: /ready\nuid: 1000\ntelemetry: otlp\n'
                    'resources: {cpu: 50m, memory: 16Mi}\nflags: {shout: {type: boolean, default: false}}\n')
        changed, out = self.quiet(edit.deploy_staging, self.root, 'orders',
                                  opener=self.registry('orders', ['8-def5678'], {'swhurl.yaml': manifest}))
        self.assertTrue(changed)
        self.assertIn('[OK] orders/staging needs: port 9000 -> 9090, health path / -> /ready, uid 101 -> 1000, telemetry otlp, '
                      "requests.cpu 10m -> 50m (the image's minimum)", out)
        app_values = yaml.safe_load(release.read_text())['spec']['values']
        main = app_values['controllers']['main']['containers']['main']
        self.assertEqual(app_values['service']['main']['ports']['http']['port'], 9090)
        self.assertEqual({name: probe['spec']['httpGet'] for name, probe in main['probes'].items()},
                         {name: {'path': '/ready', 'port': 9090} for name in ('readiness', 'liveness', 'startup')})
        self.assertEqual({app_values['defaultPodOptions']['securityContext'][k] for k in ('runAsUser', 'runAsGroup', 'fsGroup')}, {1000})
        self.assertEqual(main['env']['OTEL_SERVICE_NAME'], 'orders')
        self.assertEqual(main['resources'], {'requests': {'cpu': '50m', 'memory': '16Mi'}, 'limits': {'memory': '128Mi'}},
                         "the image's minimum where it states one, the platform's default where it does not")
        self.assertTrue(self.env('hello')['SWHURL_SERVICE_ORDERS_URL'].endswith(':9090'), "the caller follows the port")
        network = yaml.safe_load((release.parent / contract.NETWORK_POLICY_FILE).read_text())
        self.assertEqual({p['port'] for rule in network['spec']['ingress'] for p in rule['ports']}, {9090})
        self.assertEqual(generated.problems(self.root, only=frozenset((Instance('hello', 'staging'), Instance('orders', 'staging')))), [])
        self.assertEqual(yaml.safe_load((release.parent / 'release.yaml').read_text())['stack'], 'kotlin',
                         'the record says what the image says it is built with')

        # The same image again changes nothing and reads nothing; --refresh reads it again.
        changed, _ = self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry('orders', ['8-def5678'], {}))
        self.assertFalse(changed)
        changed, out = self.quiet(edit.deploy_staging, self.root, 'orders', refresh=True, opener=self.registry(
            'orders', ['8-def5678'], {'swhurl.yaml': manifest.replace('telemetry: otlp\n', '')}))
        self.assertTrue(changed)
        self.assertIn('needs: telemetry none', out)
        self.assertNotIn('OTEL_SERVICE_NAME', self.env('orders'))

        # A need the instance cannot take holds the image: nothing changes.
        before = {p: p.read_bytes() for p in self.root.rglob('*.yaml')}
        for declared, message in (
                ('version: 1\nkind: worker\n', 'kind: worker, but the instance is a web app'),
                ('version: 1\nkind: web\nport: 9090\nhealthPath: /ready\nuid: 1000\nresources: {cpu: "1"}\n',
                 r"exceeds the namespace quota for requests.cpu"),
                ('version: 1\nkind: web\nport: 9090\nhealthPath: /ready\nuid: 1000\nresources: {memory: 256Mi}\n',
                 'a memory request of 256Mi is above the memory limit 128Mi')):
            with self.subTest(message=message), self.assertRaisesRegex(edit.EditError, message):
                edit.deploy_staging(self.root, 'orders', opener=self.registry('orders', ['9-0000000'], {'swhurl.yaml': declared}))
        self.assertEqual({p: p.read_bytes() for p in self.root.rglob('*.yaml')}, before)
        self.assertIn('tag: 8-def5678', release.read_text())

        # app-scale may set more than the minimum its Release records, never less, and reads no network.
        record = release.parent / 'release.yaml'
        with self.assertRaisesRegex(edit.EditError, r"below the image's minimum.*--cpu 20m \(at least 50m\)"):
            edit.scale(self.root, 'orders', 'staging', cpu='20m')
        kept = record.read_bytes()
        self.quiet(edit.scale, self.root, 'orders', 'staging', cpu='0.1')
        self.quiet(edit.scale, self.root, 'orders', 'staging', replicas=2)
        self.assertEqual(record.read_bytes(), kept, "a scale changes Settings only: the image's minimum stays")
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as err, \
                mock.patch.object(cli, 'check_generated', return_value=0), \
                mock.patch('urllib.request.urlopen', side_effect=AssertionError('app-scale reads no network')):
            code = cli.main_scale(['orders', 'staging', '--memory', '8Mi', '--root', str(self.root)])
        self.assertEqual(code, 2)
        self.assertIn('--memory 8Mi (at least 16Mi)', err.getvalue())

    def test_a_new_database_port_and_secret_name_reach_staging_then_production_with_the_promotion(self):
        """What production must be ready for no longer holds staging's image while the app has production:
        the promotion carries it in the same commit as the image, and a person merges that promotion."""
        self.with_units()
        self.built_from_a_repository()
        self.automatic()
        declared = 'version: 1\nkind: web\nport: 9090\nhealthPath: /\nuid: 101\ndatabase: sqlite\nsecrets: [API_TOKEN]\n'
        changed, out = self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry(
            'orders', ['8-def5678'], {'swhurl.yaml': declared}))
        self.assertTrue(changed)
        self.assertIn('database sqlite on a new 1Gi volume', out)
        prod = self.root / 'apps/orders/prod'

        def values(env):
            return yaml.safe_load((self.root / 'apps/orders' / env / 'helmrelease.yaml').read_text())['spec']['values']
        self.assertNotIn('data', values('prod')['persistence'], 'production keeps the old release until the promotion')
        self.assertEqual(generated.problems(self.root, only=frozenset(model_sync.read(self.root)[0])), [], 'staging may run a newer release')

        _, out = self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod')
        for change in ('port 9000 -> 9090', 'database sqlite on a new 1Gi volume', 'secret names +API_TOKEN'):
            self.assertIn(change, out)
        self.assertIn('[INFO] orders/prod reads new secret names API_TOKEN: set their values before merging', out)
        main = values('prod')['controllers']['main']
        self.assertEqual((main['replicas'], main['strategy']), (1, 'Recreate'), 'one writer, stop-first')
        self.assertEqual(main['containers']['main']['env']['DATABASE_PATH'], '/data/app.db')
        self.assertEqual(values('prod')['persistence']['data']['storageClass'], 'local-path-retain', 'its own volume')
        self.assertEqual(values('prod')['service']['main']['ports']['http']['port'], 9090)
        self.assertTrue((prod / contract.BACKUP_FILE).is_file(), 'nightly backups of its own')
        self.assertEqual(generated.problems(self.root, only=frozenset(model_sync.read(self.root)[0])), [])

    def test_a_rollback_never_crosses_a_database_change(self):
        self.with_units()
        self.built_from_a_repository()
        self.automatic()
        # Staging runs an image older than production's (6-0000000) that keeps a database production has not.
        self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry(
            'orders', ['5-1111111'], {'swhurl.yaml': 'version: 1\nkind: web\nport: 9000\nhealthPath: /\nuid: 101\ndatabase: sqlite\n'}))
        prod = self.root / 'apps/orders/prod'
        before = {p: p.read_bytes() for p in prod.rglob('*.yaml')}
        with self.assertRaisesRegex(edit.EditError, 'older image with another database than production: a rollback never changes'):
            edit.promote(self.root, 'orders', 'staging', 'prod')
        self.assertEqual({p: p.read_bytes() for p in prod.rglob('*.yaml')}, before)

    def test_health_path_user_telemetry_and_minimum_move_with_the_image_to_production(self):
        self.with_units()
        self.built_from_a_repository()
        self.automatic()
        declared = 'version: 1\nkind: web\nport: 9000\nhealthPath: /ready\nuid: 1000\ntelemetry: otlp\nresources: {memory: 64Mi}\n'
        changed, out = self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry(
            'orders', ['8-def5678'], {'swhurl.yaml': declared}))
        self.assertIn('needs: health path / -> /ready, uid 101 -> 1000, telemetry otlp, requests.memory 32Mi -> 64Mi', out)
        prod = self.root / 'apps/orders/prod'

        def runs(env):
            values = yaml.safe_load((self.root / 'apps/orders' / env / 'helmrelease.yaml').read_text())['spec']['values']
            main = values['controllers']['main']['containers']['main']
            return ({probe['spec']['httpGet']['path'] for probe in main['probes'].values()},
                    values['defaultPodOptions']['securityContext']['runAsUser'],
                    main['env'].get('OTEL_SERVICE_NAME'), main['resources']['requests']['memory'])
        self.assertEqual(runs('staging'), ({'/ready'}, 1000, 'orders', '64Mi'))
        self.assertEqual(runs('prod'), ({'/'}, 101, None, '32Mi'), 'production keeps the old release until the promotion')
        self.assertEqual(generated.problems(self.root, only=frozenset(model_sync.read(self.root)[0])), [], 'needs move with the image')

        _, out = self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod')
        self.assertIn("[OK] orders/prod: health path / -> /ready, uid 101 -> 1000, telemetry otlp, requests.memory 32Mi -> 64Mi "
                      "(the image's minimum)", out)
        self.assertEqual(runs('prod'), runs('staging'))

        # Production's own capacity still wins where it is higher; a minimum its quota cannot hold is refused.
        self.quiet(edit.scale, self.root, 'orders', 'prod', replicas=4, memory='128Mi')
        self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry(
            'orders', ['9-0000000'], {'swhurl.yaml': declared.replace('{memory: 64Mi}', '{cpu: 500m, memory: 96Mi}')}))
        before = {p: p.read_bytes() for p in prod.rglob('*.yaml')}
        with self.assertRaisesRegex(edit.EditError, r"the image's minimum \{'cpu': '500m', 'memory': '96Mi'\} at 4 replica\(s\) "
                                                    'exceeds the namespace quota for requests.cpu'):
            edit.promote(self.root, 'orders', 'staging', 'prod')
        self.assertEqual({p: p.read_bytes() for p in prod.rglob('*.yaml')}, before)
        self.quiet(edit.scale, self.root, 'orders', 'prod', replicas=2)
        self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod')
        self.assertEqual(runs('prod')[3], '128Mi', "production's capacity, above the image's minimum")

    def test_the_description_follows_each_image_and_moves_with_it_to_production(self):
        self.with_units()
        self.built_from_a_repository()
        self.automatic()
        declared = 'version: 1\nkind: web\nport: 9000\nhealthPath: /\nuid: 101\n'
        changed, out = self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry(
            'orders', ['8-def5678'], {'swhurl.yaml': declared + 'description: Orders, kept for a year\n'}))
        self.assertTrue(changed)
        self.assertIn('needs: description', out)
        self.assertEqual(self.env('orders')['SWHURL_DESCRIPTION'], 'Orders, kept for a year',
                         'production differs until the promotion: not drift, so the image is not held')
        self.assertNotIn('SWHURL_DESCRIPTION', yaml.safe_load((self.root / 'apps/orders/prod/helmrelease.yaml').read_text())
                         ['spec']['values']['controllers']['main']['containers']['main']['env'])
        self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod')
        self.assertEqual(yaml.safe_load((self.root / 'apps/orders/prod/helmrelease.yaml').read_text())
                         ['spec']['values']['controllers']['main']['containers']['main']['env']['SWHURL_DESCRIPTION'],
                         'Orders, kept for a year')
        changed, out = self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry(
            'orders', ['9-0000000'], {'swhurl.yaml': declared}))
        self.assertIn('needs: description', out)
        self.assertNotIn('SWHURL_DESCRIPTION', self.env('orders'), 'an image without one takes it away')

    def test_a_push_does_not_change_who_owns_a_volume(self):
        self.with_units()
        self.built_from_a_repository()
        self.without_production()
        declared = 'version: 1\nkind: web\nport: 9000\nhealthPath: /\nuid: 101\ndatabase: sqlite\n'
        self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry('orders', ['8-def5678'], {'swhurl.yaml': declared}))
        with self.assertRaisesRegex(edit.EditError, 'keeps a volume whose files belong to uid 101'):
            edit.deploy_staging(self.root, 'orders', opener=self.registry(
                'orders', ['9-0000000'], {'swhurl.yaml': declared.replace('uid: 101', 'uid: 1000')}))

    def test_a_new_image_brings_its_database_and_secret_names(self):
        self.with_units()
        self.built_from_a_repository()
        self.automatic()
        self.without_production()
        instance = self.root / 'apps/orders/staging'
        base = 'version: 1\nkind: web\nport: 9000\nhealthPath: /\nuid: 101\nflags: {shout: {type: boolean, default: false}}\n'
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            edit.deploy_staging(self.root, 'orders', opener=self.registry(
                'orders', ['8-def5678'], {'swhurl.yaml': base + 'database: sqlite\nsecrets: [API_TOKEN, WEBHOOK_KEY]\n'}))
        self.assertIn('needs: database sqlite on a new 1Gi volume, secret names +API_TOKEN, +WEBHOOK_KEY', out.getvalue())
        self.assertIn('value missing for API_TOKEN, WEBHOOK_KEY (the app runs without it)', err.getvalue())
        release = yaml.safe_load((instance / 'helmrelease.yaml').read_text())
        values = release['spec']['values']
        main = values['controllers']['main']
        self.assertEqual((values['persistence']['data']['size'], values['persistence']['data']['storageClass'],
                          main['containers']['main']['env']['DATABASE_PATH'], main['replicas'], main['strategy']),
                         ('1Gi', 'local-path-retain', '/data/app.db', 1, 'Recreate'))
        self.assertEqual(release['metadata']['annotations']['platform.swhurl.com/secrets'], 'API_TOKEN,WEBHOOK_KEY')
        self.assertIn({'secretRef': {'name': 'orders-secret', 'optional': True}}, main['containers']['main']['envFrom'])
        self.assertIn('orders-secret', main['annotations']['secret.reloader.stakater.com/reload'])
        namespace = yaml.safe_load((instance / 'namespace.yaml').read_text())
        self.assertEqual(namespace['metadata']['annotations']['kustomize.toolkit.fluxcd.io/prune'], 'disabled')
        self.assertIn('orders-staging', (self.root / 'platform/reloader/helmrelease.yaml').read_text())
        self.assertFalse((instance / 'secret.sops.yaml').exists(), 'no value is invented')

        # A push never deletes data or resizes a volume: those images are held.
        before = {p: p.read_bytes() for p in self.root.rglob('*.yaml')}
        for declared, message in ((base + 'secrets: [API_TOKEN, WEBHOOK_KEY]\n', 'database: none, but orders keeps its database volume'),
                                  (base + 'database: sqlite\ndatabaseSize: 2Gi\nsecrets: [API_TOKEN, WEBHOOK_KEY]\n',
                                   'databaseSize 2Gi, but the volume was created with 1Gi')):
            with self.subTest(message=message), self.assertRaisesRegex(edit.EditError, message):
                edit.deploy_staging(self.root, 'orders', opener=self.registry('orders', ['9-0000000'], {'swhurl.yaml': declared}))
        self.assertEqual({p: p.read_bytes() for p in self.root.rglob('*.yaml')}, before)

        # A dropped name goes from the release; the Secret reference stays for any value already set.
        changed, out = self.quiet(edit.deploy_staging, self.root, 'orders', opener=self.registry(
            'orders', ['10-1111111'], {'swhurl.yaml': base + 'database: sqlite\nsecrets: [API_TOKEN]\n'}))
        self.assertIn('secret names -WEBHOOK_KEY', out)

    def test_a_secret_value_reaches_sops_on_standard_input_only(self):
        from swhurl.run import FakeRunner, Result
        self.with_units()
        self.without_production()
        seen = []

        def sops(args, given):
            seen.append((args, given))
            if args[1] == '--encrypt':
                return Result(args, 0, given.replace('s3cret-value', 'ENC[AES256_GCM,data:x]') + 'sops:\n  age: []\n')
            return Result(args, 0, '')
        runner = FakeRunner().on('sops', handler=sops)
        instance = self.root / 'apps/orders/staging'
        self.quiet(edit.set_secret, self.root, 'orders', 'staging', 'API_TOKEN', 's3cret-value', runner=runner)
        stored = (instance / 'secret.sops.yaml').read_text()
        self.assertNotIn('s3cret-value', stored)
        self.assertEqual(contract.stored_secrets(stored), ['API_TOKEN'])
        self.assertIn('secret.sops.yaml', yaml.safe_load((instance / 'kustomization.yaml').read_text())['resources'])
        unit = yaml.safe_load((self.root / 'clusters/home/app-orders-staging.yaml').read_text())
        self.assertEqual(unit['spec']['decryption']['secretRef']['name'], 'sops-age')
        self.assertIn({'secretRef': {'name': 'orders-secret', 'optional': True}},
                      yaml.safe_load((instance / 'helmrelease.yaml').read_text())['spec']['values']['controllers']['main']['containers']['main']['envFrom'])
        # The second value goes into the existing file with sops set, again on standard input.
        _, out = self.quiet(edit.set_secret, self.root, 'orders', 'staging', 'WEBHOOK_KEY', 'other "value"', runner=runner)
        self.assertEqual(seen[-1], (('sops', 'set', '--value-stdin', 'apps/orders/staging/secret.sops.yaml', '["stringData"]["WEBHOOK_KEY"]'),
                                    json.dumps('other "value"')))
        self.assertIn('not among the secrets its image declares yet', out)
        self.assertFalse(any('s3cret-value' in ' '.join(args) or 'other' in ' '.join(args) for args, _ in seen), 'never on a command line')
        for name, value, message in (('HOST_IP', 'x', 'reserved for the platform'), ('API_TOKEN', '', 'no value for API_TOKEN')):
            with self.subTest(name=name), self.assertRaisesRegex(edit.EditError, message):
                edit.set_secret(self.root, 'orders', 'staging', name, value, runner=runner)

    def test_a_secret_on_a_records_only_first_promotion_writes_the_encrypted_file_alone(self):
        """A first promotion's branch carries production's records and nothing rendered: make app-secret sets a
        value there without rendering, and main renders it when the branch is merged."""
        from swhurl.run import FakeRunner, Result
        self.with_units()
        prod = self.root / 'apps/orders/prod'
        for path in prod.iterdir():
            if path.name not in ('release.yaml', 'instance.yaml'):
                path.unlink()
        runner = FakeRunner().on('sops', handler=lambda args, given: Result(
            args, 0, given.replace('s3cret-value', 'ENC[AES256_GCM,data:x]') + 'sops:\n  age: []\n'))
        self.quiet(edit.set_secret, self.root, 'orders', 'prod', 'API_TOKEN', 's3cret-value', runner=runner)
        self.assertEqual(sorted(path.name for path in prod.iterdir()), ['instance.yaml', 'release.yaml', 'secret.sops.yaml'])
        self.assertEqual(contract.stored_secrets((prod / 'secret.sops.yaml').read_text()), ['API_TOKEN'])
        with self.assertRaisesRegex(edit.EditError, 'no instance orders/dev|ENV'):
            edit.set_secret(self.root, 'orders', 'dev', 'API_TOKEN', 'x', runner=runner)

    def test_promotion_carries_what_the_promoted_image_offers(self):
        from swhurl.apps import offers
        self.with_units()
        self.built_from_a_repository()
        runner = self.sops()
        manifest = 'version: 1\nkind: web\nhealthPath: /healthz\nprovides: {eventsFile: asyncapi.yaml}\n'
        self.quiet(intake.set_offers, self.root, 'orders', 'staging',
                   opener=self.repository({'swhurl.yaml': manifest, 'asyncapi.yaml': self.ASYNCAPI})[0], runner=runner)
        staging = self.root / 'apps/orders/staging/helmrelease.yaml'
        record = staging.parent / 'release.yaml'
        record.write_text(record.read_text().replace(yaml.safe_load(record.read_text())['image']['digest'], NEW_DIGEST))
        model_sync.apply(self.root, model_sync.prepare(self.root, {}))
        self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod', runner=runner)
        prod = self.root / 'apps/orders/prod'
        self.assertEqual(offers.read(prod).commit, 'abc1234')
        self.assertEqual((prod / 'offers/asyncapi.yaml').read_text(), self.ASYNCAPI)
        self.assertEqual(self.env('orders', 'prod')['SWHURL_EVENTS_PUBLISH'], 'created,paid', 'no second declaration for production')
        self.assertEqual(len(self.seeds), 2, 'production has its own key')
        self.assertEqual(offers.problems(prod, ['created', 'paid'], 'abc1234'), [])
        self.assertEqual(generated.problems(self.root, only=frozenset(model_sync.read(self.root)[0])), [], 'offers follow each image')
        # A first promotion carries them too: production is made from staging's release, then given the documents.
        shutil.rmtree(prod)
        (self.root / 'clusters/home/app-orders-prod.yaml').unlink()
        registry = self.root / 'clusters/home/kustomization.yaml'
        registry.write_text(registry.read_text().replace('  - app-orders-prod.yaml\n', ''))
        with mock.patch.object(policy, 'evaluate', side_effect=lambda instance, runner=None: [
                f'offers: {p}' for p in offers.problems(instance, [], str((yaml.safe_load((instance / 'helmrelease.yaml').read_text())['metadata']
                                                                           .get('annotations') or {}).get(interface.OFFERS_ANNOTATION, '')))
                if 'annotated' in p]):
            self.quiet(edit.promote, self.root, 'orders', 'staging', 'prod', runner=runner)
        self.assertEqual((offers.read(prod).commit, self.env('orders', 'prod')['SWHURL_EVENTS_PUBLISH']), ('abc1234', 'created,paid'))
        record = self.root / 'apps/orders/staging/release.yaml'
        doc = yaml.safe_load(record.read_text())
        doc['apiPath'] = '/openapi.json'
        record.write_text(yaml.safe_dump(doc, sort_keys=False))
        model_sync.apply(self.root, model_sync.prepare(self.root, {}))
        self.assertEqual(generated.problems(self.root, only=frozenset(model_sync.read(self.root)[0])), [],
                         'staging describes its API before production runs that image')

    def test_event_refusals_change_nothing(self):
        self.with_units()
        before = self.staging.read_text()
        cases = (({}, 'give --subscribe'),
                 ({'subscribe': ['created']}, '<app>.<event>'), ({'subscribe': ['orders.created', 'orders.created']}, 'each once'),
                 ({'subscribe': ['orders.created']}, 'which orders/staging does not publish'))
        for kwargs, message in cases:
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.set_events(self.root, 'hello', 'staging', runner=self.sops(), **kwargs)
        self.assertEqual(self.staging.read_text(), before)
        self.assertFalse((self.root / 'apps/hello/staging/events.sops.yaml').exists())
        from swhurl.run import FakeRunner
        with self.assertRaisesRegex(edit.EditError, 'could not SOPS-encrypt'):
            self.publish_events('hello', ['created'], FakeRunner().on('sops', returncode=1, stderr='no key'))
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
    def test_scale_refuses_bad_values(self):
        for kwargs, message in (({}, 'at least one'), ({'replicas': 11}, '0 to 10'), ({'replicas': -1}, '0 to 10'),
                                ({'cpu': '1core'}, 'quantity'), ({'memory': '64M'}, 'quantity'),
                                ({'memory_limit': '1Ti'}, 'quantity')):
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.scale(self.root, 'hello', 'prod', **kwargs)

    def snapshot(self):
        return {p.relative_to(self.root): p.read_bytes() for p in self.root.rglob('*') if p.is_file()}

    def test_remove_refuses_waiting_and_granted_callers_before_any_write(self):
        self.with_units()
        self.quiet(edit.set_calls, self.root, 'hello', 'staging', add=['orders'])
        self.assertNotIn('SWHURL_SERVICE_ORDERS_URL', self.env('hello'), 'waiting calls render no connection')
        for granted in (False, True):
            if granted:
                self.quiet(edit.set_allows, self.root, 'orders', 'staging', add=['hello'])
            # A broken/missing render must not hide a caller. The records alone refuse removal.
            rendered = self.root / 'apps/hello/staging/helmrelease.yaml'
            original = rendered.read_bytes()
            rendered.write_text('invalid: [')
            before = self.snapshot()
            with self.subTest(granted=granted), self.assertRaisesRegex(edit.EditError, 'is called by hello'):
                edit.remove(self.root, 'orders', 'staging')
            self.assertEqual(self.snapshot(), before)
            rendered.unlink()
            before = self.snapshot()
            with self.assertRaisesRegex(edit.EditError, 'is called by hello'):
                edit.remove(self.root, 'orders', 'staging')
            self.assertEqual(self.snapshot(), before)
            rendered.write_bytes(original)

    def test_remove_refuses_record_only_subscriber_without_writing(self):
        self.with_units()
        settings = self.root / 'apps/hello/staging/instance.yaml'
        doc = yaml.safe_load(settings.read_text())
        doc['subscriptions'] = ['orders.created']
        settings.write_text(yaml.safe_dump(doc))
        (self.root / 'apps/hello/staging/helmrelease.yaml').unlink()
        before = self.snapshot()
        with self.assertRaisesRegex(edit.EditError, 'publishes events that hello subscribe to'):
            edit.remove(self.root, 'orders', 'staging')
        self.assertEqual(self.snapshot(), before)

    def test_record_only_removal_refuses_subscribers_before_rendering_or_writing(self):
        self.with_units()
        self.publish_events('orders', ['created'], self.sops())
        self.quiet(edit.set_events, self.root, 'hello', 'staging', subscribe=['orders.created'], runner=self.sops())
        folder = self.root / 'apps/orders/staging'
        for name in ('release.yaml', 'instance.yaml'):
            (folder / name).unlink()
        before = self.snapshot()
        with self.assertRaisesRegex(edit.EditError, 'publishes events that hello subscribe to'):
            model_sync.sync(self.root)
        self.assertEqual(self.snapshot(), before)

    def test_plain_release_update_does_not_allocate_a_nats_key_just_because_nats_exists(self):
        self.with_units()
        each = Instance('hello', 'staging')
        with mock.patch.object(model_sync, 'encrypt_secret', side_effect=AssertionError('unexpected encryption')) as key:
            intake.change_release(self.root, each, model_sync.Update(lambda entry: entry), {})
        key.assert_not_called()
        self.assertFalse((each.folder(self.root) / 'events.sops.yaml').exists())

    def test_malformed_notification_inputs_clean_up_keys_on_failed_release_and_sync_planning(self):
        self.with_units()
        notify = self.root / 'platform/notify'
        notify.mkdir(parents=True)
        teams = self.root / 'apps/teams.yaml'
        for path, bad in ((teams, 'teams: ['), (notify / 'channel-phone.sops.yaml', 'metadata: [')):
            teams.write_text('teams: {demo: {}}\n')
            path.write_text(bad)
            for action in ('release', 'sync'):
                with self.subTest(path=path.name, action=action):
                    before, runner = self.snapshot(), self.sops()
                    if action == 'release':
                        with self.assertRaisesRegex(edit.EditError, 'notification route'):
                            intake.change_release(self.root, Instance('hello', 'staging'),
                                                  model_sync.Update(lambda entry: dataclasses.replace(
                                                      entry, release=dataclasses.replace(entry.release, publishes=('created',)))), {}, runner=runner)
                    else:
                        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as errors:
                            with mock.patch.object(model_sync, 'Runner', return_value=runner):
                                self.assertEqual(cli.main_sync(['--root', str(self.root)]), 2)
                        self.assertIn('notification route', errors.getvalue())
                    self.assertTrue(runner.calls, 'a key was actually generated/encrypted before planning refused')
                    self.assertEqual(self.snapshot(), before, 'new encrypted inputs are removed on planning refusal')
            path.unlink()

    def test_remove_ignores_self_and_other_environment_relations(self):
        self.with_units()
        # Self records go with the target; production's relations belong to production's target.
        for app, env in (('orders', 'staging'), ('hello', 'prod')):
            settings = self.root / f'apps/{app}/{env}/instance.yaml'
            doc = yaml.safe_load(settings.read_text())
            doc.update(calls=['orders'], subscriptions=['orders.created'])
            settings.write_text(yaml.safe_dump(doc))
        # Removal's existing writer syncs afterwards; valid production publications and keys let it render.
        record = self.root / 'apps/orders/prod/release.yaml'
        doc = yaml.safe_load(record.read_text())
        doc['publishes'] = ['created']
        record.write_text(yaml.safe_dump(doc))
        for app in ('hello', 'orders'):
            settings = self.root / f'apps/{app}/prod/instance.yaml'
            doc = yaml.safe_load(settings.read_text())
            doc['allocations'] = {'eventsKey': 'U' + app.upper()}
            settings.write_text(yaml.safe_dump(doc))
            (settings.parent / 'events.sops.yaml').write_text('sops: {}')
        self.quiet(edit.remove, self.root, 'orders', 'staging')
        self.assertFalse((self.root / 'apps/orders/staging').exists())
        self.assertTrue((self.root / 'apps/orders/prod').exists())

    def test_remove_invalid_world_is_cli_error_and_writes_nothing(self):
        (self.prod.parent / 'instance.yaml').write_text('invalid: [')
        before = self.snapshot()
        code, _ = self.quiet(cli.main_remove, ['hello', 'staging', '--root', str(self.root)])
        self.assertEqual(code, 2)
        self.assertEqual(self.snapshot(), before)

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
        settings = self.root / 'apps/hello/prod/instance.yaml'
        document = yaml.safe_load(settings.read_text())
        document['allocations'] = {'database': {'size': '1Gi', 'storageClass': 'local-path-retain'}}
        settings.write_text(yaml.safe_dump(document))
        _, out = self.quiet(edit.remove, self.root, 'hello', 'prod')
        self.assertIn('[WARN] hello-prod has a retained volume', out)

    def test_unknown_or_invalid_instances(self):
        for app, env in (('nope', 'prod'), ('Hello', 'prod'), ('hello', 'dev'), ('../x', 'prod')):
            with self.subTest(app=app, env=env), self.assertRaises(edit.EditError):
                edit.remove(self.root, app, env)
        self.assertTrue((self.root / 'apps/hello/prod').exists())

    def test_promote_asks_what_changed_only_when_staging_is_behind(self):
        """Behind main by a commit: this checkout's history says whether it changed staging."""
        self.new_staging_image()
        image = promotion.select(self.root, model_sync.read(self.root)[0], 'hello').image
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
                cli.main_promote(['hello', '--root', str(self.root), *argv], runner=runner)
            return err.getvalue(), runner.calls
        self.assertEqual(refusal(history=False)[1], [], 'a tree without history is not asked')
        self.assertIn('Flux is still applying', refusal(history=False)[0])
        self.assertEqual(refusal(git={'stdout': 'docs/apps.md\napps/hello/prod/helmrelease.yaml\n'}), ('', [diff]))
        self.assertIn('a change to staging is in Git', refusal(git={'stdout': 'apps/hello/staging/helmrelease.yaml\n'})[0])
        self.assertIn('Flux is still applying', refusal(git={'returncode': 128, 'stderr': 'bad object def'})[0], 'a commit not fetched here')

    def test_console_preparation_skips_only_live_readiness_and_direct_cli_stays_guarded(self):
        self.new_staging_image()
        with mock.patch.object(ops, 'gather_status', return_value=None) as read, \
                mock.patch.object(edit, 'promote', return_value=0) as write:
            code, _ = self.quiet(cli.main_promote, ['hello', '--root', str(self.root), '--prepare-only'])
            self.assertEqual(code, 0)
            read.assert_not_called()
            write.assert_called_once()
            write.reset_mock()
            code, _ = self.quiet(cli.main_promote, ['hello', '--root', str(self.root)])
            self.assertEqual(code, 2)
            read.assert_called_once()
            write.assert_not_called()
            code, _ = self.quiet(cli.main_promote, ['hello', '--root', str(self.root), '--prepare-only', '--expect-image=wrong'])
            self.assertEqual(code, 2, 'preparation still pins the exact reviewed image')
            write.assert_not_called()

    def test_cli_refusals_exit_2(self):
        code, _ = self.quiet(cli.main_scale, ['hello', 'prod', '--root', str(self.root)])
        self.assertEqual(code, 2)
        code, _ = self.quiet(cli.main_promote, ['hello', '--root', str(self.root)])
        self.assertEqual(code, 2)


if __name__ == '__main__':
    unittest.main()
