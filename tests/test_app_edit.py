"""app-promote, app-scale, app-remove: Git edits of generated instances, offline on a copy of the sample instance."""
import difflib
import io
import shutil
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import yaml

from swhurl import ROOT
from swhurl.apps import contract, edit, new, ops, policy
from swhurl.run import CommandError

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
        reviewed = ops.image_reference(edit.container(edit.load_release(self.staging.parent))['image'])
        self.staging.write_text(self.staging.read_text().replace('tag: 1.27-alpine', 'tag: 1.28-alpine').replace(
            edit.container(edit.load_release(self.staging.parent))['image']['digest'], NEW_DIGEST))
        before = self.prod.read_bytes()
        with self.assertRaisesRegex(edit.EditError, 'changed since it was reviewed'):
            edit.promote(self.root, 'hello', 'staging', 'prod', expect_image=reviewed)
        self.assertEqual(self.prod.read_bytes(), before)
        current = ops.image_reference(edit.container(edit.load_release(self.staging.parent))['image'])
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
        old_digest = edit.container(edit.load_release(self.staging.parent))['image']['digest']
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
            edit.container(edit.load_release(self.staging.parent))['image']['digest'], NEW_DIGEST))
        self.quiet(edit.promote, self.root, 'hello', 'staging', 'prod')
        self.assertIn('tag: 1.28-alpine\n', self.prod.read_text(), 'production gets the tag, never the markers')
        self.assertNotIn('imagepolicy', self.prod.read_text())
        self.assertNotEqual(before, self.prod.read_text())

    def expose(self, env, exposure, host=None):
        return self.quiet(edit.expose, self.root, 'hello', env, exposure, host)

    def state(self, env):
        instance = self.root / 'apps/hello' / env
        values = edit.load_release(instance)['spec']['values']
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

    def test_team_moves_every_environment_and_fills_in_missing_isolation(self):
        (self.root / 'apps/teams.yaml').write_text('teams:\n  platform: {}\n  payments: {}\n')
        for env in ('staging', 'prod'):  # an instance from before quotas and policies existed
            instance = self.root / 'apps/hello' / env
            for name in contract.ISOLATION_FILES:
                (instance / name).unlink()
            listing = instance / 'kustomization.yaml'
            listing.write_text(''.join(line for line in listing.read_text().splitlines(keepends=True)
                                       if not any(name in line for name in contract.ISOLATION_FILES)))
        self.quiet(edit.team, self.root, 'hello', 'payments')
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
            edit.team(self.root, 'hello', 'payments')
        before = (self.root / 'apps/hello/staging/namespace.yaml').read_text()
        with self.assertRaisesRegex(edit.EditError, "unknown team 'nobody'"):
            edit.team(self.root, 'hello', 'nobody')
        self.assertEqual((self.root / 'apps/hello/staging/namespace.yaml').read_text(), before)

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
                      '--no-policy-check'])
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
        route = edit.load_release(self.staging.parent)['spec']['values']['ingress']['main']
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

    def test_scale_refuses_bad_values(self):
        for kwargs, message in (({}, 'at least one'), ({'replicas': 11}, '0 to 10'), ({'replicas': -1}, '0 to 10'),
                                ({'cpu': '1core'}, 'quantity'), ({'memory': '64M'}, 'quantity'),
                                ({'memory_limit': '1Ti'}, 'quantity')):
            with self.subTest(kwargs=kwargs), self.assertRaisesRegex(edit.EditError, message):
                edit.scale(self.root, 'hello', 'prod', **kwargs)

    def test_remove_leaves_no_trace(self):
        reloader = self.root / 'platform/reloader/helmrelease.yaml'
        reloader.write_text(reloader.read_text().replace('namespaces: [ingress, logging, console]',
                                                         'namespaces: [ingress, logging, console, hello-prod]'))
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

    def test_cli_refusals_exit_2(self):
        code, _ = self.quiet(edit.main_scale, ['hello', 'prod', '--root', str(self.root)])
        self.assertEqual(code, 2)
        code, _ = self.quiet(edit.main_promote, ['hello', '--root', str(self.root)])
        self.assertEqual(code, 2)


if __name__ == '__main__':
    unittest.main()
