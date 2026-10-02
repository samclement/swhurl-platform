"""Images built from this repo: pinned inputs that match CI, and a build context that admits no keys."""
import difflib
import io
import json
import re
import shutil
import subprocess
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

import yaml

from swhurl import ROOT, images, platform
from swhurl.run import FakeRunner, Result, Runner

DOCKERFILE = (ROOT / 'images/console/Dockerfile').read_text()


def arg(name: str) -> str:
    return re.search(rf'^ARG {name}=(\S+)$', DOCKERFILE, re.M).group(1)


class ConsoleImageTests(unittest.TestCase):
    def test_kubectl_and_helm_match_ci(self):
        steps = yaml.safe_load((ROOT / '.github/workflows/validate.yml').read_text())['jobs']['validate']['steps']
        ci = {s['uses'].split('@')[0]: s['with'].get('version') for s in steps if 'with' in s and 'uses' in s}
        self.assertEqual(arg('KUBECTL_VERSION'), ci['azure/setup-kubectl'])
        self.assertEqual(arg('HELM_VERSION'), ci['azure/setup-helm'])

    def test_every_download_and_base_image_is_pinned(self):
        for tool in ('KUBECTL', 'HELM', 'SOPS'):
            self.assertRegex(arg(f'{tool}_SHA256'), r'^[0-9a-f]{64}$', tool)
        for line in re.findall(r'^FROM (\S+)', DOCKERFILE, re.M):
            self.assertRegex(line, r'@sha256:[0-9a-f]{64}$', line)
        self.assertIn('sha256sum -c', DOCKERFILE)

    def test_runs_as_non_root(self):
        self.assertRegex(DOCKERFILE, r'(?m)^USER 65532:65532$')

    def test_build_context_admits_only_an_allowlist(self):
        lines = [line for line in (ROOT / '.dockerignore').read_text().splitlines() if line and not line.startswith('#')]
        self.assertEqual(lines[0], '*')
        admitted = [line[1:] for line in lines if line.startswith('!')]
        for path in admitted:
            self.assertNotRegex(path, r'agekey|sops|secret', path)


class ConsoleImageCommandTests(unittest.TestCase):
    def test_content_tag_matches_the_workflow_shell(self):
        paths = ' '.join(platform.CONSOLE_IMAGE_INPUTS)
        shell = subprocess.run(['bash', '-c', f'for p in {paths}; do git rev-parse "HEAD:$p"; done | sha256sum | cut -c1-16'],
                               cwd=ROOT, capture_output=True, text=True, check=True).stdout.strip()
        self.assertEqual(images.content_tag(Runner()), f'src-{shell}')

    def test_pin_keeps_comments_and_replaces_one_tag_and_digest(self):
        text = (ROOT / images.RELEASE).read_text()
        pinned = images.pin(text, 'src-0123456789abcdef', 'sha256:' + 'c' * 64)
        changed = [line for line in difflib.unified_diff(text.splitlines(), pinned.splitlines(), lineterm='', n=0)
                   if line[:1] in '+-' and not line.startswith(('+++', '---'))]
        self.assertEqual(len(changed), 4)
        self.assertEqual(pinned.count('#'), text.count('#'))
        with self.assertRaises(images.ImageError):
            images.pin(text + '    tag: again\n', 'x', 'y')

    def test_dashboard_job_shares_the_published_image_without_console_credentials(self):
        pinned = images.pin((ROOT / images.RELEASE).read_text(), 'src-0123456789abcdef', 'sha256:' + 'c' * 64)
        controllers = yaml.safe_load(pinned)['spec']['values']['controllers']
        web, job = controllers['main'], controllers['dashboards']
        self.assertEqual(web['containers']['main']['image'], job['containers']['main']['image'])
        self.assertEqual(web['forceRename'], 'console', 'adding a controller must keep the live Deployment name')
        self.assertEqual(job['serviceAccount']['name'], 'dashboard-sync')
        self.assertNotIn('envFrom', job['containers']['main'])
        self.assertEqual(job['cronjob']['concurrencyPolicy'], 'Forbid')

    def lookup(self, head):
        return (FakeRunner().on('curl', '--silent', '--show-error', '--fail', stdout=json.dumps({'token': 'anon-token'}))
                .on('curl', '--silent', '--show-error', '--head', stdout=head))

    def test_published_digest_from_the_registry_reply(self):
        runner = self.lookup('HTTP/2 200\r\ndocker-content-digest: sha256:abc\r\n\r\n')
        self.assertEqual(images.published_digest(runner, 'src-x'), 'sha256:abc')
        self.assertNotIn('anon-token', ' '.join(' '.join(c) for c in runner.calls), 'the token goes on stdin')
        self.assertIsNone(images.published_digest(self.lookup('HTTP/2 404\r\n\r\n'), 'src-x'))

    def test_command_refuses_unpublished_or_uncommitted_inputs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / images.RELEASE).parent.mkdir(parents=True)
            (root / images.RELEASE).write_text((ROOT / images.RELEASE).read_text())
            for status, head, message in ((' M tools/x.py\n', '', 'uncommitted'), ('', 'HTTP/2 404\r\n', 'not published')):
                runner = self.lookup(head).on('git', '-C', str(root), 'status', stdout=status).on(
                    'git', '-C', str(root), 'rev-parse', stdout='0' * 40 + '\n')
                with self.subTest(message=message), redirect_stderr(io.StringIO()) as err, redirect_stdout(io.StringIO()):
                    self.assertEqual(images.main([], runner, root), 1)
                self.assertIn(message, err.getvalue())
            self.assertEqual((root / images.RELEASE).read_text(), (ROOT / images.RELEASE).read_text())


