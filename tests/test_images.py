"""Images built from this repo: pinned inputs that match CI, and a build context that admits no keys."""
import difflib
import io
import json
import re
import subprocess
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

import yaml

from swhurl import ROOT, images, platform
from swhurl.run import FakeRunner, Runner

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


if __name__ == '__main__':
    unittest.main()
