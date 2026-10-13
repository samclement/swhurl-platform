"""A PR artifact is deployable only when its inputs and tested runtime match main."""
import io
import json
import shutil
import tarfile
import tempfile
import unittest
from datetime import UTC, datetime, timedelta
from pathlib import Path

from swhurl.apps import build_image as build
from swhurl.run import CommandError, FakeRunner, Result

NOW = datetime(2026, 10, 13, 12, tzinfo=UTC)
REPO = 'samclement/demo-shop'
RECIPE = 'samclement/swhurl-app-template-typescript@v0.2.3'
BASE = 'sha256:' + 'a' * 64
BUILDER = 'sha256:' + 'b' * 64


def oci_image(path):
    """A real OCI tar, including an attestation descriptor alongside the runtime."""
    documents = {}

    def blob(value):
        data = json.dumps(value, separators=(',', ':')).encode()
        digest = 'sha256:' + build.hashed(data)
        documents['blobs/sha256/' + digest.removeprefix('sha256:')] = data
        return digest

    config = blob({'os': 'linux', 'architecture': 'amd64', 'config': {}})
    runtime = blob({'schemaVersion': 2, 'config': {'digest': config}, 'layers': []})
    root = blob({'mediaType': build.INDEX, 'schemaVersion': 2, 'manifests': [
        {'digest': runtime, 'platform': {'os': 'linux', 'architecture': 'amd64'}},
        {'digest': 'sha256:' + 'd' * 64, 'platform': {'os': 'unknown', 'architecture': 'unknown'}},
    ]})
    documents['index.json'] = json.dumps({'schemaVersion': 2, 'manifests': [{'digest': root}]}).encode()
    with tarfile.open(path, 'w') as archive:
        for name, data in documents.items():
            member = tarfile.TarInfo(name)
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))
    return root, config


