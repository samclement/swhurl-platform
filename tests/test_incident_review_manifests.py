"""The incident reviewer's manifests: each credential reaches only the container the plan names."""
from __future__ import annotations

import re
import shutil
import tempfile
import unittest
from pathlib import Path

import yaml

from swhurl import ROOT, images, platform
from swhurl.incident_review import allowlist
from swhurl.run import FakeRunner, Result

DIR = ROOT / 'platform/incident-review'
RELEASE_TEXT = (DIR / 'helmrelease.yaml').read_text()
VALUES = yaml.safe_load(RELEASE_TEXT)['spec']['values']
CONTROLLER = VALUES['controllers']['main']
CONTAINERS = {**CONTROLLER['initContainers'], **CONTROLLER['containers']}
SERVICE_ACCOUNT_PATH = '/var/run/secrets/kubernetes.io/serviceaccount'


def mounts(volume):
    return {name: entries for name, entries in VALUES['persistence'][volume]['advancedMounts']['main'].items()}


class ReviewerReleaseTests(unittest.TestCase):
    def test_three_containers_run_in_order_with_time_limits(self):
        self.assertEqual(list(CONTROLLER['initContainers']), ['collect', 'analyse'])
        self.assertEqual(CONTROLLER['initContainers']['analyse']['dependsOn'], 'collect')
        self.assertEqual(list(CONTROLLER['containers']), ['main'])
        self.assertEqual(CONTAINERS['collect']['command'],
                         ['timeout', '120', 'python', '-m', 'swhurl', 'incident-review-collect'])
        self.assertEqual(CONTAINERS['main']['command'],
                         ['timeout', '60', 'python', '-m', 'swhurl', 'incident-review-decide'])
        self.assertNotIn('command', CONTAINERS['analyse'])
        job = CONTROLLER['cronjob']
        self.assertEqual((job['concurrencyPolicy'], job['backoffLimit'], job['activeDeadlineSeconds']),
                         ('Forbid', 0, 600))
        self.assertGreaterEqual(job['activeDeadlineSeconds'], 120 + int(CONTAINERS['analyse']['env']['ANALYSE_SECONDS']) + 60)

    def test_service_account_token_reaches_collect_and_decide_only(self):
        self.assertIs(VALUES['defaultPodOptions']['automountServiceAccountToken'], False)
        self.assertEqual(set(mounts('token')), {'collect', 'main'})
        for entries in mounts('token').values():
            self.assertEqual(entries, [{'path': SERVICE_ACCOUNT_PATH, 'readOnly': True}])
        for name, volume in VALUES['persistence'].items():
            self.assertNotIn('globalMounts', volume, f'{name} must name the containers it is mounted into')

    def test_each_secret_reaches_one_container(self):
        self.assertEqual({name: [e['secretRef']['name'] for e in c.get('envFrom', [])]
                          for name, c in CONTAINERS.items()},
                         {'collect': ['incident-review-clickhouse'], 'analyse': [], 'main': ['incident-review-ntfy']})
        self.assertEqual(VALUES['persistence']['openai']['name'], 'incident-review-openai')
        self.assertEqual(mounts('openai'), {'analyse': [{'path': '/run/secrets/openai', 'readOnly': True}]})
        secrets = [name for name, volume in VALUES['persistence'].items() if volume['type'] == 'secret']
        self.assertEqual(secrets, ['openai'])
        for name, container in CONTAINERS.items():
            for value in (container.get('env') or {}).values():
                self.assertNotIsInstance(value, dict, f'{name} must not read a Secret through env valueFrom')

    def test_handoff_volumes_have_one_writer(self):
        writers = {'collect': 'collect', 'bundle': 'collect', 'diagnosis': 'analyse'}
        for volume, writer in writers.items():
            for container, entries in mounts(volume).items():
                self.assertEqual(entries[0].get('readOnly', False), container != writer, (volume, container))
        self.assertNotIn('analyse', mounts('collect'), 'the worker never sees the collect report')
        self.assertEqual(set(mounts('scratch')), {'analyse'})

    def test_containers_are_locked_down_and_bounded(self):
        for name, container in CONTAINERS.items():
            context = container['securityContext']
            self.assertEqual((context['allowPrivilegeEscalation'], context['readOnlyRootFilesystem'],
                              context['capabilities']), (False, True, {'drop': ['ALL']}), name)
            self.assertIn('memory', container['resources']['limits'], name)
        self.assertTrue(VALUES['defaultPodOptions']['securityContext']['runAsNonRoot'])

    def test_model_matches_the_allowlist(self):
        self.assertEqual(CONTAINERS['analyse']['env']['CODEX_MODEL'], allowlist.current().model)
        self.assertTrue(allowlist.current().model)

    def test_rbac_is_state_and_helmrelease_list_only(self):
        rules = [(doc['kind'], rule) for doc in yaml.safe_load_all((DIR / 'rbac.yaml').read_text())
                 if doc['kind'] in ('Role', 'ClusterRole') for rule in doc['rules']]
        self.assertEqual(rules, [
            ('Role', {'apiGroups': [''], 'resources': ['configmaps'], 'resourceNames': ['incident-review-state'],
                      'verbs': ['get', 'patch']}),
            ('ClusterRole', {'apiGroups': ['helm.toolkit.fluxcd.io'], 'resources': ['helmreleases'],
                             'verbs': ['list']})])

    def test_network_policy_selects_only_the_reviewer_and_admits_nothing_in(self):
        policy = yaml.safe_load((DIR / 'networkpolicy.yaml').read_text())['spec']
        self.assertEqual(policy['podSelector']['matchLabels'],
                         {'app.kubernetes.io/instance': 'incident-review', 'app.kubernetes.io/name': 'incident-review'})
        self.assertEqual((policy['policyTypes'], policy['ingress']), (['Ingress', 'Egress'], []))
        ports = sorted({port['port'] for rule in policy['egress'] for port in rule['ports']})
        self.assertEqual(ports, [53, 443, 6443, 8123])
        for rule in policy['egress']:
            for peer in rule['to']:
                if 'ipBlock' in peer and peer['ipBlock']['cidr'] == '0.0.0.0/0':
                    self.assertEqual(peer['ipBlock']['except'], ['10.42.0.0/16', '10.43.0.0/16'])

    def test_state_configmap_is_left_to_the_reviewer(self):
        state = yaml.safe_load((DIR / 'state.yaml').read_text())
        self.assertEqual(state['metadata']['annotations'], {'kustomize.toolkit.fluxcd.io/ssa': 'Merge'})
        self.assertNotIn('data', state)

    def test_unit_is_separate_decrypts_and_alerts_on_failure(self):
        units = {doc['metadata']['name']: doc for doc in
                 yaml.safe_load_all((ROOT / 'clusters/home/platform.yaml').read_text()) if doc}
        unit = units['platform-incident-review']
        self.assertEqual(unit['spec']['path'], './platform/incident-review')
        self.assertEqual(unit['metadata']['labels'], {platform.label('alert'): 'failures'})
        self.assertEqual(unit['spec']['decryption']['provider'], 'sops')
        self.assertNotIn('postBuild', unit['spec'])
        self.assertNotIn('${', RELEASE_TEXT)