class PublishWorkflowTests(unittest.TestCase):
    def test_pins_the_built_content_on_the_latest_main(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/publish-console.yml').read_text())
        self.assertEqual(workflow['permissions']['contents'], 'write')
        steps = workflow['jobs']['publish']['steps']
        self.assertEqual(steps[0]['with']['fetch-depth'], 0)
        pin = steps[-1]
        self.assertNotIn('if', pin, 'pin also when the image was already published (docs-only commits no-op)')
        self.assertIn('git checkout --quiet --detach origin/main', pin['run'])
        self.assertIn('swhurl console-image', pin['run'])
        self.assertIn('--expect "src-${{ steps.source.outputs.content }}" --commit', pin['run'])


class WorkflowPinTests(unittest.TestCase):
    """console-image --expect/--commit as the publish workflow runs it, with FakeRunner."""
    HEAD = 'HTTP/2 200\r\ndocker-content-digest: sha256:' + 'b' * 64 + '\r\n'

    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.root)
        (self.root / images.RELEASE).parent.mkdir(parents=True)
        (self.root / images.RELEASE).write_text((ROOT / images.RELEASE).read_text())
        self.ids = '1' * 40  # what `git rev-parse HEAD:<input>` answers; changing it changes the content tag

    def runner(self, push_codes=(0,), moved_on_pull=False):
        pushes = list(push_codes)

        def pull(argv, _input):
            if moved_on_pull:
                self.ids = '2' * 40
            return _result(argv, '')

        def push(argv, _input):
            return _result(argv, '', pushes.pop(0) if pushes else 0)

        git = ('git', '-C', str(self.root))
        return (FakeRunner()
                .on('curl', '--silent', '--show-error', '--fail', stdout=json.dumps({'token': 'anon-token'}))
                .on('curl', '--silent', '--show-error', '--head', stdout=self.HEAD)
                .on(*git, 'status', stdout='')
                .on(*git, 'rev-parse', handler=lambda argv, _i: _result(argv, self.ids + '\n'))
                .on(*git, 'commit', stdout='')
                .on(*git, 'pull', handler=pull)
                .on(*git, 'push', handler=push))

    def expected(self):
        return images.content_tag(self.runner(), self.root)

    def run_main(self, runner, *args):
        out = io.StringIO()
        with redirect_stdout(out), redirect_stderr(out):
            code = images.main(list(args), runner, self.root)
        return code, out.getvalue(), [c[3] for c in runner.calls if c[:3] == ('git', '-C', str(self.root))]

    def test_pins_commits_and_pushes(self):
        tag = self.expected()
        code, out, git = self.run_main(self.runner(), '--expect', tag, '--commit')
        self.assertEqual(code, 0, out)
        self.assertIn(f'tag: {tag}', (self.root / images.RELEASE).read_text())
        self.assertEqual([g for g in git if g in ('commit', 'push', 'pull')], ['commit', 'push'])
        self.assertIn('[OK] pushed', out)

    def test_does_nothing_when_main_builds_other_inputs(self):
        code, out, git = self.run_main(self.runner(), '--expect', 'src-0000000000000000', '--commit')
        self.assertEqual(code, 0, out)
        self.assertIn('nothing to do', out)
        self.assertEqual((self.root / images.RELEASE).read_text(), (ROOT / images.RELEASE).read_text())
        self.assertNotIn('commit', git)

    def test_rejected_push_is_rebased_and_retried_once(self):
        code, out, git = self.run_main(self.runner(push_codes=(1, 0)), '--expect', self.expected(), '--commit')
        self.assertEqual(code, 0, out)
        self.assertEqual([g for g in git if g in ('commit', 'push', 'pull')], ['commit', 'push', 'pull', 'push'])
        self.assertIn('pushed after rebasing', out)

    def test_main_moving_to_other_inputs_while_pushing_skips(self):
        code, out, git = self.run_main(self.runner(push_codes=(1,), moved_on_pull=True), '--expect', self.expected(),
                                       '--commit')
        self.assertEqual(code, 0, out)
        self.assertEqual([g for g in git if g in ('commit', 'push', 'pull')], ['commit', 'push', 'pull'])
        self.assertIn('skipped: main moved', out)

    def test_second_rejection_fails(self):
        code, out, _ = self.run_main(self.runner(push_codes=(1, 1)), '--expect', self.expected(), '--commit')
        self.assertEqual(code, 1)
        self.assertIn('[ERROR]', out)

    def test_already_pinned_commits_nothing(self):
        runner = self.runner()
        self.run_main(runner, '--expect', self.expected(), '--commit')
        code, out, git = self.run_main(self.runner(), '--expect', self.expected(), '--commit')
        self.assertEqual(code, 0, out)
        self.assertIn('already pinned', out)
        self.assertNotIn('commit', git)


def _result(argv, stdout, code=0):
    return Result(tuple(argv), code, stdout, '')


if __name__ == '__main__':
    unittest.main()
