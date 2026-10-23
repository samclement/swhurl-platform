"""Images built from this repo: pinned inputs that match CI, and a build context that admits no keys."""
import difflib
import io
import json
import re
import shutil
import subprocess
import sys
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
    def setUp(self):
        self.jobs = yaml.safe_load((ROOT / '.github/workflows/validate.yml').read_text())['jobs']

    def test_builds_on_pushes_only_alongside_the_checks(self):
        build = self.jobs['console-image']
        self.assertNotIn('needs', build, 'built alongside the checks, not after them')
        self.assertEqual(build['if'], "github.event_name == 'push'", 'pull requests publish nothing')
        self.assertEqual(build['permissions'], {'contents': 'read', 'packages': 'write'})

    def test_deploys_only_after_every_check_passed(self):
        deploy = self.jobs['deploy-console']
        self.assertEqual(set(deploy['needs']), set(self.jobs) - {'deploy-console'},
                         'a new check job must hold the deploy too')
        for condition in ("!contains(needs.*.result, 'failure')", "needs.console-image.result == 'success'",
                          "github.event_name == 'push'", '!cancelled()'):
            self.assertIn(condition, deploy['if'])
        self.assertEqual(deploy['permissions'], {'contents': 'write'})

    def test_pins_the_built_content_on_the_latest_main(self):
        steps = self.jobs['deploy-console']['steps']
        self.assertEqual(steps[0]['with']['fetch-depth'], 0)
        pin = steps[-1]
        self.assertNotIn('if', pin, 'pin also when the image was already published (docs-only commits no-op)')
        self.assertIn('swhurl console-image', pin['run'])
        self.assertIn('--expect "src-${{ needs.console-image.outputs.content }}" --commit', pin['run'])
        uses = {step['uses'].split('@')[0] for step in steps if 'uses' in step}
        self.assertLessEqual({'azure/setup-kubectl', 'azure/setup-helm'}, uses, 'push_main runs check-repo check-apps')
        self.assertTrue(any('flux-schema' in step.get('run', '') for step in steps))