class ReviewerImagePinTests(unittest.TestCase):
    TAG, DIGEST = 'src-1111111111111111', 'sha256:' + 'c' * 64

    def test_release_marks_both_images(self):
        for marker in ('console', 'worker'):
            self.assertEqual(len(re.findall(rf'^\s+tag: \S+ # image: {marker}$', RELEASE_TEXT, re.M)), 1)
            self.assertEqual(len(re.findall(rf'^\s+digest: sha256:[0-9a-f]{{64}} # image: {marker}$', RELEASE_TEXT, re.M)), 1)
        self.assertEqual(CONTAINERS['collect']['image'], CONTAINERS['main']['image'])
        self.assertEqual(CONTAINERS['collect']['image']['repository'], f'{images.REGISTRY}/{images.CONSOLE.repository}')
        self.assertEqual(CONTAINERS['analyse']['image']['repository'], f'{images.REGISTRY}/{images.WORKER.repository}')

    def test_marked_pin_changes_only_its_own_lines(self):
        pinned = images.pin(RELEASE_TEXT, self.TAG, self.DIGEST, 'worker', images.REVIEWER)
        values = yaml.safe_load(pinned)['spec']['values']['controllers']['main']
        self.assertEqual((values['initContainers']['analyse']['image']['tag'],
                          values['initContainers']['analyse']['image']['digest']), (self.TAG, self.DIGEST))
        self.assertEqual(values['containers']['main']['image'], CONTAINERS['main']['image'])
        self.assertIn(f'tag: {self.TAG} # image: worker', pinned)
        with self.assertRaisesRegex(images.ImageError, 'marked'):
            images.pin(RELEASE_TEXT, self.TAG, self.DIGEST, 'other', images.REVIEWER)

    def test_workflow_hashes_the_same_inputs_as_the_tooling(self):
        workflow = (ROOT / '.github/workflows/publish-incident-review-worker.yml').read_text()
        listed = re.search(r'paths=\(([^)]*)\)', workflow).group(1).split()
        self.assertEqual(tuple(listed), platform.WORKER_IMAGE_INPUTS)
        self.assertIn('swhurl worker-image', workflow)
        self.assertIn('IMAGE: ghcr.io/samclement/swhurl-incident-review-worker', workflow)
        for path in platform.WORKER_IMAGE_INPUTS:
            self.assertTrue((ROOT / path).exists(), path)

    def runner(self, root):
        git = ('git', '-C', str(root))
        head = f'HTTP/2 200\r\ndocker-content-digest: {self.DIGEST}\r\n'
        return (FakeRunner().on('curl', '--silent', '--show-error', '--fail', stdout='{"token": "anon"}')
                .on('curl', '--silent', '--show-error', '--head', stdout=head)
                .on(*git, 'status', stdout='')
                .on(*git, 'rev-parse', handler=lambda argv, _i: Result(tuple(argv), 0, '1' * 40 + '\n', ''))
                .on(*git, 'commit').on(*git, 'push'))

    def checkout(self, *, reviewer=True):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root)
        for relative in (images.RELEASE, *([images.REVIEWER] if reviewer else [])):
            (root / relative).parent.mkdir(parents=True)
            (root / relative).write_text((ROOT / relative).read_text())
        return root

    def test_console_pin_also_moves_the_reviewers_operator_image(self):
        root = self.checkout()
        runner = self.runner(root)
        tag, digest, changed = images.pin_release(runner, root)
        self.assertEqual(changed, [images.RELEASE, images.REVIEWER])
        reviewer = (root / images.REVIEWER).read_text()
        self.assertIn(f'tag: {tag} # image: console', reviewer)
        self.assertIn(f'digest: {digest} # image: console', reviewer)
        self.assertIn(re.search(r'^\s+tag: \S+ # image: worker$', RELEASE_TEXT, re.M).group(0), reviewer)
        self.assertEqual(images.commit_and_push(runner, root, tag, None, images.CONSOLE, changed), 'pushed')
        commit = next(call for call in runner.calls if 'commit' in call)
        self.assertEqual(commit[-2:], (str(images.RELEASE), str(images.REVIEWER)))

    def test_console_pin_works_without_a_reviewer_manifest(self):
        root = self.checkout(reviewer=False)
        self.assertEqual(images.pin_release(self.runner(root), root)[2], [images.RELEASE])

    def test_worker_pin_touches_only_the_reviewer(self):
        root = self.checkout()
        runner = self.runner(root)
        self.assertEqual(images.worker_main(['--expect', images.content_tag(runner, root, image=images.WORKER),
                                             '--commit'], runner, root), 0)
        self.assertEqual((root / images.RELEASE).read_text(), (ROOT / images.RELEASE).read_text())
        self.assertIn(f'digest: {self.DIGEST} # image: worker', (root / images.REVIEWER).read_text())
        commit = next(call for call in runner.calls if 'commit' in call)
        self.assertIn('deploy: incident review worker image', commit[commit.index('-m') + 1])
        self.assertEqual(commit[-1], str(images.REVIEWER))
        token = next(call for call in runner.calls if '--fail' in call)
        self.assertIn('samclement/swhurl-incident-review-worker', token[-1])


if __name__ == '__main__':
    unittest.main()
