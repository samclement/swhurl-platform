"""Images built from this repo: pinned inputs that match CI, and a build context that admits no keys."""
import re
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]


DOCKERFILE = (ROOT / 'images/console/Dockerfile').read_text()


def arg(name: str) -> str:
    return re.search(rf'^ARG {name}=(\S+)$', DOCKERFILE, re.M).group(1)


class ConsoleImageTests(unittest.TestCase):
    def test_helm_matches_ci(self):
        """The console reaches the API server itself, so the image carries no kubectl: only helm's pin is CI's."""
        steps = yaml.safe_load((ROOT / '.github/workflows/validate.yml').read_text())['jobs']['validate']['steps']
        ci = {s['uses'].split('@')[0]: s['with'].get('version') for s in steps if 'with' in s and 'uses' in s}
        self.assertEqual(arg('HELM_VERSION'), ci['azure/setup-helm'])
        instructions = [line for line in DOCKERFILE.splitlines() if not line.lstrip().startswith('#')]
        self.assertFalse([line for line in instructions if 'kubectl' in line], 'no instruction installs kubectl')

    def test_every_download_and_base_image_is_pinned(self):
        for tool in ('HELM', 'SOPS'):
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


class ConsoleImageInputTests(unittest.TestCase):
    def test_content_tag_stops_without_printing_a_tag_if_an_input_is_missing(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / 'images').mkdir()
            script = root / 'images/content-tag.sh'
            shutil.copyfile(ROOT / 'images/content-tag.sh', script)
            subprocess.run(['git', 'init', '-q', str(root)], check=True)
            for image in ('console', 'backup'):
                result = subprocess.run(['bash', str(script), image], capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, '', 'a partial input hash must never become an image tag')

    def test_validate_tags_both_images_with_the_script(self):
        workflow = (ROOT / '.github/workflows/validate.yml').read_text()
        for image in ('console', 'backup'):
            self.assertIn(f'tag=$(bash images/content-tag.sh {image})', workflow)
        self.assertNotIn('rev-parse "HEAD:$p"', workflow, 'one computation of the content tag, the script')

    def test_dashboard_job_shares_the_published_image_without_console_credentials(self):
        controllers = yaml.safe_load((ROOT / 'platform/console/helmrelease.yaml').read_text())['spec']['values']['controllers']
        web, job = controllers['main'], controllers['dashboards']
        self.assertEqual(web['containers']['main']['image'], job['containers']['main']['image'])
        self.assertEqual(web['forceRename'], 'console', 'adding a controller must keep the live Deployment name')
        self.assertEqual(job['serviceAccount']['name'], 'dashboard-sync')
        self.assertNotIn('envFrom', job['containers']['main'])
        self.assertEqual(job['cronjob']['concurrencyPolicy'], 'Forbid')
        self.assertEqual(job['cronjob']['schedule'], '* * * * *')
        self.assertEqual(job['containers']['main']['command'], ['timeout', '50', 'swhurl-v2', 'clickstack-dashboards', '--cluster', '--in-cluster'])

    def test_dashboard_job_reads_only_its_named_secrets(self):
        docs = list(yaml.safe_load_all((ROOT / 'platform/console/dashboard-rbac.yaml').read_text()))
        roles = [d for d in docs if d['kind'] == 'Role' and d['metadata']['name'] == 'platform-dashboard-webhooks']
        self.assertEqual(len(roles), 2)
        for role, namespace, name in zip(roles, ('console', 'notify'), ('notification-ntfy', 'notify-alerts'), strict=True):
            self.assertEqual(role['metadata']['namespace'], namespace)
            self.assertEqual(role['rules'], [{'apiGroups': [''], 'resources': ['secrets'], 'resourceNames': [name], 'verbs': ['get']}])
            binding = next(d for d in docs if d['kind'] == 'RoleBinding' and d['metadata']['name'] == role['metadata']['name']
                           and d['metadata']['namespace'] == namespace)
            self.assertEqual(binding['roleRef'], {'apiGroup': 'rbac.authorization.k8s.io', 'kind': 'Role', 'name': role['metadata']['name']})
            self.assertEqual(binding['subjects'], [{'kind': 'ServiceAccount', 'name': 'dashboard-sync', 'namespace': 'console'}])
        secret_rules = [rule for d in docs if d['kind'] in ('Role', 'ClusterRole') for rule in d['rules'] if 'secrets' in rule['resources']]
        self.assertEqual(len(secret_rules), 3)  # the two webhook credentials and ClickStack's
        self.assertTrue(all(rule['verbs'] == ['get'] and rule['resourceNames'] for rule in secret_rules))


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
        self.assertIn('host/push-main.sh console-image', pin['run'])
        self.assertIn('--expect "src-${{ needs.console-image.outputs.content }}"', pin['run'])
        uses = {step['uses'].split('@')[0] for step in steps if 'uses' in step}
        self.assertLessEqual({'azure/setup-kubectl', 'azure/setup-helm', 'actions/setup-go'}, uses,
                             'host/push-main.sh runs check-repo check-apps, which build swhurl-v2')
        self.assertNotIn('actions/setup-python', uses, 'the pin and its checks run no Python')

if __name__ == '__main__':
    unittest.main()
