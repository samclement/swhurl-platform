"""Images built from this repo: pinned inputs that match CI, and a build context that admits no keys."""
import re
import unittest

import yaml

from swhurl import ROOT

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
        for tool in ('KUBECTL', 'HELM', 'FLUX', 'SOPS'):
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


if __name__ == '__main__':
    unittest.main()