class WorkflowPinTests(unittest.TestCase):
    """console-image --expect/--commit as Validate's deploy job runs it: bots.push_main, with FakeRunner."""
    HEAD = 'HTTP/2 200\r\ndocker-content-digest: sha256:' + 'b' * 64 + '\r\n'

    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.root)
        (self.root / images.RELEASE).parent.mkdir(parents=True)
        (self.root / images.RELEASE).write_text((ROOT / images.RELEASE).read_text())
        self.ids = ['1' * 40]  # what `git rev-parse HEAD:<input>` answers on each fetched main, in turn
        self.fetches, self.pushes, self.worktrees, self.committed, self.rendered = 0, [], [], [], []

    def runner(self, push_codes=(0,), checks=0):
        codes = list(push_codes)

        def fetch(argv, _input):
            self.fetches += 1
            return _result(argv, '')

        def add(argv, _input):  # each attempt pins a fresh copy of main
            work = Path(argv[-2])
            (work / images.RELEASE).parent.mkdir(parents=True)
            (work / images.RELEASE).write_text((self.root / images.RELEASE).read_text())
            self.worktrees.append(work)
            return _result(argv, '')

        def ids(argv, _input):
            return _result(argv, self.ids[min(self.fetches, len(self.ids)) - 1] + '\n')

        def push(argv, _input):
            self.pushes.append(argv)
            return _result(argv, '', codes.pop(0) if codes else 0)

        return (FakeRunner()
                .on('curl', '--silent', '--show-error', '--fail', stdout=json.dumps({'token': 'anon-token'}))
                .on('curl', '--silent', '--show-error', '--head', stdout=self.HEAD)
                .on('git', 'fetch', handler=fetch)
                .on('git', 'rev-parse', 'origin/main', stdout='a' * 40)
                .on('git', 'worktree', 'add', handler=add)
                .on('git', 'worktree', 'remove')
                .on('git', '-C', handler=lambda argv, i: ids(argv, i) if 'rev-parse' in argv else _result(argv, ''))
                .on(sys.executable, '-m', 'swhurl', 'app-sync', handler=lambda argv, _i: self.rendered.append(argv) or _result(argv, ''))
                .on('make', returncode=checks, stdout='[BAD] policy: broken' if checks else '')
                .on('git', 'add')
                .on('git', 'write-tree', stdout='tree0')
                .on('git', 'commit-tree', handler=lambda argv, _i: self.committed.append(
                    (self.worktrees[-1] / images.RELEASE).read_text()) or _result(argv, 'c' * 40))
                .on('git', 'push', handler=push))

    def expected(self):
        return images.content_tag(FakeRunner().on('git', '-C', stdout=self.ids[0] + '\n'), self.root)

    def run_main(self, runner, *args):
        out = io.StringIO()
        with redirect_stdout(out), redirect_stderr(out):
            code = images.main(list(args), runner, self.root)
        return code, out.getvalue(), [c for c in runner.calls if c[:2] == ('git', 'commit-tree')]

    def test_pins_the_newest_main_checks_it_and_pushes_without_force(self):
        tag = self.expected()
        code, out, commits = self.run_main(self.runner(), '--expect', tag, '--commit')
        self.assertEqual(code, 0, out)
        self.assertEqual(commits[0][-2:], ('-m', f'deploy: console image {tag} (Validate)'))
        self.assertIn(f'tag: {tag}', self.committed[0])
        self.assertEqual((self.root / images.RELEASE).read_text(), (ROOT / images.RELEASE).read_text(), 'the checkout is left alone')
        self.assertEqual(self.pushes, [('git', 'push', 'origin', f'{"c" * 40}:refs/heads/main')])
        self.assertIn('[OK] pushed cccccccccccc to main', out)
        self.assertEqual(len(self.rendered), 1, 'an unrendered main would fail the checks: it is rendered with the pin')

    def test_does_nothing_when_main_builds_other_inputs(self):
        runner = self.runner()
        code, out, commits = self.run_main(runner, '--expect', 'src-0000000000000000', '--commit')
        self.assertEqual((code, commits, self.pushes), (0, [], []), out)
        self.assertIn('nothing to do', out)
        self.assertFalse([c for c in runner.calls if c[0] == 'make'], 'nothing changed: nothing to check')

    def test_a_lost_push_pins_again_on_the_newer_main(self):
        code, out, commits = self.run_main(self.runner(push_codes=(1, 0)), '--expect', self.expected(), '--commit')
        self.assertEqual((code, len(commits), len(self.pushes), self.fetches), (0, 2, 2, 2), out)
        self.assertIn('attempt 1 of 5', out)
        self.assertIn(f'tag: {self.expected()}', self.committed[1], 'the newer main carries the pin too')

    def test_main_moving_to_other_inputs_after_a_lost_push_pins_nothing(self):
        self.ids = ['1' * 40, '2' * 40]
        code, out, commits = self.run_main(self.runner(push_codes=(1,)), '--expect', self.expected(), '--commit')
        self.assertEqual((code, len(commits), len(self.pushes)), (0, 1, 1), out)
        self.assertIn('nothing to do: main now builds', out)

    def test_every_lost_push_and_a_failed_check_fail(self):
        for codes, checks, message, pushed in (((1,) * 5, 0, 'main kept moving', 5), ((0,), 2, '[BAD] policy: broken', 0)):
            with self.subTest(message=message):
                self.pushes.clear()
                code, out, _ = self.run_main(self.runner(push_codes=codes, checks=checks), '--expect', self.expected(), '--commit')
                self.assertEqual((code, len(self.pushes)), (1, pushed))
                self.assertIn(message, out)

    def test_already_pinned_commits_nothing(self):
        tag = self.expected()
        release = self.root / images.RELEASE
        release.write_text(images.pin(release.read_text(), tag, 'sha256:' + 'b' * 64))
        code, out, commits = self.run_main(self.runner(), '--expect', tag, '--commit')
        self.assertEqual((code, commits, self.pushes), (0, [], []), out)
        self.assertIn('already pinned', out)


def _result(argv, stdout, code=0):
    return Result(tuple(argv), code, stdout, '')


if __name__ == '__main__':
    unittest.main()
