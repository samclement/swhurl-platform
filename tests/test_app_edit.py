"""app-promote, app-scale, app-remove: Git edits of generated instances, offline on a copy of the repo."""
import difflib
import io
import shutil
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import contract, edit, new, policy

NEW_DIGEST = 'sha256:' + 'b' * 64


def changed_lines(before: str, after: str) -> list[str]:
    return [line for line in difflib.unified_diff(before.splitlines(), after.splitlines(), lineterm='', n=0)
            if line[:1] in '+-' and not line.startswith(('+++', '---'))]


class EditTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        for rel in ('apps/hello', 'clusters/home/kustomization.yaml', 'clusters/home/app-hello-staging.yaml',
                    'clusters/home/app-hello-prod.yaml', 'platform/reloader/helmrelease.yaml'):
            src, dst = ROOT / rel, self.root / rel
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

    def test_hand_edited_files_are_refused_not_reformatted(self):
        self.prod.write_text('# tuned by hand\n' + self.prod.read_text())
        before = self.prod.read_text()
        with self.assertRaisesRegex(edit.EditError, 'edited by hand'):
            edit.scale(self.root, 'hello', 'prod', replicas=2)
        self.assertEqual(self.prod.read_text(), before)

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
        self.staging.write_text(text.replace('tag: 1.27-alpine', 'tag: 1.28-alpine'))
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

    def test_expose_refusals(self):
        cases = (('public', None, 'needs --host'), ('public', 'x.homelab.swhurl.com', 'outside'),
                 ('authenticated-web', 'hello.example.com', 'must be under'), ('private', 'x.example.com', 'drop --host'),
                 ('authenticated-web', None, 'already authenticated-web'))
        for exposure, host, message in cases:
            with self.subTest(exposure=exposure, host=host), self.assertRaisesRegex(edit.EditError, message):
                edit.expose(self.root, 'hello', 'staging', exposure, host)

    def test_expose_refuses_a_worker_route_and_hand_edits(self):
        with redirect_stdout(io.StringIO()):
            new.main(['job', '--env', 'staging', '--kind', 'worker', '--image', 'r/job:1', '--root', str(self.root),
                      '--no-policy-check'])
        with self.assertRaisesRegex(edit.EditError, 'worker'):
            edit.expose(self.root, 'job', 'staging', 'public', 'job.example.com')
        namespace = self.root / 'apps/hello/staging/namespace.yaml'
        namespace.write_text('# tuned by hand\n' + namespace.read_text())
        with self.assertRaisesRegex(edit.EditError, 'edited by hand'):
            edit.expose(self.root, 'hello', 'staging', 'private')

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