class ReuseTests(unittest.TestCase):
    def setUp(self):
        self.work = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.work)
        self.root = self.work / 'app'
        self.root.mkdir()
        (self.root / 'Dockerfile').write_text('FROM node:24 AS deps\nFROM deps AS check\nFROM scratch AS runtime\n')
        self.event = self.work / 'event.json'
        self.event.write_text(json.dumps({'pull_request': {'head': {'repo': {'full_name': REPO}},
                                                         'base': {'ref': 'main'}}}))
        self.env = {'GITHUB_REPOSITORY': REPO, 'GITHUB_EVENT_NAME': 'pull_request',
                    'GITHUB_EVENT_PATH': str(self.event), 'GITHUB_REF': 'refs/pull/1/merge', 'GITHUB_RUN_ID': '123'}
        self.tree = '1' * 40
        self.base = BASE

    def runner(self):
        def inspect(args, _input):
            return Result(args, stdout=(BUILDER if args[4] == build.BUILDKIT else self.base) + '\n')
        return (FakeRunner()
                .on('git', '-C', str(self.root), 'status', stdout='')
                .on('git', '-C', str(self.root), 'rev-parse', stdout=self.tree)
                .on('gh', 'api', 'repos/samclement/swhurl-app-template-typescript/contents/.github/workflows/app.yml?ref=v0.2.3',
                    stdout=json.dumps({'sha': '2' * 40}))
                .on('docker', 'buildx', 'imagetools', 'inspect', handler=inspect))

    def prepare(self, runner=None, recipe=RECIPE):
        work = self.work / 'state'
        work.mkdir(exist_ok=True)
        return build.prepare(runner or self.runner(), self.root, recipe, 'runtime', work, self.env, NOW)

    def retained(self):
        state = self.prepare()
        folder = self.work / 'retained'
        folder.mkdir()
        image = folder / 'image.oci.tar'
        digest, config = oci_image(image)
        runner = FakeRunner().on('docker', 'inspect', stdout=config)
        proof = build.retain(runner, state, image, folder / 'proof.json', self.env)
        return state, folder, proof, digest

    def candidate_runner(self, folder, state, *, run=None, artifact=None, edit=None):
        run = run or {'id': 123, 'event': 'pull_request', 'status': 'completed', 'conclusion': 'success',
                      'head_repository': {'full_name': REPO}}
        artifact = artifact or {'name': 'swhurl-tested-' + state['key'], 'expired': False,
                                'created_at': (NOW - timedelta(minutes=3)).isoformat()}

        def download(args, _input):
            destination = Path(args[-1])
            shutil.copytree(folder, destination, dirs_exist_ok=True)
            if edit:
                edit(destination)
            return Result(args)

        def api(args, _input):
            value = {'workflow_runs': [run]} if '/actions/workflows/' in args[2] else {'artifacts': [artifact]}
            return Result(args, stdout=json.dumps(value))

        return (self.runner().on('gh', 'api', handler=api)
                .on('gh', 'run', 'download', handler=download))

    def main_env(self):
        self.env.update(GITHUB_EVENT_NAME='push', GITHUB_REF='refs/heads/main')

    def test_same_tree_after_squash_merge_reuses_the_tested_oci_digest(self):
        state, folder, _proof, digest = self.retained()
        self.main_env()
        runner = self.candidate_runner(folder, state)
        result = self.prepare(runner)
        self.assertTrue(result['reuse'])
        self.assertEqual(result['image_digest'], digest)
        self.assertEqual(result['candidate_run'], 123)
        self.assertEqual(build.archive_image(Path(result['archive']))[0], digest)

    def test_base_images_are_pinned_and_stage_aliases_are_preserved(self):
        state = self.prepare()
        self.assertIn('FROM node:24@' + BASE, Path(state['dockerfile']).read_text())
        self.assertIn('FROM deps AS check', Path(state['dockerfile']).read_text())
        self.assertEqual(state['builder_image'], build.BUILDKIT + '@' + BUILDER)

    def test_source_and_mutable_base_changes_invalidate_artifacts(self):
        state, folder, _proof, _digest = self.retained()
        self.main_env()
        for changed in ('tree', 'base'):
            self.tree, self.base = '1' * 40, BASE
            if changed == 'tree':
                self.tree = '3' * 40
            else:
                self.base = 'sha256:' + 'c' * 64
            with self.subTest(changed=changed):
                result = self.prepare(self.candidate_runner(folder, state))
                self.assertFalse(result['reuse'])
                self.assertNotEqual(result['key'], state['key'])

    def test_failed_or_fork_runs_are_not_downloaded(self):
        state, folder, _proof, _digest = self.retained()
        self.main_env()
        runs = [
            {'id': 123, 'event': 'pull_request', 'status': 'completed', 'conclusion': 'failure',
             'head_repository': {'full_name': REPO}},
            {'id': 123, 'event': 'pull_request', 'status': 'completed', 'conclusion': 'success',
             'head_repository': {'full_name': 'stranger/demo-shop'}},
            {'id': 123, 'event': 'push', 'status': 'completed', 'conclusion': 'success',
             'head_repository': {'full_name': REPO}},
        ]
        for run in runs:
            with self.subTest(run=run):
                runner = self.candidate_runner(folder, state, run=run)
                self.assertFalse(self.prepare(runner)['reuse'])
                self.assertFalse(any(call[:3] == ('gh', 'run', 'download') for call in runner.calls))

    def test_expired_or_old_artifacts_are_not_downloaded(self):
        state, folder, _proof, _digest = self.retained()
        self.main_env()
        for expired, age in ((True, timedelta(minutes=3)), (False, timedelta(days=2))):
            artifact = {'name': 'swhurl-tested-' + state['key'], 'expired': expired,
                        'created_at': (NOW - age).isoformat()}
            runner = self.candidate_runner(folder, state, artifact=artifact)
            self.assertFalse(self.prepare(runner)['reuse'])
            self.assertFalse(any(call[:3] == ('gh', 'run', 'download') for call in runner.calls))

    def test_corrupt_archive_or_forged_test_proof_forces_a_rebuild(self):
        state, folder, _proof, _digest = self.retained()
        self.main_env()

        def corrupt(path):
            with (path / 'image.oci.tar').open('ab') as stream:
                stream.write(b'changed')

        def forge(path):
            proof = json.loads((path / 'proof.json').read_text())
            proof['tested_config'] = 'sha256:' + 'f' * 64
            (path / 'proof.json').write_text(json.dumps(proof))

        for edit in (corrupt, forge):
            result = self.prepare(self.candidate_runner(folder, state, edit=edit))
            self.assertFalse(result['reuse'])
            self.assertIn('ordinary build', result['reason'])

    def test_fork_pr_cannot_retain_and_manual_runs_do_not_reuse(self):
        self.event.write_text(json.dumps({'pull_request': {'head': {'repo': {'full_name': 'stranger/demo-shop'}},
                                                         'base': {'ref': 'main'}}}))
        self.assertFalse(self.prepare()['retain'])
        self.env.update(GITHUB_EVENT_NAME='workflow_dispatch', GITHUB_REF='refs/heads/main')
        self.assertFalse(self.prepare()['reuse'])

    def test_unsupported_frontends_and_dynamic_froms_fall_back(self):
        for text in ('# syntax=docker/dockerfile:1\nFROM node:24\n', 'ARG BASE\nFROM ${BASE} AS runtime\n'):
            (self.root / 'Dockerfile').write_text(text)
            state = self.prepare()
            self.assertFalse(state['retain'])
            self.assertFalse(state['reuse'])

    def test_the_archive_must_contain_the_actual_smoke_tested_runtime(self):
        state = self.prepare()
        archive = self.work / 'image.oci.tar'
        oci_image(archive)
        runner = FakeRunner().on('docker', 'inspect', stdout='sha256:' + 'f' * 64)
        with self.assertRaisesRegex(build.Rebuild, 'smoke-tested'):
            build.retain(runner, state, archive, self.work / 'proof.json', self.env)
        self.assertFalse((self.work / 'proof.json').exists())

    def test_publication_preserves_digest_and_never_rebuilds_or_runs_the_candidate(self):
        state, folder, _proof, digest = self.retained()
        self.main_env()
        state = self.prepare(self.candidate_runner(folder, state))
        runner = (FakeRunner().on('docker', 'run', stdout='copied')
                  .on('docker', 'buildx', 'imagetools', 'inspect', stdout=digest))
        result = build.publish(runner, state, f'ghcr.io/{REPO}:8-abcdef0', {})
        self.assertEqual(result, digest)
        self.assertIn('--preserve-digests', runner.calls[0])
        self.assertFalse(any(call[:3] == ('docker', 'buildx', 'build') for call in runner.calls))
        self.assertNotIn('app:smoke', runner.calls[0])

    def test_a_copy_failure_or_changed_digest_fails_publication(self):
        state, folder, _proof, _digest = self.retained()
        self.main_env()
        state = self.prepare(self.candidate_runner(folder, state))
        runner = FakeRunner().on('docker', 'run', returncode=1, stderr='could not copy')
        with self.assertRaises(CommandError):
            build.publish(runner, state, f'ghcr.io/{REPO}:8-abcdef0', {})
        runner = (FakeRunner().on('docker', 'run')
                  .on('docker', 'buildx', 'imagetools', 'inspect', stdout='sha256:' + 'f' * 64))
        with self.assertRaisesRegex(build.Rebuild, 'digest differs'):
            build.publish(runner, state, f'ghcr.io/{REPO}:8-abcdef0', {})
        with self.assertRaisesRegex(build.Rebuild, 'destination'):
            build.publish(runner, state, 'ghcr.io/stranger/app:8-abcdef0', {})

    def test_archive_changed_after_verification_is_not_published(self):
        state, folder, _proof, _digest = self.retained()
        self.main_env()
        state = self.prepare(self.candidate_runner(folder, state))
        with Path(state['archive']).open('ab') as stream:
            stream.write(b'changed')
        runner = FakeRunner()
        with self.assertRaisesRegex(build.Rebuild, 'changed before publication'):
            build.publish(runner, state, f'ghcr.io/{REPO}:8-abcdef0', {})
        self.assertEqual(runner.calls, [])
