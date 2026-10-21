"""Console changes through GitHub: form to tooling argv, download, commit, branch, PR; offline, with
FakeRunner for the tree's tooling and a fake GitHub API."""
import base64
import contextlib
import io
import json
import shutil
import sys
import tarfile
import tempfile
import unittest
import urllib.error
from dataclasses import replace
from pathlib import Path
from unittest import mock

import httpx
import yaml
from ci_changes import records_only
from starlette.testclient import TestClient

from swhurl import ROOT
from swhurl.apps import calling, channels, egress, new, ops, promotion, repo, teams
from swhurl.apps import edit as app_edit
from swhurl.apps.instance import Instance
from swhurl.apps.model import write as model_sync
from swhurl.apps.model.records import Release
from swhurl.apps.state import State
from swhurl.console import actions, changes, cluster, merge, server, stages
from swhurl.console import promotion as console_promotion
from swhurl.run import CommandError, FakeRunner, Result, Runner

TOKEN = 'github_pat_fixture_0123456789abcdef'
HEAD = 'abc1234' + '0' * 33
WHO = {'X-Auth-Request-Email': 'sam@swhurl.com', 'Origin': 'http://testserver'}
NEW = {'name': 'weather-api', 'type': 'web-app', 'stack': 'typescript', 'team': 'platform', 'exposure': 'authenticated-web', 'host': ''}
IMAGE = 'ghcr.io/me/hello:2.0@sha256:' + 'a' * 64
REVISION = 'main@sha1:' + HEAD
SAMPLE = ROOT / 'tests/fixtures/instance'  # frozen sample instance; see its README
MAIN = {'README.md': b'# repo\n', calling.SIGNING_KEYS_FILE.as_posix(): (ROOT / calling.SIGNING_KEYS_FILE).read_bytes()}
for env in ('staging', 'prod'):
    for path in (SAMPLE / 'apps/hello' / env).glob('*.yaml'):
        MAIN[path.relative_to(SAMPLE).as_posix()] = path.read_bytes()
    rel = f'apps/hello/{env}/helmrelease.yaml'
    doc = yaml.safe_load(MAIN[rel])
    doc['spec']['values']['controllers']['main']['containers']['main']['image'] = new.parse_image(
        IMAGE if env == 'staging' else 'ghcr.io/me/hello:1.0@sha256:' + 'b' * 64)
    MAIN[rel] = yaml.safe_dump(doc, sort_keys=False).encode()
    record = f'apps/hello/{env}/release.yaml'  # the Release record says the same image
    doc = yaml.safe_load(MAIN[record])
    doc['image'] = new.parse_image(IMAGE if env == 'staging' else 'ghcr.io/me/hello:1.0@sha256:' + 'b' * 64)
    MAIN[record] = yaml.safe_dump(doc, sort_keys=False).encode()
MAIN['apps/hello/app.yaml'] = (SAMPLE / 'apps/hello/app.yaml').read_bytes()
for rel in ('clusters/home/app-hello-staging.yaml', 'clusters/home/app-hello-prod.yaml'):
    MAIN[rel] = (SAMPLE / rel).read_bytes()
with tempfile.TemporaryDirectory() as tmp:  # rendered from its records, as main always is
    root = Path(tmp)
    for rel, data in MAIN.items():
        (root / rel).parent.mkdir(parents=True, exist_ok=True)
        (root / rel).write_bytes(data)
    model_sync.sync(root)
    MAIN = {path.relative_to(root).as_posix(): path.read_bytes() for path in sorted(root.rglob('*')) if path.is_file()}
PROMOTION = {'image': IMAGE}  # what the review page submits



ROUTES = channels.routes_configmap({'channels': {'payments-phone': 'ntfy', 'spare': 'ntfy'},
                                    'teams': {'payments': ['payments-phone']}})
"""The generated routes on main: two channels, one of them notified by team payments."""


class FakeGitHub:
    """GitHub's REST API for one repository whose main is ``MAIN``; records every request."""

    def __init__(self, fail: dict[str, tuple[int, str]] | None = None):
        self.requests: list[httpx.Request] = []
        self.fail = fail or {}
        self.also: dict[str, object] = {}  # further answers, by "METHOD /path"
        self.github = changes.GitHub('samclement/swhurl-platform', TOKEN, transport=httpx.MockTransport(self.handle))

    def tarball(self) -> bytes:
        out = io.BytesIO()
        with tarfile.open(fileobj=out, mode='w:gz') as tar:
            for path, data in MAIN.items():
                info = tarfile.TarInfo(f'samclement-swhurl-platform-abc1234/{path}')
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
        return out.getvalue()

    def handle(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        path = request.url.path.removeprefix('/repos/samclement/swhurl-platform')
        key = f'{request.method} {path}'
        if key in self.fail:
            code, message = self.fail[key]
            return httpx.Response(code, json={'message': message})
        answers = {
            'GET /git/ref/heads/main': {'object': {'sha': HEAD}},
            f'GET /git/commits/{HEAD}': {'tree': {'sha': 'tree0'}},
            'POST /git/blobs': {'sha': f'blob{len(self.sent("POST /git/blobs")) - 1}'},
            'POST /git/trees': {'sha': 'tree1'},
            'POST /git/commits': {'sha': 'def5678' + '0' * 33},
            'POST /git/refs': {'ref': 'x'},
            'GET /contents/apps/teams.yaml': {'content': base64.b64encode(
                b'teams:\n  platform: {description: The platform itself}\n  payments: {}\n').decode()},
            'GET /contents/platform/notify/routes.yaml': {'content': base64.b64encode(ROUTES.encode()).decode()},
            'POST /pulls': {'html_url': 'https://github.com/x/pull/7', 'number': 7},
            'POST /issues/7/labels': [{'name': 'auto-merge'}],
            'GET /pulls': [{'number': 7, 'title': '[console] apps: add weather-api staging', 'html_url': 'https://github.com/x/pull/7',
                            'head': {'ref': 'console/new-weather-api-staging-abc1234'}, 'created_at': '2026-10-01T07:00:00Z'},
                           {'number': 8, 'title': 'chore(deps): bump', 'html_url': 'https://github.com/x/pull/8',
                            'head': {'ref': 'renovate/x'}, 'created_at': '2026-10-01T07:00:00Z'}],
        }
        answers.update(self.also)
        if key == f'GET /tarball/{HEAD}' and key not in answers:  # built only when asked: compressing is the cost
            answers[key] = self.tarball()
        if key not in answers:
            return httpx.Response(404, json={'message': 'Not Found'})
        answer = answers[key]
        return httpx.Response(200, content=answer) if isinstance(answer, bytes) else httpx.Response(201, json=answer)

    def sent(self, key: str) -> list[dict]:
        return [json.loads(r.content) for r in self.requests if f'{r.method} {r.url.path}'.endswith(key.split(' ', 1)[1])
                and r.method == key.split(' ', 1)[0]]


def rendered(hash, digest, settings, records):
    """An instance a tree has records for and has rendered (stages.Target)."""
    return stages.Target(records, settings, digest, hash, digest, hash)


class StagingHistoryTests(unittest.TestCase):
    def test_complete_releases_from_main_first_parents_only(self):
        current = Release.parse(yaml.safe_load(MAIN['apps/hello/staging/release.yaml']))
        previous = replace(current, health_path='/previous')
        ancestor, missing, side = 'ancestor', 'missing', 'side'
        api = FakeGitHub()
        api.also.update({f'GET /git/commits/{HEAD}': {'parents': [{'sha': missing}, {'sha': side}]},
                         f'GET /git/commits/{missing}': {'parents': [{'sha': ancestor}]},
                         f'GET /git/commits/{ancestor}': {'parents': []}})
        # GitHub contents answers use the immutable ref, not the moving main branch.
        original = api.handle
        def answer(request):
            if request.url.params.get('ref') == ancestor:
                api.requests.append(request)
                return httpx.Response(200, json={'content': base64.b64encode(yaml.safe_dump(previous.document()).encode()).decode()})
            return original(request)
        api.github = replace(api.github, transport=httpx.MockTransport(answer))
        client = changes.GitHubAPI(FakeRunner(), api.github)
        self.addCleanup(client.client.close)
        self.assertTrue(promotion.staged(previous, changes.staging_history(client, HEAD, 'hello', current)))
        self.assertFalse(any(side in str(r.url) for r in api.requests), 'a side branch never authenticates a Release')
        for tampered in (replace(previous, image=replace(previous.image, tag='retagged')),
                         replace(previous, image=replace(previous.image, repository='ghcr.io/other/app')),
                         replace(previous, image=replace(previous.image, digest='sha256:' + 'e' * 64)),
                         replace(previous, telemetry=not previous.telemetry)):
            with self.subTest(release=tampered):
                self.assertFalse(promotion.staged(tampered, changes.staging_history(client, HEAD, 'hello', current)))

    def test_failed_history_read_prevents_publication(self):
        api = FakeGitHub(fail={f'GET /git/commits/{HEAD}': (503, 'history unavailable')})
        def unselected(tree):
            promoted(tree)
            path = tree / 'apps/hello/prod/release.yaml'
            doc = yaml.safe_load(path.read_text())
            doc['healthPath'] = '/different'
            path.write_text(yaml.safe_dump(doc))
        job = actions.Job(1, 'promote', 'hello/prod', 'sam@swhurl.com', None)
        commands = merge.ACTIONS['promote'].commands('hello', 'staging', {'image': IMAGE})
        with self.assertRaisesRegex(actions.ActionError, 'history unavailable'):
            changes.open_action(tree_fake(edit=unselected), api.github, job, 'promote', 'hello', 'staging', commands)
        self.assertTrue(all(r.method == 'GET' for r in api.requests))


class RenderTargetTests(unittest.TestCase):
    def test_complete_tree_reads_targets_without_turning_failed_reads_into_absence(self):
        api = FakeGitHub()
        targets = changes.render_targets(FakeRunner(), api.github, HEAD)
        self.assertIn('hello/staging', targets)
        self.assertEqual(targets['hello/staging'].digest, 'sha256:' + 'a' * 64)
        self.assertTrue(targets['hello/staging'].hash)
        self.assertFalse(targets['hello/staging'].pending, 'its HelmRelease runs the digest its records request')
        self.assertEqual(len(targets['hello/staging'].settings), 16, 'its instance.yaml and app.yaml, hashed')
        self.assertEqual(len(targets['hello/staging'].records), 16, 'its app.yaml, release.yaml and instance.yaml, hashed')
        self.assertNotEqual(targets['hello/staging'].records, targets['hello/prod'].records)
        self.assertNotIn('missing/staging', targets)
        records_only = {rel.replace('hello/prod', 'fresh/prod').replace('apps/hello/app', 'apps/fresh/app'): data
                        for rel, data in MAIN.items() if rel.endswith(('hello/prod/release.yaml', 'hello/prod/instance.yaml', 'hello/app.yaml'))}
        with mock.patch.dict(MAIN, records_only):
            fresh = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['fresh/prod']
        self.assertEqual((fresh.hash, fresh.digest, fresh.pending), (None, 'sha256:' + 'b' * 64, True),
                         'a merged first promotion: records, not yet rendered')
        self.assertTrue(fresh.records)
        api.fail[f'GET /tarball/{HEAD}'] = (403, 'rate limited')
        with self.assertRaises(actions.ActionError):
            changes.render_targets(FakeRunner(), api.github, HEAD)

    def test_settings_only_merge_waits_for_its_complete_render_and_not_the_old_healthy_output(self):
        unit = 'hello/prod'
        old = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)[unit]
        path = f'apps/{unit}/instance.yaml'
        doc = yaml.safe_load(MAIN[path])
        doc['replicas'] = 0
        pending = {**MAIN, path: yaml.safe_dump(doc).encode()}
        with mock.patch.dict(MAIN, pending, clear=True):
            merged = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)[unit]
        live = cluster.AppRow(Instance('hello', 'prod'), State('healthy'), False, 'x', digest=old.digest,
                              render_hash=old.hash, render_ready=True, render_state=State('healthy'), image_matches=True)
        self.assertEqual(merged.digest, old.digest, 'Settings did not change the image')
        self.assertEqual(merged.hash, old.hash, 'the old rendered output is still beside the records')
        self.assertNotEqual(merged.expected, old.expected, 'the complete intended render did change')
        self.assertEqual(stages.progress('scale', merged, merged, live).state, 'rendering')
        doc['replicas'] = 2
        with mock.patch.dict(MAIN, {**pending, path: yaml.safe_dump(doc).encode()}, clear=True):
            replacement = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)[unit]
        self.assertEqual(replacement.hash, merged.hash, 'both pending records sit beside the same stale output')
        self.assertEqual(stages.progress('scale', merged, replacement, live).state, 'superseded')
        self.assertEqual(stages.progress('scale', merged, replacement, live, 'earlier').state, 'replaced')
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for path, data in pending.items():
                (root / path).parent.mkdir(parents=True, exist_ok=True)
                (root / path).write_bytes(data)
            model_sync.sync(root)
            complete = {p.relative_to(root).as_posix(): p.read_bytes() for p in root.rglob('*') if p.is_file()}
        with mock.patch.dict(MAIN, complete, clear=True):
            current = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)[unit]
        self.assertFalse(current.pending)
        self.assertEqual(current.hash, current.expected)
        self.assertEqual(stages.progress('scale', merged, current, live).state, 'applying')
        stopped = replace(live, render_hash=current.hash, render_state=State('stopped'), state=State('stopped'))
        self.assertEqual(stages.progress('scale', merged, current, stopped).state, 'zero', 'Render completes the same change')

    def test_renderer_version_limitation_keeps_healthy_matching_digest_rendering_until_hashes_agree(self):
        unit = 'hello/prod'
        current = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)[unit]
        path = f'apps/{unit}/helmrelease.yaml'
        doc = yaml.safe_load(MAIN[path])
        # Same records, different renderer version's committed fingerprint. The running
        # console cannot prove that this healthy output is the render it would produce.
        doc['metadata']['annotations']['platform.swhurl.com/render'] = 'f' * 16
        live = cluster.AppRow(Instance('hello', 'prod'), State('healthy'), False, 'x', digest=current.digest,
                              render_hash='f' * 16, render_ready=True, render_state=State('healthy'), image_matches=True)
        runner = tree_fake()
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        jobs.submit('scale', unit, 'sam@swhurl.com',
                    lambda job: setattr(job, 'link', 'https://github.com/x/pull/1'))
        saved = mock.Mock()
        jobs.store = saved
        client = TestClient(app_under_test(runner, jobs=jobs, github=FakeGitHub().github))
        with mock.patch.dict(MAIN, {path: yaml.safe_dump(doc).encode()}), \
                mock.patch.object(changes, 'console_prs', return_value=[]), \
                mock.patch.object(changes, 'pull_state', return_value=('merged', '2026-10-21T10:00:00Z', HEAD)), \
                mock.patch.object(cluster, 'apps', return_value=[live]):
            skewed = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)[unit]
            self.assertEqual((skewed.records, skewed.settings), (current.records, current.settings))
            self.assertEqual(skewed.expected, current.expected)
            self.assertNotEqual(skewed.hash, skewed.expected)
            self.assertEqual(skewed.hash, live.render_hash)
            self.assertEqual(skewed.digest, skewed.rendered)
            self.assertEqual(skewed.digest, live.digest, 'digest equality must not bypass renderer evidence')
            self.assertTrue(skewed.pending)
            for reached in ('', 'previously-seen'):
                with self.subTest(reached=reached):
                    progress = stages.progress('scale', skewed, skewed, live, reached)
                    self.assertEqual(progress.state, 'rendering')
                    self.assertNotIn(progress.state, stages.REACHED)
            page = client.get('/jobs/1', headers=WHO).text
            activity = client.get('/activity', headers=WHO).text
            self.assertIn('waiting for the Render workflow', page)
            self.assertIn('waiting for the Render workflow', activity)
            self.assertEqual(jobs.get(1).reached, '')
            saved.save.assert_not_called()
        agreeing = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)[unit]
        self.assertEqual(agreeing.hash, agreeing.expected)
        self.assertFalse(agreeing.pending)
        matching_live = replace(live, render_hash=agreeing.hash)
        self.assertEqual(stages.progress('scale', skewed, agreeing, matching_live).state, 'live')

    def test_render_evidence_uses_public_inputs_and_encrypted_presence_not_secret_contents(self):
        path = 'apps/hello/staging/secret.sops.yaml'
        baseline = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/staging']
        with mock.patch.dict(MAIN, {path: b'opaque contents, never parsed'}):
            encrypted = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/staging']
        self.assertNotEqual(baseline.expected, encrypted.expected, 'encrypted presence affects the generated resources')
        with mock.patch.dict(MAIN, {path: b'different opaque contents'}):
            same = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/staging']
        self.assertEqual(encrypted, same, 'neither the reader nor its evidence consumes Secret values')
        with mock.patch.dict(MAIN, {calling.SIGNING_KEYS_FILE.as_posix(): b'{"keys": []}'}):
            keys = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/staging']
        self.assertNotEqual(baseline.expected, keys.expected, 'public signing keys are part of the complete input')
        self.assertTrue(keys.pending)
        with mock.patch.dict(MAIN, {calling.SIGNING_KEYS_FILE.as_posix(): b'not json'}):
            with self.assertRaises(actions.ActionError):
                changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)

    def test_records_only_removal_reaches_removed_after_render_and_live_absence(self):
        unit = 'hello/prod'
        merged = {path: text for path, text in MAIN.items()
                  if path not in (f'apps/{unit}/release.yaml', f'apps/{unit}/instance.yaml')}
        with mock.patch.dict(MAIN, merged, clear=True):
            target = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD).get(unit, stages.Target())
        self.assertEqual(target, stages.Target(), 'stale generated output is not the removal target')
        live = cluster.AppRow(Instance('hello', 'prod'), State('healthy'), False, 'x', render_state=State('healthy'))
        self.assertEqual(stages.progress('remove', target, target, live).state, 'removing')
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for path, text in merged.items():
                (root / path).parent.mkdir(parents=True, exist_ok=True)
                (root / path).write_bytes(text)
            model_sync.sync(root)
            rendered = {path.relative_to(root).as_posix(): path.read_bytes() for path in root.rglob('*') if path.is_file()}
        with mock.patch.dict(MAIN, rendered, clear=True):
            current = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD).get(unit, stages.Target())
        self.assertEqual(stages.progress('remove', target, current, live).state, 'removing', 'Flux is still uninstalling')
        self.assertEqual(stages.progress('remove', target, current, stages.ABSENT).state, 'removed')
        self.assertEqual(stages.progress('remove', target, current, None).state, 'unread', 'a failed cluster read is not absence')
        merged[f'apps/{unit}/helmrelease.yaml'] = b'invalid: ['
        with mock.patch.dict(MAIN, merged, clear=True):
            self.assertNotIn(unit, changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD),
                             'an absent instance does not require parsing its stale output')

    def test_malformed_output_is_a_failed_read_but_hashless_output_is_historical(self):
        path = 'apps/hello/prod/helmrelease.yaml'
        for content in (b'', b'not a mapping', b'invalid: ['):
            with self.subTest(content=content), mock.patch.dict(MAIN, {path: content}):
                with self.assertRaises(actions.ActionError):
                    changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)
        old = yaml.safe_load(MAIN[path])
        old['metadata']['annotations'].pop('platform.swhurl.com/render')
        with mock.patch.dict(MAIN, {path: yaml.safe_dump(old).encode()}):
            historical = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/prod']
        self.assertEqual(historical.hash, '')
        self.assertEqual(stages.progress('scale', historical, historical, None).state, 'historical')

    def test_partial_records_are_not_an_absent_render_target(self):
        for missing in ('release.yaml', 'instance.yaml'):
            with self.subTest(missing=missing), mock.patch.dict(MAIN, clear=False):
                del MAIN[f'apps/hello/prod/{missing}']
                with self.assertRaises(actions.ActionError):
                    changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)

    def test_held_later_promotion_waits_for_render_not_the_healthy_previous_image(self):
        record_path = 'apps/hello/prod/release.yaml'
        promoted = yaml.safe_load(MAIN[record_path])
        promoted['image'] = new.parse_image(IMAGE)
        # GitHub's merge changes only the records. Production's rendered HelmRelease and pod
        # still run the previous image until Render pushes its follow-up commit.
        with mock.patch.dict(MAIN, {record_path: yaml.safe_dump(promoted).encode()}):
            target = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/prod']
        old_hash = (yaml.safe_load(MAIN['apps/hello/prod/helmrelease.yaml'])['metadata']['annotations']
                    ['platform.swhurl.com/render'])
        self.assertEqual((target.hash, target.digest, target.pending), (old_hash, IMAGE.split('@')[1], True))
        live = cluster.AppRow(Instance('hello', 'prod'), State('healthy'), False, 'x', digest=target.rendered, render_hash=old_hash,
                              render_ready=True, render_state=State('healthy'), image_matches=True)
        progress = stages.progress('promote', target, target, live)
        self.assertEqual((progress.state, progress.detail), ('rendering', 'main has not rendered it yet'))
        self.assertNotIn(progress.state, stages.REACHED, 'the old image must not persist a first-seen time')


class NewAppRenderTargetTests(unittest.TestCase):
    def test_manual_records_merge_then_port_allocation_is_creation_completion_not_replacement(self):
        records = {path: data for path, data in MAIN.items() if path in (
            'apps/hello/app.yaml', 'apps/hello/staging/release.yaml', 'apps/hello/staging/instance.yaml', calling.SIGNING_KEYS_FILE.as_posix())}
        settings = yaml.safe_load(records['apps/hello/staging/instance.yaml'])
        settings.get('allocations', {}).pop('egressPort', None)
        records['apps/hello/staging/instance.yaml'] = yaml.safe_dump(settings).encode()
        with mock.patch.dict(MAIN, records, clear=True):
            before = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/staging']
        self.assertTrue(before.pending)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for path, data in records.items():
                (root / path).parent.mkdir(parents=True, exist_ok=True)
                (root / path).write_bytes(data)
            proxy = root / egress.PROXY_FILE
            proxy.parent.mkdir(parents=True)
            shutil.copyfile(ROOT / egress.PROXY_FILE, proxy)
            model_sync.sync(root)
            rendered = {p.relative_to(root).as_posix(): p.read_bytes() for p in root.rglob('*') if p.is_file()}
        self.assertIn('egressPort', yaml.safe_load(rendered['apps/hello/staging/instance.yaml'])['allocations'])
        with mock.patch.dict(MAIN, rendered, clear=True):
            current = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/staging']
        self.assertEqual(before.records, current.records, 'system port allocation is not a new request')
        self.assertEqual(before.settings, current.settings)
        def live(target):
            return cluster.AppRow(Instance('hello', 'staging'), State('healthy'), False, 'x', digest=target.digest,
                                  render_hash=target.hash, render_ready=True, render_state=State('healthy'), image_matches=True)
        for action in stages.NEW_APP_ACTIONS:
            with self.subTest(action=action):
                result = stages.progress(action, before, current, live(current))
                self.assertEqual(result.state, 'live')
                # Even if the first read after Render sees its own build rather than the seed,
                # the creation still follows it; a later operator setting does supersede it.
                built = replace(current, hash='built', expected='built', digest='own-build', rendered='own-build', records='new-release')
                result = stages.progress(action, before, built, live(built))
                self.assertEqual((result.state, result.newer), ('live', 'own-build'))
                changed = replace(current, hash='changed', settings='operator-change', records='operator-change')
                result = stages.progress(action, before, changed, live(changed))
                self.assertEqual(result.state, 'superseded')


class ConsolePrChecksTests(unittest.TestCase):
    def listed(self, *, labels=('auto-merge',), sha=HEAD, runs=(), mergeable='clean', fail=None, comments=(),
               branch='console/scale-hello-staging-abc1234'):
        requests = []
        pull = {'number': 7, 'title': 'apps: scale hello', 'html_url': 'https://github.com/x/pull/7',
                'head': {'ref': branch, 'sha': sha},
                'created_at': '2026-10-01T07:00:00Z', 'labels': [{'name': label} for label in labels]}

        def handle(request):
            path = request.url.path.removeprefix('/repos/samclement/swhurl-platform')
            requests.append(path)
            if path == fail:
                return httpx.Response(503, json={'message': 'Unavailable'})
            answers = {'/pulls': [pull], '/actions/runs': {'workflow_runs': list(runs)},
                       '/pulls/7': {'mergeable_state': mergeable}, '/issues/7/comments': list(comments)}
            return httpx.Response(200, json=answers[path])

        github = changes.GitHub('samclement/swhurl-platform', TOKEN, transport=httpx.MockTransport(handle))
        return changes.console_prs(FakeRunner(), github)[0], requests

    def test_validation_statuses_and_checks_urls(self):
        cases = (
            ('success', 'completed', 'passed'),
            ('failure', 'completed', 'failed'),
            ('cancelled', 'completed', 'failed'),
            ('timed_out', 'completed', 'failed'),
            ('action_required', 'completed', 'failed'),
            (None, 'in_progress', 'running'),
            (None, 'queued', 'pending'),
            ('neutral', 'completed', 'pending'),
            ('skipped', 'completed', 'pending'),
        )
        for conclusion, status, expected in cases:
            with self.subTest(conclusion=conclusion, status=status):
                run = {'name': 'Validate', 'head_sha': HEAD, 'conclusion': conclusion,
                       'status': status, 'html_url': 'https://github.com/x/actions/runs/9'}
                pr, requests = self.listed(runs=(run,))
                self.assertEqual((pr.checks, pr.checks_url, pr.refused), (expected, run['html_url'], ''))
                self.assertEqual(requests, ['/pulls', '/actions/runs', '/pulls/7'])

    def test_checks_are_read_whoever_merges_it_and_missing_sha_needs_no_checks(self):
        cases = (
            # A scale PR is the gate's: without its label, the gate's refusal (if any) is read too.
            ((), HEAD, 'pending', ['/pulls', '/actions/runs', '/pulls/7', '/issues/7/comments']),
            (('auto-merge',), '', '', ['/pulls']),
        )
        for labels, sha, checks, read in cases:
            with self.subTest(labels=labels, sha=sha):
                pr, requests = self.listed(labels=labels, sha=sha)
                self.assertEqual((pr.checks, pr.checks_url, pr.refused), (checks, pr.url + '/checks', ''))
                self.assertEqual(requests, read)

    def test_a_held_gate_pr_shows_the_gates_newest_reason_and_nobody_elses(self):
        """Without its label, a PR the gate takes shows why the gate held it, when it said so."""
        gate, person = {'login': 'github-actions[bot]'}, {'login': 'someone'}
        said = [{'user': gate, 'body': 'Auto-merge refused: production changed after review\n\nReview the changes.'},
                {'user': gate, 'body': 'Auto-merge refused: diff includes unrelated edits\n\nReview the changes.'},
                {'user': person, 'body': 'Auto-merge refused: <b>trust me</b>'}, {'user': gate, 'body': 'another note'}]
        cases = ((said, None, 'diff includes unrelated edits'), (said[2:], None, ''), ((), None, ''),
                 ([{'user': gate, 'body': 'Auto-merge refused: ' + 'x' * 400}], None, 'x' * 300),
                 (said, '/issues/7/comments', ''))
        for comments, fail, expected in cases:
            with self.subTest(expected=expected, fail=fail):
                pr, requests = self.listed(labels=(), comments=comments, fail=fail,
                                           branch='console/promote-hello-staging-abc1234')
                self.assertEqual(pr.refused, expected)
                self.assertEqual(requests, ['/pulls', '/actions/runs', '/pulls/7', '/issues/7/comments'])
        pr, requests = self.listed(labels=('auto-merge',), comments=said, branch='console/promote-hello-staging-abc1234')
        self.assertEqual((pr.refused, requests[-1]), ('', '/pulls/7'), 'a labelled PR is the gate\'s again: no comment read')
        pr, requests = self.listed(labels=(), comments=said, branch='console/remove-hello-staging-abc1234')
        self.assertEqual((pr.refused, requests[-1]), ('', '/pulls/7'), 'the gate never takes a held action: no comment read')

    def test_only_validate_for_this_sha_counts_and_first_matching_run_wins(self):
        run = {'name': 'Validate', 'head_sha': HEAD, 'conclusion': 'failure', 'html_url': 'matching'}
        runs = ({**run, 'head_sha': 'old', 'conclusion': 'success', 'html_url': 'wrong-sha'},
                {**run, 'name': 'Other', 'conclusion': 'success', 'html_url': 'wrong-name'},
                run, {**run, 'conclusion': 'success', 'html_url': 'older-match'})
        pr, _requests = self.listed(runs=runs)
        self.assertEqual((pr.checks, pr.checks_url), ('failed', 'matching'))
        pr, _requests = self.listed(runs=runs[:2])
        self.assertEqual((pr.checks, pr.checks_url), ('pending', pr.url + '/checks'))

    def test_conflict_overrides_validation_status(self):
        for conclusion in ('success', 'failure', None):
            with self.subTest(conclusion=conclusion):
                run = {'name': 'Validate', 'head_sha': HEAD, 'conclusion': conclusion, 'html_url': 'checks'}
                pr, _requests = self.listed(runs=(run,), mergeable='dirty')
                self.assertEqual((pr.checks, pr.checks_url), ('conflict', 'checks'))

    def test_read_failure_is_not_read_and_keeps_the_checks_url_available_at_that_point(self):
        run = {'name': 'Validate', 'head_sha': HEAD, 'conclusion': 'success', 'html_url': 'checks'}
        for fail, expected_url, expected_requests in (
            ('/actions/runs', 'https://github.com/x/pull/7/checks', ['/pulls', '/actions/runs']),
            ('/pulls/7', 'checks', ['/pulls', '/actions/runs', '/pulls/7']),
        ):
            with self.subTest(fail=fail):
                pr, requests = self.listed(runs=(run,), fail=fail)
                self.assertEqual((pr.checks, pr.checks_url), ('', expected_url), 'a failed read is not a verdict')
                self.assertEqual(requests, expected_requests)


class TreeRunner(FakeRunner):
    """A FakeRunner whose tooling commands edit the downloaded tree they run in, as app-new does."""

    def __init__(self, edit=None, **kwargs):
        super().__init__(**kwargs)
        self.edit = edit  # None: app-promote writes production's records, anything else a new file

    def _execute(self, argv, *, input, env, cwd):
        if cwd is not None and argv[:3] == (sys.executable, '-m', 'swhurl'):
            if self.edit is not None:
                self.edit(Path(cwd))
            elif argv[3:4] == ('app-promote',):
                promoted(Path(cwd))
            elif argv[3] in SETTINGS_COMMANDS:
                settings_edit(Path(cwd), argv[3:])
            elif argv[3:4] == ('app-new',):
                app = argv[4]
                for name in ('app.yaml', 'staging/release.yaml', 'staging/instance.yaml', 'staging/helmrelease.yaml'):
                    path = Path(cwd) / 'apps' / app / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(MAIN[f'apps/hello/{name}'])
                path = Path(cwd) / 'apps' / app / 'staging/instance.yaml'
                doc = yaml.safe_load(path.read_text())
                for flag in argv[5:]:
                    key, _, value = flag.removeprefix('--').partition('=')
                    if key in ('calls', 'subscribe'):
                        doc['calls' if key == 'calls' else 'subscriptions'] = value.split(',')
                path.write_text(yaml.safe_dump(doc))
            elif argv[3:4] == ('app-remove',):
                shutil.rmtree(Path(cwd) / 'apps' / argv[4] / argv[5])
            elif argv[3].startswith('team-'):
                path = Path(cwd) / 'apps/teams.yaml'
                path.write_text((path.read_text() if path.is_file() else 'teams: {}\n') + f'# {argv[3:]}\n')
            else:
                (Path(cwd) / 'apps/x').mkdir(parents=True, exist_ok=True)
                (Path(cwd) / 'apps/x/helmrelease.yaml').write_text('new\n')
        return super()._execute(argv, input=input, env=env, cwd=cwd)


SETTINGS_COMMANDS = {'app-scale': '', 'app-schedule': '', 'app-expose': '', 'app-calls': 'calls',
                     'app-events': 'subscriptions', 'app-egress': 'egress', 'app-allows': 'allows'}
"""The instance commands whose console PR carries only the instance's Settings, and the list each edits."""


def settings_edit(tree: Path, argv: tuple[str, ...]) -> None:
    """What an instance command writes that its PR carries: the instance's instance.yaml, changed by its flags."""
    command, app, env, *flags = argv
    path = tree / 'apps' / app / env / 'instance.yaml'
    doc = yaml.safe_load(path.read_text())
    for flag in flags:
        name, _, value = flag.removeprefix('--').partition('=')
        items = [item for item in value.split(',') if item]
        if name == 'replicas':
            doc['replicas'] = int(value)
        elif name in ('cpu', 'memory', 'memory-limit'):
            doc.setdefault('capacity', {})['memoryLimit' if name == 'memory-limit' else name] = value
        elif name in ('exposure', 'host', 'schedule'):
            doc[name] = value
        elif name.startswith('add'):
            doc[SETTINGS_COMMANDS[command]] = [*doc.get(SETTINGS_COMMANDS[command], []), *items]
        elif name.startswith('remove'):
            doc[SETTINGS_COMMANDS[command]] = [item for item in doc.get(SETTINGS_COMMANDS[command], []) if item not in items]
    path.write_text(yaml.safe_dump(doc, sort_keys=False))


def connected_main() -> dict[str, bytes]:
    """MAIN with hello/staging already calling orders and receiving orders.created: something to disconnect."""
    rel = 'apps/hello/staging/instance.yaml'
    doc = {**yaml.safe_load(MAIN[rel]), 'calls': ['orders'], 'subscriptions': ['orders.created']}
    return {rel: yaml.safe_dump(doc, sort_keys=False).encode()}


def promoted(tree: Path) -> None:
    """What app-promote writes that a promotion PR carries: production's Release with staging's image
    (and, for a first promotion, production's Settings)."""
    staging = yaml.safe_load((tree / 'apps/hello/staging/release.yaml').read_text())
    target = tree / 'apps/hello/prod/release.yaml'
    doc = yaml.safe_load(target.read_text()) if target.is_file() else dict(staging)
    doc['image'] = staging['image']
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(yaml.safe_dump(doc, sort_keys=False))
    settings = tree / 'apps/hello/prod/instance.yaml'
    if not settings.is_file():
        settings.write_text('version: 1\nexposure: authenticated-web\n')


APP_NEW_OK = Result((), 0, '[OK] wrote apps/weather-api/staging/helmrelease.yaml\n')


def staging_app(name, variables=None, port=8080):
    """A unit and release as the cluster lists them, for the apps the New app form offers."""
    values = {'controllers': {'main': {'containers': {'main': {'env': variables or {}}}}}}
    if port:
        values['service'] = {'main': {'ports': {'http': {'port': port}}}}
    ready = {'conditions': [{'type': 'Ready', 'status': 'True'}]}
    return ({'metadata': {'name': f'app-{name}-staging'}, 'spec': {}, 'status': ready},
            {'metadata': {'namespace': f'{name}-staging', 'name': name}, 'spec': {'values': values}})


def tree_fake(app_new=APP_NEW_OK, edit=None, staging=()):
    """``staging``: the staging_app()s that run (none by default)."""
    runner = TreeRunner(edit)
    # Only the three lists the New app form reads, and what the Teams page judges each app's state from;
    # any other kubectl call is still unexpected.
    for prefix, found in ((cluster.UNITS[:5], [unit for unit, _ in staging]),
                          (cluster.RELEASES[:3], [release for _, release in staging]),
                          (('kubectl', 'get', 'namespaces'), []),
                          (cluster.PODS[:4], []), (cluster.WORKLOADS[:4], [])):
        runner.on(*prefix, stdout=json.dumps({'items': found}))
    runner.on(*cluster.SOURCE[:6], stdout=json.dumps({'status': {'artifact': {'revision': REVISION}}}))
    runner.on('kubectl', '-n', 'console', 'get', 'configmap', 'app-dashboards', stdout='')  # no dashboard recorded
    (runner.on(sys.executable, '-m', 'swhurl', 'app-new', handler=lambda args, _: Result(
        args, app_new.returncode, app_new.stdout, app_new.stderr))
        .on(sys.executable, '-m', 'swhurl', handler=lambda args, _: Result(args, 0, '[OK] edited\n')))
    return runner


def app_under_test(runner, **kwargs):
    """The console app, its template questions read from COPIER_YML: no test here reaches GitHub."""
    kwargs.setdefault('repo_opener', ghcr_opener)
    return server.create_app(runner, **kwargs)


class FormTests(unittest.TestCase):
    def test_action_builders_validate_shapes_and_produce_the_clones_commands(self):
        cases = {
            'scale': ({'replicas': '2', 'memory_limit': '256Mi'},
                      (merge.Command('app-scale', ('hello', 'staging', '--replicas=2', '--memory-limit=256Mi')),)),
            'schedule': ({'schedule': '  */10  * * * * '},
                         (merge.Command('app-schedule', ('hello', 'staging', '--schedule=*/10 * * * *')),)),
            'expose': ({'exposure': 'public', 'host': 'weather.example.com'},
                       (merge.Command('app-expose', ('hello', 'staging', '--exposure=public', '--host=weather.example.com')),)),
            'connect': ({'calls': 'orders, billing', 'subscribe': 'orders.created'},
                        (merge.Command('app-calls', ('hello', 'staging', '--add=orders,billing')),
                         merge.Command('app-events', ('hello', 'staging', '--add-subscribe=orders.created')))),
            'disconnect': ({'role': 'calls', 'app': 'orders'}, (merge.Command('app-calls', ('hello', 'staging', '--remove=orders')),)),
            'egress': ({'hosts': 'API.stripe.com, *.example.com:8443'},
                       (merge.Command('app-egress', ('hello', 'staging', '--add=api.stripe.com:443,*.example.com:8443')),)),
            'allow': ({'add': 'orders, team:demo'}, (merge.Command('app-allows', ('hello', 'staging', '--add=orders,team:demo')),)),
            'remove': ({}, (merge.Command('app-remove', ('hello', 'staging')),)),
        }
        for action, (form, commands) in cases.items():
            with self.subTest(action=action):
                self.assertEqual(merge.ACTIONS[action].commands('hello', 'staging', form), commands)
        bad = (('scale', {}, 'change at least one'), ('scale', {'cpu': '--root=/'}, 'not a number or quantity'),
               ('schedule', {'schedule': 'every day'}, 'is not five cron fields'),
               ('expose', {'exposure': 'open'}, 'exposure must'),
               ('expose', {'exposure': 'public', 'host': '--root=/'}, 'not a DNS name'),
               ('connect', {}, 'choose an app to call'), ('connect', {'publish': ''}, 'edit asyncapi.yaml'),
               ('disconnect', {}, 'no such connection to remove'),
               ('disconnect', {'role': 'called-by', 'app': 'orders'}, 'no such connection'),
               ('disconnect', {'role': 'calls', 'app': '--root=/'}, 'no such connection'),
               ('disconnect', {'role': 'subscribes', 'app': 'orders', 'event': 'a.b'}, 'no such connection'),
               ('disconnect', {'role': 'reaches', 'event': '--root=/'}, 'no such connection'),
               ('disconnect', {'role': 'publishes', 'event': 'seen'}, 'edit asyncapi.yaml'),
               ('egress', {'hosts': ' '}, 'name a host'), ('egress', {'hosts': '10.0.0.1'}, 'a grant is a host name'),
               ('egress', {'hosts': '--root=/'}, 'a grant is a host name'), ('allow', {'add': '--root=/'}, 'an allowed caller'))
        for action, form, message in bad:
            with self.subTest(action=action, form=form), self.assertRaisesRegex(actions.ActionError, message):
                merge.ACTIONS[action].commands('hello', 'staging', form)
        self.assertEqual(merge.ACTIONS['expose'].commands('hello', 'staging', {'exposure': 'private'}),
                         (merge.Command('app-expose', ('hello', 'staging', '--exposure=private')),))
        for form, expected in (({'role': 'subscribes', 'app': 'orders', 'event': 'created'},
                                merge.Command('app-events', ('hello', 'staging', '--remove-subscribe=orders.created'))),
                               ({'role': 'reaches', 'event': 'api.stripe.com:443'},
                                merge.Command('app-egress', ('hello', 'staging', '--remove=api.stripe.com:443')))):
            self.assertEqual(merge.ACTIONS['disconnect'].commands('hello', 'staging', form), (expected,))
        for form in ({'calls': 'reports'}, {'subscribe': 'orders.paid'}):
            with self.assertRaisesRegex(actions.ActionError, 'not a web app, or an event'):
                merge.Connections.from_form(form).check_offered({'orders'}, {'orders.created'})

    def test_new_app_open_carries_real_input_records_offers_and_key_but_no_port_or_rendered_files(self):
        public = 'U' + 'A' * 55
        encrypted = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'weather-api-events',
                     'namespace': 'weather-api-staging'}, 'type': 'Opaque',
                     'stringData': {'SWHURL_EVENTS_SEED': 'ENC[x]'}, 'sops': {'age': [{'recipient': 'fixture'}]}}
        api = FakeGitHub()
        chosen = changes.NewApp.from_form({**NEW, 'team': 'demo'}, [], {'demo': ''})
        image = 'ghcr.io/samclement/weather-api:0-abcdef0@sha256:' + 'd' * 64
        commands = merge.ACTIONS['new'].commands(chosen.name, 'staging',
                                                 {'repo': chosen.request.repo, 'image': image, 'flags': chosen.flags})
        job = actions.Job(1, merge.ACTIONS['new'].job, chosen.target, 'sam@swhurl.com', None)
        source = (ROOT / 'tests/fixtures/manifests/web.yaml').read_text() + (
            'provides:\n  api: /openapi.json\n  apiFile: openapi.yaml\n  eventsFile: asyncapi.yaml\n'
            'flags:\n  example: {type: boolean, default: false, description: Example}\n')
        documents = {'swhurl.yaml': source, 'openapi.yaml': 'openapi: 3.0.3\npaths: {}\n',
                     'asyncapi.yaml': 'asyncapi: 3.0.0\nchannels:\n  created: {address: created}\n'
                                      'operations:\n  created: {action: send, channel: {$ref: "#/channels/created"}}\n'}
        def opener(request):
            path = request.full_url.split('/contents/', 1)[1].split('?', 1)[0]
            return documents[path].encode()
        def create(tree):
            proxy = tree / egress.PROXY_FILE
            proxy.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / egress.PROXY_FILE, proxy)
            with contextlib.redirect_stdout(io.StringIO()), mock.patch.object(model_sync.nkeys, 'generate', return_value=('fixture-secret', public)), \
                    mock.patch.object(model_sync, 'Runner', return_value=FakeRunner().on('sops', stdout=yaml.safe_dump(encrypted))):
                self.assertEqual(new.main([*commands[0].argv, '--root', str(tree), '--no-policy-check'], opener), 0)
            # Direct CLI output remains rendered, with its allocation.
            self.assertIn('egressPort', yaml.safe_load((tree / 'apps/weather-api/staging/instance.yaml').read_text())['allocations'])
            self.assertTrue((tree / 'apps/weather-api/staging/helmrelease.yaml').is_file())
        runner = tree_fake(edit=create)
        with mock.patch.dict(MAIN, {'.sops.yaml': b'creation_rules: [{path_regex: "", age: fixture}]'}):
            changes.open_new_app(runner, api.github, job, chosen, image)
        entries = api.sent('POST /git/trees')[0]['tree']
        paths = {entry['path'] for entry in entries}
        expected = {'apps/weather-api/app.yaml', 'apps/weather-api/staging/release.yaml',
                    'apps/weather-api/staging/instance.yaml', 'apps/weather-api/staging/events.sops.yaml',
                    *{f'apps/weather-api/staging/offers/{name}' for name in ('source.yaml', 'openapi.yaml', 'asyncapi.yaml', 'flags.json')}}
        self.assertEqual(paths, expected)
        blobs = {f'blob{i}': base64.b64decode(blob['content']) for i, blob in enumerate(api.sent('POST /git/blobs'))}
        written = {entry['path']: blobs[entry['sha']] for entry in entries}
        settings = yaml.safe_load(written['apps/weather-api/staging/instance.yaml'])
        self.assertNotIn('egressPort', settings['allocations'])
        self.assertEqual(settings['allocations']['eventsKey'], public)
        self.assertEqual(yaml.safe_load(written['apps/weather-api/staging/events.sops.yaml']), encrypted)
        self.assertEqual(yaml.safe_load(written['apps/weather-api/staging/release.yaml'])['publishes'], ['created'])
        self.assertTrue(api.sent('POST /issues/7/labels'))
        self.assertNotIn('swhurl-request', api.sent('POST /pulls')[0]['body'])

    def test_team_channel_and_new_builders_have_no_parallel_change_metadata(self):
        cases = {
            'team-add': ({'description': ' Takes  money '}, merge.Command('team-add', ('payments', '--description=Takes money')),
                         "make team-add NAME=payments DESCRIPTION='Takes money'"),
            'team-edit': ({'description': 'x', 'rename': 'billing'},
                          merge.Command('team-edit', ('payments', '--description=x', '--rename=billing')),
                          'make team-edit NAME=payments ARGS="--description=x --rename=billing"'),
            'team-remove': ({'move_apps_to': 'platform'}, merge.Command('team-remove', ('payments', '--move-apps-to=platform')),
                            'make team-remove NAME=payments ARGS="--move-apps-to platform"'),
            'team-channel-remove': ({}, merge.Command('team-channel', ('payments', '--remove')),
                                    'make team-channel NAME=payments ARGS=--remove'),
        }
        for action, (form, command, make) in cases.items():
            with self.subTest(action=action):
                self.assertEqual(merge.ACTIONS[action].commands('payments', '', form), (command,))
                self.assertEqual(command.as_make(), make)
        for action, name, form, message in (('team-add', 'Payments', {}, 'team name'),
                                            ('team-add', 'payments', {'description': 'x' * 201}, 'at most 200'),
                                            ('team-edit', 'payments', {'rename': 'Bad'}, 'new name'),
                                            ('team-remove', 'payments', {'move_apps_to': 'Bad'}, 'team to move'),
                                            ('team-channel-set', 'Bad', {}, 'team name'),
                                            ('team-channel-set', 'payments', {'address': ''}, 'channel address')):
            with self.subTest(action=action, form=form), self.assertRaisesRegex(actions.ActionError, message):
                merge.ACTIONS[action].commands(name, '', form)
        address = 'https://ntfy.sh/a-private-topic-0123456789'
        (command,) = merge.ACTIONS['team-channel-set'].commands('payments', '', {'address': address})
        self.assertEqual((command.name, command.argv, command.input), ('team-channel', ('payments', '--transport=ntfy'), address + '\n'))
        self.assertNotIn(address, repr(command) + command.as_make() + merge.ACTIONS['team-channel-set'].title)
        commands = merge.ACTIONS['connect'].commands('hello', 'staging', {'calls': 'orders', 'subscribe': 'orders.created'})
        self.assertEqual(' && '.join(c.as_make() for c in commands),
                         'make app-calls APP=hello ENV=staging ARGS="--add=orders" && '
                         'make app-events APP=hello ENV=staging ARGS="--add-subscribe=orders.created"')

    def test_the_clone_s_tooling_leaves_only_the_files_it_means_to(self):
        """A real interpreter on a small clone: without the setting, its bytecode would join the commit."""
        with tempfile.TemporaryDirectory() as tmp:
            clone = Path(tmp)
            (clone / 'tools/swhurl').mkdir(parents=True)
            (clone / 'tools/swhurl/__init__.py').write_text('')
            (clone / 'tools/swhurl/helper.py').write_text('WORD = "edited"\n')
            (clone / 'tools/swhurl/__main__.py').write_text(
                'import sys\nfrom pathlib import Path\nfrom swhurl.helper import WORD\n'
                'Path("out.txt").write_text(WORD + " " + " ".join(sys.argv[1:]))\nprint("[OK]", WORD)\n')
            before = changes.snapshot(clone)
            job = actions.Job(1, 'scale', 'hello/staging', 'sam@swhurl.com', None)
            (command,) = merge.ACTIONS['scale'].commands('hello', 'staging', {'replicas': '2'})
            changes.run_tool(Runner(), job, clone, command.name, list(command.argv), input=command.input)
            self.assertEqual(job.lines, ['[OK] edited'])
            self.assertEqual((clone / 'out.txt').read_text(), 'edited app-scale hello staging --replicas=2')
            self.assertEqual(sorted(changes.snapshot(clone).keys() - before.keys()), ['out.txt'])

    def test_only_console_branches(self):
        api = FakeGitHub()
        job = actions.Job(1, 'scale', 'hello/staging', 'sam@swhurl.com', None)
        with self.assertRaisesRegex(actions.ActionError, 'only its own action branches'):
            changes.open_action(FakeRunner(), api.github, job, 'scale', '../main', 'staging', ())
        self.assertEqual([r.method for r in api.requests], ['GET'], 'refused before anything is downloaded or written')

    def test_token_from_environment(self):
        runner = FakeRunner()
        for value in ('', 'REPLACE_ME', '  '):
            self.assertIsNone(changes.github_from_env(runner, {'GITHUB_TOKEN': value}))
        found = changes.github_from_env(runner, {'GITHUB_TOKEN': TOKEN, 'CONSOLE_REPO': 'me/repo'})
        self.assertEqual(found, changes.GitHub('me/repo', TOKEN))
        self.assertEqual(runner.redact(TOKEN), '<redacted>')


class SharedInputPrTests(unittest.TestCase):
    """Use the real offline edits: their full writes become input-only, held console PRs."""

    def main(self, *, two_environments=True, channel=False):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        for name, text in MAIN.items():
            if not two_environments and ('/prod/' in name or name.endswith('app-hello-prod.yaml')):
                continue
            (root / name).parent.mkdir(parents=True, exist_ok=True)
            (root / name).write_bytes(text)
        (root / 'apps/teams.yaml').write_text('teams:\n  demo: {}\n')
        (root / 'clusters/home/kustomization.yaml').write_text('resources: [platform.yaml]\n')
        notify = root / channels.NOTIFY_DIR
        notify.mkdir(parents=True)
        (notify / 'kustomization.yaml').write_text('resources: [namespace.yaml, routes.yaml]\n')
        (root / channels.UNIT_FILE).write_text('spec:\n  path: ./platform/notify\n')
        settings = root / 'apps/hello/staging/instance.yaml'
        doc = yaml.safe_load(settings.read_text())
        doc['allows'] = ['team:demo']
        doc.setdefault('allocations', {})['eventsKey'] = 'UFIXTURE'
        settings.write_text(yaml.safe_dump(doc))
        folder = settings.parent
        for name in ('events.sops.yaml', 'secret.sops.yaml'):
            (folder / name).write_text('sops: {}\n')
        (folder / 'offers').mkdir()
        (folder / 'offers/source.yaml').write_text('api: openapi.json\n')
        (folder / 'offers/openapi.json').write_text('{}\n')
        if channel:
            (root / 'apps/teams.yaml').write_text('teams:\n  demo: {notify: [phone]}\n')
            (notify / 'channel-phone.sops.yaml').write_text('metadata:\n  name: channel-phone\n  labels:\n'
                                                           '    platform.swhurl.com/notify-transport: ntfy\nsops: {}\n')
        model_sync.sync(root)
        return {path.relative_to(root).as_posix(): path.read_bytes() for path in root.rglob('*') if path.is_file()}

    def propose(self, action, operation, main, *, name='hello', env='staging', form=None):
        runner, api = tree_fake(edit=operation), FakeGitHub()
        job = actions.Job(1, merge.ACTIONS[action].job or action, merge.target(action, name, env), 'sam@swhurl.com', None)
        with mock.patch.dict(MAIN, main, clear=True), contextlib.redirect_stdout(io.StringIO()):
            commands = merge.ACTIONS[action].commands(name, env, form or {})
            changes.open_action(runner, api.github, job, action, name, env, commands)
        self.assertEqual(api.sent('POST /issues/7/labels'), [], 'these changes stay held for review')
        tree = api.sent('POST /git/trees')[0]['tree']
        self.assertTrue(records_only([entry['path'] for entry in tree]), 'Validate must render the actual PR inputs')
        return tree

    def test_failed_semantic_reads_before_or_after_preparation_make_no_github_writes(self):
        for corrupt_before in (True, False):
            with self.subTest(corrupt_before=corrupt_before):
                main = self.main()
                path = 'apps/hello/staging/instance.yaml'
                if corrupt_before:
                    main[path] = b'['
                api = FakeGitHub()
                runner = tree_fake(edit=lambda root, path=path: (root / path).write_text('['))
                job = actions.Job(1, 'scale', 'hello/staging', 'sam@swhurl.com', None)
                commands = merge.ACTIONS['scale'].commands('hello', 'staging', {'replicas': '2'})
                with mock.patch.dict(MAIN, main, clear=True), self.assertRaisesRegex(merge.Hold, 'cannot be read'):
                    changes.open_action(runner, api.github, job, 'scale', 'hello', 'staging', commands)
                self.assertTrue(api.requests)
                self.assertEqual({r.method for r in api.requests}, {'GET'})

    def test_remove_pr_carries_only_input_deletions_for_one_or_last_environment(self):
        for two in (True, False):
            with self.subTest(two_environments=two):
                main = self.main(two_environments=two)
                main['apps/hello/staging/notes.yaml'] = b'keep: true\n'
                tree = self.propose('remove', lambda root: app_edit.remove(root, 'hello', 'staging'), main)
                expected = {'apps/hello/staging/' + name for name in
                            ('release.yaml', 'instance.yaml', 'secret.sops.yaml', 'events.sops.yaml',
                             'offers/source.yaml', 'offers/openapi.json')}
                if not two:
                    expected.add('apps/hello/app.yaml')
                self.assertEqual({entry['path'] for entry in tree}, expected)
                self.assertTrue(all(entry['sha'] is None for entry in tree), 'only deletions, no generated updates')

    def test_remove_filter_never_carries_input_additions_or_modifications(self):
        def incomplete_remove(root):
            folder = root / 'apps/hello/staging'
            (folder / 'instance.yaml').unlink()
            (folder / 'release.yaml').write_text('changed: true\n')
            (folder / 'offers/new.json').write_text('{}\n')
        tree = self.propose('remove', incomplete_remove, self.main())
        self.assertEqual(tree, [{'path': 'apps/hello/staging/instance.yaml', 'mode': '100644', 'type': 'blob', 'sha': None}])

    def test_team_rename_pr_includes_ownership_and_team_grants_but_not_routes(self):
        tree = self.propose('team-edit', lambda root: teams.change(root, 'demo', rename='core'), self.main(channel=True),
                            name='demo', env='', form={'rename': 'core'})
        self.assertEqual({entry['path'] for entry in tree},
                         {'apps/teams.yaml', 'apps/hello/app.yaml', 'apps/hello/staging/instance.yaml'})

    def test_channel_pr_carries_encrypted_and_registry_inputs_not_generated_routes(self):
        def encrypt(args, text):
            doc = yaml.safe_load(text)
            doc['stringData'] = {'address': 'ENC[...]'}
            return Result(args, 0, yaml.safe_dump(doc) + 'sops: {}\n')
        tree = self.propose('team-channel-set', lambda root: teams.team_channel(root, 'demo', 'https://ntfy.sh/private',
                            runner=FakeRunner().on('sops', handler=encrypt)), self.main(),
                            name='demo', env='', form={'address': 'https://ntfy.sh/private'})
        self.assertEqual({entry['path'] for entry in tree},
                         {'apps/teams.yaml', 'platform/notify/channel-demo.sops.yaml',
                          'platform/notify/kustomization.yaml', 'clusters/home/platform-notify.yaml'})


class OpenActionTests(unittest.TestCase):
    def open(self, runner, api=None, *, name='weather-api'):
        api = api or FakeGitHub()
        job = actions.Job(1, 'new app', f'{name}/staging', 'sam@swhurl.com', None)
        url = changes.open_action(runner, api.github, job, 'new', name, 'staging',
                                  (merge.Command('app-new', (name,)),))
        return url, job, api

    def test_removing_an_encrypted_file_needs_review_without_credential_setup(self):
        api, runner = FakeGitHub(), tree_fake()
        job = actions.Job(1, 'remove', 'hello/staging', 'sam@swhurl.com', None)
        path = 'apps/hello/staging/events.sops.yaml'
        with mock.patch.dict(MAIN, {path: b'kind: Secret\n'}):
            changes.open_action(runner, api.github, job, 'remove', 'hello', 'staging',
                                merge.ACTIONS['remove'].commands('hello', 'staging', {}))
        body = api.sent('POST /pulls')[0]['body']
        self.assertIn('needs manual review', body)
        self.assertNotIn('Secret values', body)

    def test_auto_merge_labels_unless_encrypted_inputs_need_review(self):
        for filename, reason in (('', ''), ('events.sops.yaml', "not the app's generated")):
            with self.subTest(filename=filename):
                def edit(tree, filename=filename):
                    folder = tree / 'apps/weather-api/staging'
                    for name in ('app.yaml', 'staging/release.yaml', 'staging/instance.yaml'):
                        path = tree / 'apps/weather-api' / name
                        path.parent.mkdir(parents=True, exist_ok=True)
                        path.write_bytes(MAIN[f'apps/hello/{name}'])
                    if filename:
                        (folder / filename).write_text('sops: {}\n')
                _, job, api = self.open(tree_fake(edit=edit))
                self.assertEqual(bool(api.sent('POST /issues/7/labels')), not bool(filename))
                if reason:
                    self.assertIn(reason, api.sent('POST /pulls')[0]['body'])
                    self.assertTrue(any(line.startswith('Merge plan: Manual merge required') for line in job.lines))
        api = FakeGitHub(fail={'POST /issues/7/labels': (403, 'Resource not accessible')})
        url, job, _ = self.open(tree_fake(), api)
        self.assertEqual(url, 'https://github.com/x/pull/7', 'a failed label leaves the PR for a person')
        self.assertTrue(any(line.startswith('Could not label it auto-merge') for line in job.lines))

    def test_commits_only_owned_changed_inputs_and_publisher_preserves_modes(self):
        def edit(tree):
            (tree / 'apps/hello/staging/helmrelease.yaml').write_text('kind: HelmRelease\nchanged: true\n')
            (tree / 'README.md').unlink()
            path = tree / 'apps/hello/staging/instance.yaml'
            doc = yaml.safe_load(path.read_text())
            doc['replicas'] = 2
            path.write_text(yaml.safe_dump(doc))
            offers = path.parent / 'offers'
            offers.mkdir()
            (offers / 'run.sh').write_text('#!/bin/sh\n')
            (offers / 'run.sh').chmod(0o755)
        api, runner = FakeGitHub(), tree_fake(edit=edit)
        job = actions.Job(1, 'scale', 'hello/staging', 'sam@swhurl.com', None)
        changes.open_action(runner, api.github, job, 'scale', 'hello', 'staging',
                            merge.ACTIONS['scale'].commands('hello', 'staging', {'replicas': '2'}))
        tree = api.sent('POST /git/trees')[0]
        self.assertEqual(tree, {'base_tree': 'tree0', 'tree': [
            {'path': 'apps/hello/staging/instance.yaml', 'mode': '100644', 'type': 'blob', 'sha': 'blob0'}]})
        commit = api.sent('POST /git/commits')[0]
        self.assertEqual((commit['parents'], commit['tree'], commit['author']['name']), ([HEAD], 'tree1', 'swhurl console'))
        self.assertIn('Requested-by: sam@swhurl.com', commit['message'])
        self.assertEqual(api.sent('POST /git/refs'), [{'ref': 'refs/heads/console/scale-hello-staging-abc1234',
                                                    'sha': 'def5678' + '0' * 33}])
        pr = api.sent('POST /pulls')[0]
        self.assertEqual((pr['head'], pr['base'], pr['title']),
                         ('console/scale-hello-staging-abc1234', 'main', '[console] apps: scale hello/staging'))
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            path = root / 'offer.sh'
            path.write_text('#!/bin/sh\n')
            path.chmod(0o755)
            client = changes.GitHubAPI(runner, api.github)
            try:
                changes.publish(client, job, root, {}, changes.snapshot(root), ['offer.sh'], HEAD,
                                'console/new-x-staging-abc1234', 'test', 'body', False, 'review')
            finally:
                client.client.close()
        self.assertEqual(api.sent('POST /git/trees')[-1]['tree'][0]['mode'], '100755')

    def test_token_only_in_the_authorization_header_and_never_sent_off_github(self):
        runner = tree_fake()
        _, _, api = self.open(runner)
        for request in api.requests:
            self.assertEqual(request.url.host, 'api.github.com')
            self.assertEqual(request.headers['authorization'], f'Bearer {TOKEN}')
            self.assertNotIn(TOKEN, str(request.url) + request.content.decode(errors='replace'))
        for call in runner.calls:
            self.assertFalse(any(TOKEN in arg for arg in call), call)

    def test_no_change_opens_nothing_and_the_tree_is_removed(self):
        seen = []
        api = FakeGitHub()
        with self.assertRaisesRegex(actions.ActionError, 'no files changed'):
            changes.open_action(tree_fake(edit=seen.append), api.github,
                                actions.Job(1, 'scale', 'hello/staging', 'sam@swhurl.com', None),
                                'scale', 'hello', 'staging', (merge.Command('app-scale', ('hello', 'staging')),))
        self.assertFalse(seen[0].parent.exists())
        self.assertEqual([r.method for r in api.requests], ['GET', 'GET'])

    def test_refused_or_unvalidated_app_new_writes_nothing_to_github(self):
        failures = [(2, '[ERROR] production instances must pin an image digest'),
                    (1, '[ERROR] could not validate the app policy (helm unavailable)')]
        for code, message in failures:
            with self.subTest(code=code):
                runner = tree_fake(app_new=Result((), code, '', message + '\n'))
                api = FakeGitHub()
                with self.assertRaisesRegex(actions.ActionError, 'refused'):
                    self.open(runner, api)
                self.assertEqual({r.method for r in api.requests}, {'GET'})

    def test_app_new_runs_the_trees_tooling(self):
        runner = tree_fake()
        self.open(runner)
        (call,) = [c for c in runner.calls if 'app-new' in c]
        self.assertEqual(call, (sys.executable, '-m', 'swhurl', 'app-new', 'weather-api'))

    def test_dry_run_lists_the_change_and_writes_nothing(self):
        runner = tree_fake()
        runner.dry_run = True
        url, job, api = self.open(runner)
        self.assertEqual(url, '')
        self.assertEqual({r.method for r in api.requests}, {'GET'})
        self.assertIn('Dry run: would commit 3 file(s) to console/new-weather-api-staging-abc1234; no PR opened', job.lines)

    def test_github_error_is_a_redacted_action_error(self):
        api = FakeGitHub(fail={'POST /git/refs': (422, f'Reference already exists (token {TOKEN})')})
        with self.assertRaises(actions.ActionError) as caught:
            self.open(tree_fake(), api)
        self.assertIn('POST /git/refs returned 422: Reference already exists', str(caught.exception))
        self.assertNotIn(TOKEN, str(caught.exception))
        self.assertEqual(api.sent('POST /pulls'), [])


class NewAppRouteTests(unittest.TestCase):
    def client(self, runner, github=True):
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        self.api = FakeGitHub()
        github = self.api.github if github else None
        return TestClient(app_under_test(runner, jobs=jobs, github=github)), jobs

    def test_the_page_only_starts_a_new_app(self):
        runner = tree_fake()
        c, jobs = self.client(runner)
        for url in ('/new', '/new?preset=swhurl-web'):
            with self.subTest(url=url):
                page = c.get(url, headers=WHO).text
                self.assertIn('action="/new/repo"', page)
                self.assertIn('Create repository and open pull request', page)
                self.assertNotIn('Deploy an existing image', page)
                self.assertNotIn('name="image"', page)
        self.assertEqual(c.post('/new', data={'name': 'weather-api', 'image': 'ghcr.io/me/weather:1.0'},
                                headers=WHO).status_code, 405)
        self.assertIsNone(jobs.get(1))
        self.assertTrue(all(call[0] == 'kubectl' and 'get' in call for call in runner.calls),
                        'it reads the apps it could connect to and changes nothing')
        self.assertEqual({(r.method, r.url.path.rsplit('/', 1)[-1]) for r in self.api.requests}, {('GET', 'teams.yaml'), ('GET', 'routes.yaml')},
                         'the page reads the team registry and each team\'s channel, and nothing else')
        self.assertEqual(len(self.api.requests), 2, 'and keeps both for five minutes')

    def test_the_team_is_chosen_from_the_registry_on_main(self):
        c, _ = self.client(tree_fake())
        page = c.get('/new', headers=WHO).text
        self.assertIn('name="team" value="platform" required >', page)
        self.assertNotIn('checked>\n            <span><strong>platform', page)
        self.assertEqual(page.count('name="team" value="platform" required checked'), 0, 'no team is preselected: there is no default')
        self.assertIn('<span class="muted">The platform itself</span>', page)
        self.assertIn('name="team" value="payments" required >', page)
        self.assertLess(page.index('name="team" value="payments"'), page.index('name="team" value="platform"'), 'alphabetical')
        self.assertEqual((page.count('>notification channel</span>'), page.count('>no notification channel</span>')), (1, 1),
                         'each team is marked with whether it has a channel')
        teams = changes.platform_teams(FakeRunner(), self.api.github)
        self.assertEqual(teams, {'platform': 'The platform itself', 'payments': ''})
        web = [repo.Question('kind', '', ('web', 'worker'), 'web')]
        self.assertEqual(changes.NewApp.from_form({**NEW, 'team': 'payments'}, web, teams).flags,
                         ('--team=payments', '--exposure=authenticated-web'))
        self.assertEqual(changes.NewApp.from_form({**NEW, 'team': 'payments', 'type': 'worker'}, web, teams).flags,
                         ('--team=payments',), 'a worker has a team too')
        for form, registered, message in (({**NEW, 'team': ''}, teams, 'choose the team that owns the app: one of platform, payments'),
                                          ({**NEW, 'team': 'nobody'}, teams, 'choose the team that owns the app'),
                                          (NEW, {}, 'no team is registered')):
            with self.subTest(message=message), self.assertRaisesRegex(actions.ActionError, message):
                changes.NewApp.from_form(form, web, registered)
        unreadable = FakeGitHub(fail={'GET /contents/apps/teams.yaml': (404, 'Not Found')})
        page = TestClient(app_under_test(tree_fake(), github=unreadable.github)).get('/new', headers=WHO).text
        self.assertIn('No team is registered yet', page)
        self.assertNotIn('name="team" value=', page)

    def test_the_form_offers_what_staging_has_to_connect_to_and_says_what_a_team_is(self):
        staging = (staging_app('orders', {'SWHURL_EVENTS_PUBLISH': 'created,paid'}), staging_app('job', port=None),
                   staging_app('billing'))
        c, _ = self.client(tree_fake(staging=staging))
        page = c.get('/new', headers=WHO).text
        self.assertIn('name="calls" value="orders"', page)
        self.assertIn('name="calls" value="billing"', page)
        self.assertNotIn('name="calls" value="job"', page, 'a worker cannot be called')
        self.assertIn('name="subscribe" value="orders.created"', page)
        self.assertIn('name="subscribe" value="orders.paid"', page)
        self.assertIn('It does not grant or restrict anyone\'s access', page)
        empty = self.client(tree_fake())[0].get('/new', headers=WHO).text
        self.assertIn('No web app is in staging yet.', empty)
        self.assertIn('No app in staging publishes an event yet.', empty)

    def test_connections_become_app_new_arguments_and_only_offered_ones_are_accepted(self):
        offered = ({'orders', 'billing'}, {'orders.created'})
        teams = {'platform': ''}
        chosen = changes.NewApp.from_form({**NEW, 'calls': 'orders,billing', 'subscribe': 'orders.created'}, [], teams, *offered)
        self.assertEqual(chosen.flags, ('--team=platform', '--exposure=authenticated-web', '--calls=orders,billing',
                                        '--subscribe=orders.created'))
        with self.assertRaisesRegex(actions.ActionError, 'edit asyncapi.yaml'):
            changes.NewApp.from_form({**NEW, 'publish': 'sent'}, [], teams, *offered)
        self.assertEqual(changes.NewApp.from_record(chosen.record), chosen, 'saved connections keep their flags')
        for obsolete in ('--publish=sent', '--publish='):
            stale = {**chosen.record, 'extra': [*chosen.flags, obsolete]}
            with self.subTest(obsolete=obsolete), self.assertRaises(actions.ActionError) as refused:
                changes.NewApp.from_record(stale)
            self.assertEqual(str(refused.exception), 'publishing choices are no longer supported: edit asyncapi.yaml '
                             'named by provides.eventsFile, then start a new job')
        self.assertEqual(changes.NewApp.from_form(NEW, [], teams, *offered).flags[2:], (), 'nothing chosen adds nothing')
        cases = (({'calls': 'stranger'}, 'not a web app, or an event, that staging has now'),
                 ({'subscribe': 'orders.cancelled'}, 'not a web app, or an event, that staging has now'),
                 ({'calls': '--root=/'}, 'a call names an app'), ({'publish': 'Sent'}, 'edit asyncapi.yaml'),
                 ({'subscribe': 'created'}, 'a subscription <app>.<event>'))
        for form, message in cases:
            with self.subTest(form=form), self.assertRaisesRegex(actions.ActionError, message):
                changes.NewApp.from_form({**NEW, **form}, [], teams, *offered)

    def test_new_app_keeps_resumable_choices_and_the_table_builds_its_commands(self):
        teams, offered = {'platform': ''}, ({'orders'}, set())
        chosen = changes.NewApp.from_form({**NEW, 'description': ' Says  the weather '}, [], teams, *offered)
        self.assertEqual(changes.NewApp.from_record(json.loads(json.dumps(chosen.record))), chosen, 'what a job keeps reads back')
        self.assertEqual(chosen.record['kind'], changes.NEW_APP)
        self.assertEqual(changes.NewApp.from_record({**chosen.record, 'touches_others': True}), chosen,
                         'obsolete review metadata in saved jobs is ignored')
        self.assertNotIn('touches_others', chosen.record)
        commands = merge.ACTIONS['new'].commands(chosen.name, 'staging',
                                                 {'repo': chosen.request.repo, 'image': IMAGE, 'flags': chosen.flags})
        self.assertEqual(commands, (merge.Command('app-new', (
            'weather-api', f'--from-repo={chosen.request.repo}', '--env=staging', f'--image={IMAGE}', '--team=platform',
            '--exposure=authenticated-web')),))
        self.assertEqual((merge.ACTIONS['new'].job, chosen.target, merge.ACTIONS['new'].title.format(name=chosen.name)),
                         ('new app and repository', 'weather-api/staging', 'apps: add weather-api/staging'))
        for record in ({}, {**chosen.record, 'name': '../x'}, {**chosen.record, 'stack': 'cobol'},
                       {**chosen.record, 'extra': ['weather-api; rm']}, {**chosen.record, 'extra': None}):
            with self.subTest(record=record), self.assertRaises((KeyError, TypeError, ValueError)):
                changes.NewApp.from_record(record)

    def test_open_console_prs_are_listed_and_others_ignored(self):
        c, _ = self.client(tree_fake())
        text = c.get('/activity', headers=WHO).text
        self.assertIn('#7 apps: add weather-api staging</a>', text, 'every pull request here is a console one: the prefix is left out')
        self.assertIn('Manual merge required', text)
        self.assertIn('<span class="state suspended">Waiting for you</span>', text)
        self.assertIn('Waiting for you: merge #7 on GitHub', text, 'a pull request no remembered job opened still shows its path')
        self.assertIn('Live in staging', text)
        self.assertNotIn('pull/8', text)
        self.assertEqual(changes.console_prs(FakeRunner(), self.api.github)[0].opened, '2026-10-01')
        c, _ = self.client(tree_fake(), github=False)
        self.assertIn('No GitHub token is configured, so open pull requests are not listed', c.get('/activity', headers=WHO).text)
        api = FakeGitHub(fail={'GET /pulls': (401, 'Bad credentials')})
        page = TestClient(app_under_test(tree_fake(), jobs=actions.Jobs(tree_fake(), inline=True), github=api.github))
        self.assertIn('Could not list open pull requests', page.get('/activity', headers=WHO).text)

    def test_a_new_app_not_yet_applied_waits_for_its_pr(self):
        runner = tree_fake().on('kubectl', '-n', 'flux-system', 'get', 'kustomization', stdout='')
        c, _ = self.client(runner)
        response = c.get('/apps/weather-api/staging', headers=WHO)
        self.assertEqual(response.status_code, 200, 'the open PR on its console/new-weather-api-staging-* branch')
        self.assertIn('Not created yet', response.text)
        self.assertIn('href="https://github.com/x/pull/7"', response.text)
        self.assertEqual(c.get('/apps/weather/staging', headers=WHO).status_code, 404, 'another app\'s PR does not count')
        c, _ = self.client(runner, github=False)
        self.assertEqual(c.get('/apps/weather-api/staging', headers=WHO).status_code, 404, 'neither a PR nor a job')
        self.assertEqual(stages.target_of('console/new-a-staging-staging-abc1234'), ('new', 'a-staging/staging'))

    def test_a_new_app_job_without_a_token_still_shows_on_its_page(self):
        runner = tree_fake().on('kubectl', '-n', 'flux-system', 'get', 'kustomization', stdout='')
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        jobs.submit('new app', 'weather-api/staging', 'sam@swhurl.com', lambda job: None)
        text = TestClient(app_under_test(runner, jobs=jobs)).get('/apps/weather-api/staging', headers=WHO).text
        self.assertIn('Not created yet', text)
        self.assertIn('href="/jobs/1"', text)

    def test_invalid_form_or_missing_token_runs_nothing(self):
        runner = tree_fake()
        app_api = FakeAppGitHub()
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        c = TestClient(app_under_test(runner, jobs=jobs, github=FakeGitHub().github, app_repos=app_api.config))
        response = c.post('/new/repo', data={**NEW, 'name': 'Bad'}, headers=WHO)
        self.assertEqual(response.status_code, 400)
        self.assertIn('value="Bad"', response.text)
        c = TestClient(app_under_test(runner, jobs=jobs, app_repos=app_api.config))
        self.assertIn('No GitHub token is configured', c.get('/new', headers=WHO).text)
        self.assertEqual(c.post('/new/repo', data=NEW, headers=WHO).status_code, 409)
        self.assertTrue(all(call[0] == 'kubectl' and 'get' in call for call in runner.calls), 'only the form\'s reads')
        self.assertEqual(app_api.requests, [])

    def test_cross_site_post_is_refused(self):
        runner = tree_fake()
        app_api = FakeAppGitHub()
        c = TestClient(app_under_test(runner, jobs=actions.Jobs(runner, audit=lambda line: None, inline=True),
                                      github=FakeGitHub().github, app_repos=app_api.config))
        self.assertEqual(c.post('/new/repo', data=NEW, headers={**WHO, 'Origin': 'https://evil.example'}).status_code, 403)
        self.assertEqual((runner.calls, app_api.requests), ([], []))


def staging_reads(runner):
    """A healthy, applied staging image for promotion; all reads remain offline."""
    ready = {'conditions': [{'type': 'Ready', 'status': 'True'}]}
    objects = {
        'kustomization': {'spec': {}, 'status': {**ready, 'lastAppliedRevision': REVISION}},
        'gitrepository': {'status': {'artifact': {'revision': REVISION}}},
        'helmrelease': {'status': ready, 'spec': {'values': {'controllers': {'main': {
            'containers': {'main': {'image': new.parse_image(IMAGE)}}}}}}},
        'pods': {'items': [{'metadata': {'name': 'hello-1'}, 'status': {'containerStatuses': [
            {'ready': True, 'imageID': IMAGE.split(':2.0')[0] + '@' + IMAGE.split('@')[1]}]}}]},
        'deploy,statefulset,daemonset,cronjob': {'items': [{'kind': 'Deployment', 'metadata': {'name': 'hello'},
                                                  'spec': {'replicas': 1}, 'status': {'readyReplicas': 1}}]},
        'ingress': {'items': []}, 'certificate': {'items': []},
    }
    for kind, obj in objects.items():
        ns = 'flux-system' if kind in ('kustomization', 'gitrepository') else 'hello-staging'
        runner.on('kubectl', '-n', ns, 'get', kind, stdout=json.dumps(obj))
    return runner


class ChangeAppRouteTests(unittest.TestCase):
    def client(self, runner, github=True):
        staging_reads(runner)
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        self.api = FakeGitHub()
        github = self.api.github if github else None
        return TestClient(app_under_test(runner, jobs=jobs, github=github)), jobs

    def tool_call(self, runner):
        return next(c for c in runner.calls if c[:3] == (sys.executable, '-m', 'swhurl'))

    def merged(self, jobs, count=2):
        """Finished changes to hello/staging whose pull requests merged, #2 after #1, and the calls made to read them."""
        for number in range(1, count + 1):
            jobs.submit('scale', 'hello/staging', 'sam@swhurl.com',
                        lambda job, number=number: setattr(job, 'link', f'https://github.com/x/pull/{number}'))
        return mock.Mock(side_effect=lambda _runner, _github, number: ('merged', '2026-10-13T10:00:00Z', f'merge{number}'))

    def row(self, state='healthy', applied='render-b'):
        return cluster.AppRow(Instance('hello', 'staging'), State(state), False, 'x:1', digest='sha256:a',
                              render_hash=applied, render_ready=True, render_state=State(state), image_matches=True)

    def test_actual_git_reader_never_persists_reached_for_stale_settings_output(self):
        c, jobs = self.client(tree_fake())
        jobs.submit('scale', 'hello/prod', 'sam@swhurl.com',
                    lambda job: setattr(job, 'link', 'https://github.com/x/pull/1'))
        store = mock.Mock()
        jobs.store = store
        old = changes.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/prod']
        live = cluster.AppRow(Instance('hello', 'prod'), State('healthy'), False, 'x', digest=old.digest,
                              render_hash=old.hash, render_ready=True, render_state=State('healthy'), image_matches=True)
        path = 'apps/hello/prod/instance.yaml'
        doc = yaml.safe_load(MAIN[path])
        doc['replicas'] = 0
        with mock.patch.dict(MAIN, {path: yaml.safe_dump(doc).encode()}), \
                mock.patch.object(changes, 'console_prs', return_value=[]), \
                mock.patch.object(changes, 'pull_state', return_value=('merged', '2026-10-21T10:00:00Z', HEAD)), \
                mock.patch.object(cluster, 'apps', return_value=[live]):
            page = c.get('/jobs/1', headers=WHO).text
            activity = c.get('/activity', headers=WHO).text
            self.assertEqual(jobs.get(1).reached, '', 'stale output must never persist a first-seen completion')
            store.save.assert_not_called()
            self.assertIn('waiting for the Render workflow', page)
            self.assertIn('waiting for the Render workflow', activity)

    def test_the_merge_tree_is_authoritative_and_verdicts_are_always_fresh(self):
        c, jobs = self.client(tree_fake())
        read = self.merged(jobs)
        trees = {'merge1': {'hello/staging': rendered('render-a', 'sha256:a', 'settings-1', 'records-1')},
                 'merge2': {'hello/staging': rendered('render-b', 'sha256:a', 'settings-2', 'records-2')},
                 'main': {'hello/staging': rendered('render-b', 'sha256:a', 'settings-2', 'records-2')}}
        rows = [self.row()]
        with mock.patch.object(changes, 'console_prs', return_value=[]), mock.patch.object(changes, 'pull_state', read), \
                mock.patch.object(changes, 'main_commit', return_value='main'), \
                mock.patch.object(changes, 'render_targets', side_effect=lambda _r, _g, sha: trees[sha]) as targets, \
                mock.patch.object(cluster, 'apps', side_effect=lambda _r, **kw: rows):
            self.assertIn('Superseded', c.get('/jobs/1', headers=WHO).text)
            self.assertIn('hello/staging runs it.', c.get('/jobs/2', headers=WHO).text)
            rows[:] = [self.row('failing')]
            activity = c.get('/activity', headers=WHO).text
            self.assertIn('hello/staging is failing', activity, 'a verdict is never saved')
            self.assertEqual(targets.call_count, 3, 'immutable render facts may be cached')
            self.assertEqual(sorted(call.args[2] for call in read.call_args_list), [1, 2], 'a merged pull request is read once')
            self.assertFalse(hasattr(jobs.get(2), 'ending'))

    def test_a_change_seen_live_is_remembered_and_a_new_image_carries_it(self):
        c, jobs = self.client(tree_fake())
        trees = {'merge1': {'hello/staging': rendered('render-a', 'sha256:a', 'settings-1', 'records-1')},
                 'main': {'hello/staging': rendered('render-a', 'sha256:a', 'settings-1', 'records-1')}}
        rows = [self.row(applied='render-a')]
        with mock.patch.object(changes, 'console_prs', return_value=[]), \
                mock.patch.object(changes, 'pull_state', self.merged(jobs, 1)), \
                mock.patch.object(changes, 'main_commit', side_effect=lambda _r, _g: head[0]), \
                mock.patch.object(changes, 'render_targets', side_effect=lambda _r, _g, sha: trees[sha]), \
                mock.patch.object(cluster, 'apps', side_effect=lambda _r, **kw: rows):
            head = ['main']
            self.assertIn('hello/staging runs it.', c.get('/jobs/1', headers=WHO).text)
            self.assertTrue(jobs.get(1).reached, 'the first read at its end is recorded')
            trees['deploy'] = {'hello/staging': rendered('render-b', 'sha256:b', 'settings-1', 'records-2')}
            head[0], rows[:] = 'deploy', [replace(self.row(applied='render-b'), digest='sha256:b')]
            self.assertIn('hello/staging runs it, with a newer image.', c.get('/jobs/1', headers=WHO).text)
            trees['rescale'] = {'hello/staging': rendered('render-c', 'sha256:b', 'settings-2', 'records-3')}
            head[0] = 'rescale'
            page = c.get('/jobs/1', headers=WHO).text
            self.assertIn('a newer configuration has since replaced it', page)
            self.assertNotIn('Superseded', page)

    def test_current_main_and_cluster_failures_never_complete_a_change(self):
        c, jobs = self.client(tree_fake())
        trees = {'hello/staging': rendered('render-b', 'sha256:a', 'settings-1', 'records-1')}
        rows = [self.row(applied='old')]
        with mock.patch.object(changes, 'console_prs', return_value=[]), \
                mock.patch.object(changes, 'pull_state', self.merged(jobs, 1)), \
                mock.patch.object(changes, 'main_commit', return_value='main') as main, \
                mock.patch.object(changes, 'render_targets', return_value=trees), \
                mock.patch.object(cluster, 'apps', side_effect=lambda _r, **kw: rows) as apps:
            self.assertIn('Flux is applying it', c.get('/jobs/1', headers=WHO).text)
            apps.side_effect = cluster.ReadError('offline')
            self.assertIn('cluster could not be read', c.get('/jobs/1', headers=WHO).text)
            main.side_effect = actions.ActionError('offline')
            self.assertIn('current render could not be read', c.get('/jobs/1', headers=WHO).text)

    def test_a_closed_pull_request_is_read_again_and_github_failure_is_unknown(self):
        c, jobs = self.client(tree_fake())
        read = mock.Mock(return_value=('closed', '', ''))
        self.merged(jobs, 1)
        with mock.patch.object(changes, 'pull_state', read), mock.patch.object(changes, 'console_prs', return_value=[]):
            self.assertIn('Closed without merging', c.get('/jobs/1', headers=WHO).text)
            read.return_value = ('open', '', '')
            self.assertIn('Waiting for you', c.get('/jobs/1', headers=WHO).text)
            read.side_effect = actions.ActionError('offline')
            self.assertIn('could not be read from GitHub', c.get('/jobs/1', headers=WHO).text)

    def test_scale_promote_and_remove_run_the_clones_commands(self):
        cases = (
            ('/apps/hello/prod/scale', {'replicas': '2', 'memory_limit': '256Mi', 'cpu': ''},
             ('app-scale', 'hello', 'prod', '--replicas=2', '--memory-limit=256Mi'), 'hello/prod',
             'console/scale-hello-prod-abc1234', '[console] apps: scale hello/prod'),
            ('/apps/hello/staging/promote', PROMOTION, ('app-promote', 'hello', f'--expect-image={IMAGE}', '--prepare-only'), 'hello/prod',
             'console/promote-hello-staging-abc1234', '[console] apps: promote hello/staging to hello/prod'),
            ('/apps/hello/staging/remove', {}, ('app-remove', 'hello', 'staging'), 'hello/staging',
             'console/remove-hello-staging-abc1234', '[console] apps: remove hello/staging'),
            ('/apps/hello/staging/connect', {'subscribe': 'orders.created'},
             ('app-events', 'hello', 'staging', '--add-subscribe=orders.created'), 'hello/staging',
             'console/connect-hello-staging-abc1234', '[console] apps: change the connections of hello/staging'),
            ('/apps/hello/staging/disconnect', {'role': 'subscribes', 'app': 'orders', 'event': 'created'},
             ('app-events', 'hello', 'staging', '--remove-subscribe=orders.created'), 'hello/staging',
             'console/disconnect-hello-staging-abc1234', '[console] apps: change the connections of hello/staging'),
            ('/catalogue/staging/use', {'app': 'hello', 'subscribe': 'orders.created'},  # the same subscription grant
             ('app-events', 'hello', 'staging', '--add-subscribe=orders.created'), 'hello/staging',
             'console/connect-hello-staging-abc1234', '[console] apps: change the connections of hello/staging'),
        )
        for path, form, command, target, branch, title in cases:
            with self.subTest(path=path), mock.patch.dict(MAIN, connected_main()):
                runner = tree_fake(staging=(staging_app('orders', {'SWHURL_EVENTS_PUBLISH': 'created'}),))
                c, jobs = self.client(runner)
                response = c.post(path, data=form, headers=WHO, follow_redirects=False)
                self.assertEqual(response.status_code, 303, response.text)
                self.assertEqual(self.tool_call(runner)[3:], command)
                job = jobs.get(1)
                self.assertEqual((job.state, job.unit, job.link), ('succeeded', target, 'https://github.com/x/pull/7'))
                (payload,) = self.api.sent('POST /pulls')
                self.assertEqual((payload['head'], payload['title']), (branch, title))

    def test_instance_changes_merge_themselves_unless_they_touch_production_or_grant_access(self):
        cases = (('/apps/hello/staging/scale', {'replicas': '2'}, True),
                 ('/apps/hello/staging/schedule', {'schedule': '*/5 * * * *'}, True),
                 ('/apps/hello/staging/connect', {'calls': 'orders'}, True),
                 ('/apps/hello/staging/allow', {'remove': 'nobody', 'add': 'team:demo'}, False),
                 ('/apps/hello/staging/egress', {'hosts': 'api.stripe.com'}, False),
                 ('/apps/hello/staging/disconnect', {'role': 'reaches', 'event': 'api.stripe.com:443'}, True),
                 ('/apps/hello/prod/scale', {'replicas': '2'}, False),
                 ('/apps/hello/prod/scale', {'replicas': '0', 'auto_merge': 'on'}, False),
                 ('/apps/hello/staging/promote', PROMOTION, True),
                 ('/apps/hello/staging/promote', {**PROMOTION, 'hold': 'on'}, False),
                 ('/apps/hello/staging/expose', {'exposure': 'public', 'host': 'hello.example.com'}, False),
                 ('/apps/hello/staging/disconnect', {'role': 'calls', 'app': 'orders'}, True),
                 ('/apps/hello/staging/remove', {'auto_merge': 'on'}, False))
        for path, form, merges in cases:
            with self.subTest(path=path, form=form), mock.patch.dict(MAIN, connected_main()):
                c, jobs = self.client(tree_fake(staging=(staging_app('orders'),)))
                c.post(path, data=form, headers=WHO)
                self.assertEqual(jobs.get(1).state, 'succeeded')
                self.assertEqual(self.api.sent('POST /issues/7/labels'), [{'labels': ['auto-merge']}] if merges else [])
                self.assertEqual('Merges itself when Validate and the trusted merge checks pass' in self.api.sent('POST /pulls')[0]['body'], merges)

    def test_a_rollback_promotion_is_merged_by_a_person(self):
        """Staging runs an older build than production: the PR opens without the auto-merge label."""
        c, jobs = self.client(tree_fake())
        with mock.patch.object(promotion, 'older', return_value=True):
            c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        job = jobs.get(1)
        self.assertEqual(job.state, 'succeeded', job.lines)
        self.assertEqual(self.api.sent('POST /issues/7/labels'), [])
        self.assertTrue(any(line.startswith('Merge plan: Manual merge required') and 'rollback' in line for line in job.lines))
        body = self.api.sent('POST /pulls')[0]['body']
        self.assertNotIn('Merges itself', body)
        self.assertIn('a rollback', body)
        self.assertEqual(console_promotion.promoted_image(body), IMAGE)

    def test_a_first_promotion_that_changes_the_apps_it_calls_is_merged_by_a_person(self):
        """The merge gate holds a promotion to its own app: one that edits its callees must not be labelled for it."""
        def record(rel, **fields):  # an instance.yaml with these settings added
            return yaml.safe_dump({**yaml.safe_load(main[rel]), **fields}, sort_keys=False).encode()
        main = {rel: data for rel, data in MAIN.items() if 'hello/prod' not in rel and 'app-hello-prod' not in rel}
        main['apps/hello/staging/instance.yaml'] = record('apps/hello/staging/instance.yaml', calls=['orders'])
        for rel, data in MAIN.items():  # orders runs in production: hello's production files, renamed
            if 'hello/prod' in rel or 'app-hello-prod' in rel or rel == 'apps/hello/app.yaml':
                main[rel.replace('hello', 'orders')] = data.replace(b'hello', b'orders')
        main['apps/orders/prod/instance.yaml'] = record('apps/orders/prod/instance.yaml', allows=['hello'])
        with tempfile.TemporaryDirectory() as tmp, mock.patch.dict(MAIN, main, clear=True):
            root = Path(tmp)
            for rel, data in main.items():
                (root / rel).parent.mkdir(parents=True, exist_ok=True)
                (root / rel).write_bytes(data)
            good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
            review = console_promotion.review(root, 'hello', good)
            self.assertEqual((review['admits'], review['setup']), (['orders'], True))
            shutil.rmtree(root / 'apps/orders/prod')
            self.assertEqual(console_promotion.admitting(root, 'hello'), [], 'a callee with no production is not edited')
            c, jobs = self.client(tree_fake())
            page = c.get('/apps/hello/staging/promotion', headers=WHO).text
            self.assertIn('This also lets hello call orders in production', page)
            self.assertNotIn('name="hold"', page, 'there is no automatic merge to hold')
            c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        job = jobs.get(1)
        self.assertEqual(job.state, 'succeeded', job.lines)
        self.assertTrue(any(line.startswith('Merge plan: Manual merge required') and 'orders' in line for line in job.lines))
        self.assertEqual(self.api.sent('POST /issues/7/labels'), [])
        body = self.api.sent('POST /pulls')[0]['body']
        self.assertIn('This also lets `hello` call `orders/prod`', body)
        self.assertNotIn('Merges itself', body)
        self.assertIn('creates production', body)
        committed = [entry['path'] for entry in self.api.sent('POST /git/trees')[0]['tree']]
        self.assertTrue(committed and all(promotion.records('hello', path) for path in committed),
                        f'a first promotion carries records only: {committed}')

    def test_a_change_the_tooling_refuses_shows_its_own_words_and_opens_nothing(self):
        """The console runs the same commands as make, so their refusals read the same in the job."""
        refusals = (
            ('/apps/hello/staging/disconnect', {'role': 'calls', 'app': 'nowhere'}, '[ERROR] hello/staging does not call nowhere'),
            ('/apps/hello/staging/remove', {}, '[ERROR] hello/staging is called by reports: remove those calls first'),
            ('/apps/hello/prod/scale', {'replicas': '9'}, '[BAD] quota: replicas and sizes exceed the namespace quota for pods'),
        )
        for path, form, message in refusals:
            with self.subTest(path=path):
                runner = TreeRunner()
                runner.on(sys.executable, '-m', 'swhurl', handler=lambda args, _, message=message: Result(args, 2, '', message + '\n'))
                c, jobs = self.client(runner)
                self.assertEqual(c.post(path, data=form, headers=WHO, follow_redirects=False).status_code, 303)
                job = jobs.get(1)
                self.assertEqual(job.state, 'failed')
                self.assertIn(message, job.lines)
                self.assertTrue(any('refused this change (see above); nothing was written to GitHub' in line for line in job.lines))
                self.assertEqual(self.api.sent('POST /pulls'), [])
                self.assertIn(message.split('] ')[1][:30], c.get('/jobs/1', headers=WHO).text.replace('&#39;', "'"))

    def test_refusals_run_nothing(self):
        runner = tree_fake()
        c, _ = self.client(runner)
        for path, form, code in (('/apps/hello/prod/promote', {}, 400), ('/apps/hello/prod/scale', {}, 400),
                                 ('/apps/hello/prod/scale', {'replicas': '2; rm'}, 400), ('/apps/hello/prod/delete', {}, 404),
                                 ('/apps/hello/prod/connect', {}, 400), ('/apps/hello/prod/connect', {'calls': '--root=/'}, 400),
                                 ('/apps/hello/prod/connect', {'publish': 'created'}, 400),
                                 ('/catalogue/prod/use', {'app': 'hello', 'publish': 'created'}, 400),
                                 ('/apps/hello/prod/disconnect', {'role': 'publishes', 'event': 'created'}, 400),
                                 ('/apps/hello/prod/connect', {'subscribe': 'created'}, 400),
                                 ('/apps/hello/prod/disconnect', {'role': 'called-by', 'app': 'orders'}, 400),
                                 ('/catalogue/prod/use', {'calls': 'orders'}, 400), ('/catalogue/prod/use', {'app': '../hello', 'calls': 'orders'}, 404),
                                 ('/catalogue/dev/use', {'app': 'hello', 'calls': 'orders'}, 404),
                                 ('/apps/hello/prod/calls', {}, 404), ('/apps/hello/prod/events', {}, 404),
                                 ('/apps/Hello/prod/remove', {}, 404), ('/apps/hello/dev/remove', {}, 404)):
            with self.subTest(path=path, form=form):
                self.assertEqual(c.post(path, data=form, headers=WHO).status_code, code)
        c, _ = self.client(runner, github=False)
        self.assertEqual(c.post('/apps/hello/prod/remove', headers=WHO).status_code, 409)
        self.assertEqual(runner.calls, [])

    def test_promotion_refuses_missing_or_stale_review_and_unhealthy_staging(self):
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
        cases = (({}, good, 'review the image'),
                 ({**PROMOTION, 'image': IMAGE.replace('2.0', '1.0')}, good, 'changed'),
                 (PROMOTION, replace(good, release=('False', 'upgrade failed')), 'healthy'),
                 (PROMOTION, replace(good, running_images=['x@sha256:old']), 'healthy'),
                 (PROMOTION, replace(good, unit_suspended=True), 'healthy'),
                 (PROMOTION, replace(good, release_suspended=True), 'healthy'),
                 (PROMOTION, replace(good, release=None), 'healthy'))
        for form, status, message in cases:
            with self.subTest(form=form, status=status):
                runner = tree_fake()
                c, jobs = self.client(runner)
                with mock.patch.object(ops, 'gather_status', return_value=status):
                    response = c.post('/apps/hello/staging/promote', data=form, headers=WHO)
                if not form:
                    self.assertEqual(response.status_code, 400)
                    self.assertIn(message, response.text)
                    self.assertEqual(jobs.recent(), [])
                else:
                    self.assertEqual(jobs.get(1).state, 'failed')
                    self.assertIn(message, jobs.get(1).lines[-1])
                self.assertTrue(all(r.method == 'GET' for r in self.api.requests))

    def test_promotion_readiness_is_once_after_preparation_before_any_write(self):
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
        prepared = []
        def prepare(root):
            self.assertEqual(read.call_count, 0)
            promoted(root)
            prepared.append(True)
        c, jobs = self.client(tree_fake(edit=prepare))
        def status(*_):
            self.assertEqual(prepared, [True])
            self.assertTrue(all(r.method == 'GET' for r in self.api.requests))
            return good
        with mock.patch.object(ops, 'gather_status', side_effect=status) as read:
            c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        self.assertEqual(read.call_count, 1)
        self.assertEqual(jobs.get(1).state, 'succeeded', jobs.get(1).lines)
        self.assertEqual(len(self.api.sent('POST /pulls')), 1)

    def test_promotion_read_failure_fails_the_job_and_writes_nothing(self):
        c, jobs = self.client(tree_fake())
        with mock.patch.object(ops, 'gather_status', side_effect=CommandError(['kubectl'], 'Forbidden')) as read:
            c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        self.assertEqual(read.call_count, 1)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('Forbidden', jobs.get(1).lines[-1])
        self.assertTrue(all(r.method == 'GET' for r in self.api.requests))

    def test_malformed_or_partial_production_stops_before_readiness_and_github_writes(self):
        for path in ('apps/hello/prod/release.yaml', 'apps/hello/prod/instance.yaml'):
            for invalid in (None, b'['):
                with self.subTest(path=path, invalid=invalid):
                    main = dict(MAIN)
                    if invalid is None:
                        del main[path]
                    else:
                        main[path] = invalid
                    c, jobs = self.client(tree_fake())
                    with mock.patch.dict(MAIN, main, clear=True), mock.patch.object(ops, 'gather_status') as read:
                        c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
                    self.assertEqual(jobs.get(1).state, 'failed')
                    read.assert_not_called()
                    self.assertTrue(all(r.method == 'GET' for r in self.api.requests))

    def test_failed_preparation_never_checks_readiness_or_writes_to_github(self):
        def broken(_root):
            raise actions.ActionError('failed preparation')
        c, jobs = self.client(tree_fake(edit=broken))
        with mock.patch.object(ops, 'gather_status') as read:
            c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        self.assertEqual(read.call_count, 0)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('failed preparation', jobs.get(1).lines[-1])
        self.assertTrue(all(r.method == 'GET' for r in self.api.requests))

    def test_a_commit_elsewhere_does_not_hold_a_promotion_and_one_to_staging_does(self):
        """Staging's unit is one commit behind main: GitHub says which files that commit changed."""
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
        newer = 'fed9876' + '0' * 33
        behind = replace(good, desired_revision='main@sha1:' + newer, unit=('Unknown', 'Reconciliation in progress'),
                         unit_reason='Progressing')
        compare = f'GET /compare/{HEAD}...{newer}'
        elsewhere = {'status': 'ahead', 'files': [{'filename': 'docs/apps.md'}, {'filename': 'apps/orders/staging/helmrelease.yaml'}]}
        cases = ((elsewhere, ''),
                 ({'status': 'ahead', 'files': [{'filename': 'apps/hello/staging/helmrelease.yaml'}]}, 'a change to staging is in Git'),
                 ({'status': 'ahead', 'files': [{'filename': 'apps/hello/old.yaml', 'previous_filename': 'apps/hello/staging/x.yaml'}]},
                  'a change to staging is in Git'),
                 ({'status': 'diverged', 'files': []}, 'Flux is still applying'),
                 ({'status': 'ahead', 'files': [{'filename': f'docs/{n}.md'} for n in range(300)]}, 'Flux is still applying'),
                 (None, 'Flux is still applying'))
        for answer, problem in cases:
            with self.subTest(answer=str(answer)[:60]):
                runner = tree_fake().on('kubectl', 'get', 'namespace', stdout='{}')  # the app page reads its team
                c, jobs = self.client(runner)
                if answer is not None:
                    self.api.also[compare] = answer
                with mock.patch.object(ops, 'gather_status', return_value=behind):
                    review = c.get('/apps/hello/staging/promotion', headers=WHO)
                    page = c.get('/apps/hello/staging', headers=WHO)
                    c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
                if problem:
                    self.assertEqual(review.status_code, 409)
                    self.assertEqual(jobs.get(1).state, 'failed')
                    self.assertIn(problem, review.text)
                    self.assertIn(problem, page.text)
                    self.assertIn(problem, jobs.get(1).lines[-1])
                    self.assertTrue(all(r.method == 'GET' for r in self.api.requests))
                else:
                    self.assertEqual(review.status_code, 200, review.text)
                    self.assertIn('See exactly what would go to production', page.text)
                    self.assertEqual(jobs.get(1).state, 'succeeded', jobs.get(1).lines)
                    self.assertIn('--prepare-only', self.tool_call(runner), 'the console owns the single late readiness check')
                    self.assertEqual(len(self.api.sent('POST /pulls')), 1)
        c, _ = self.client(tree_fake())
        with mock.patch.object(ops, 'gather_status', return_value=good):
            c.get('/apps/hello/staging/promotion', headers=WHO)
        self.assertFalse([r for r in self.api.requests if '/compare/' in r.url.path], 'an applied staging asks GitHub nothing more')

    def test_review_page_submits_the_displayed_image_and_shows_what_main_renders(self):
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
        c, _ = self.client(tree_fake())
        with mock.patch.object(ops, 'gather_status', return_value=good):
            response = c.get('/apps/hello/staging/promotion', headers=WHO)
        self.assertEqual(response.status_code, 200, response.text)
        for name, value in PROMOTION.items():
            self.assertIn(f'name="{name}" value="{value}"', response.text)
        self.assertNotIn('name="configuration"', response.text)
        self.assertIn('What main renders differently', response.text)
        self.assertIn('b/apps/hello/prod/helmrelease.yaml', response.text, 'the rendered diff, against main')
        self.assertNotIn('b/apps/hello/prod/release.yaml', response.text, 'records are in the PR, not the diff')
        self.assertIn('Hold for manual review', response.text)
        with mock.patch.object(ops, 'gather_status', return_value=replace(good, release=None)):
            response = c.get('/apps/hello/staging/promotion', headers=WHO)
        self.assertEqual(response.status_code, 409)
        self.assertNotIn('action="/apps/hello/staging/promote"', response.text)

    def test_duplicate_click_and_restart_recover_the_existing_pr(self):
        pr = changes.PullRequest(9, 'promote hello', 'https://github.com/x/pull/9',
                                 'console/promote-hello-staging-abcdef0', '2026-10-03',
                                 f'Promote the reviewed staging image `{IMAGE}` to `hello/prod`.')
        runner = tree_fake()
        c, jobs = self.client(runner)
        with mock.patch.object(changes, 'console_prs', return_value=[pr]):
            for _ in range(2):
                self.assertEqual(c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO).status_code, 200)
            self.assertEqual(jobs.get(2).link, pr.url)
            fresh, fresh_jobs = self.client(tree_fake())
            response = fresh.get('/apps/hello/staging/promotion', headers=WHO)
            self.assertIn(pr.url, response.text)
            self.assertIn('Manual merge required', response.text)
            self.assertNotIn('/promotion-prs/', response.text, 'no hold or resume control: the label is the request')
            self.assertEqual(fresh_jobs.recent(), [])
        self.assertFalse(any(c[:3] == (sys.executable, '-m', 'swhurl') for c in runner.calls))
        self.assertEqual(self.api.sent('POST /pulls'), [])

    def test_an_open_pr_says_who_merges_it(self):
        def pr(branch='console/promote-hello-staging-abcdef0', labels=(), body='', refused=''):
            return changes.PullRequest(9, 'promote hello', 'https://github.com/x/pull/9', branch, '2026-10-03', body, labels,
                                       refused=refused)
        mode = server.promotion.merge_mode
        self.assertEqual(mode(pr(labels=('auto-merge',))), 'Auto-merge after checks')
        self.assertEqual(mode(pr('console/new-hello-staging-abcdef0', ('auto-merge',))), 'Auto-merge after checks')
        self.assertEqual(mode(pr(refused='it promotes an older image than production runs (a rollback): a person reviews it')),
                         'Manual merge required: auto-merge was refused: it promotes an older image than production runs '
                         '(a rollback): a person reviews it')
        plan = '**Merge plan:** Manual merge required: this pull request creates production. Review the changes, then merge it yourself.'
        self.assertEqual(mode(pr(body=plan)), 'Manual merge required: this pull request creates production. Review the changes, '
                                              'then merge it yourself.')
        self.assertEqual(mode(pr(body=plan, refused='it conflicts with main')),
                         'Manual merge required: auto-merge was refused: it conflicts with main', "the gate's reason comes first")
        self.assertEqual(mode(pr('console/scale-hello-prod-abcdef0')), 'Manual merge required')

    def test_existing_pr_requires_explicit_replacement_for_another_image(self):
        pr = changes.PullRequest(9, 'promote hello', 'https://github.com/x/pull/9',
                                 'console/promote-hello-staging-abcdef0', '2026-10-03',
                                 'Promote the reviewed staging image `older` to `hello/prod`.')
        c, jobs = self.client(tree_fake())
        with mock.patch.object(changes, 'console_prs', return_value=[pr]):
            c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('Close it explicitly', jobs.get(1).lines[-1])

    def test_staging_movement_after_generation_still_opens_nothing(self):
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
        runner = tree_fake()
        c, jobs = self.client(runner)
        with mock.patch.object(ops, 'gather_status', return_value=replace(good, desired_image='new')) as read:
            c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('changed', jobs.get(1).lines[-1])
        self.assertEqual(read.call_count, 1)
        self.assertTrue(any(c[:4] == (sys.executable, '-m', 'swhurl', 'app-promote') for c in runner.calls))
        self.assertTrue(all(r.method == 'GET' for r in self.api.requests))


if __name__ == '__main__':
    unittest.main()


APP_COMMIT = '9f8e7d6' + '1' * 33
DIGEST = 'sha256:' + 'd' * 64


class FakeAppGitHub:
    """GitHub's API as the second token sees it: notes does not exist until created; its first run succeeds."""

    def __init__(self, existing=('hello-ts',), runs=None, webhook='fixture-image-webhook-token', hooks_status=201):
        self.hooks_status = hooks_status
        self.requests: list[httpx.Request] = []
        self.existing = set(existing)
        self.runs = list(runs if runs is not None else [None, {'status': 'in_progress'}, {'status': 'completed',
                                                                                        'conclusion': 'success'}])
        from swhurl.console import repos
        self.config = repos.AppReposToken('github_pat_app_repos_fixture_0123', image_webhook_token=webhook,
                                          transport=httpx.MockTransport(self.handle))

    def handle(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        path, method = request.url.path, request.method
        if method == 'GET' and path.count('/') == 3 and path.startswith('/repos/samclement/'):
            return httpx.Response(200 if path.rsplit('/', 1)[1] in self.existing else 404, json={'message': 'x'})
        if (method, path) == ('POST', '/user/repos'):
            self.existing.add(json.loads(request.content)['name'])
            return httpx.Response(201, json={'html_url': 'https://github.com/samclement/notes'})
        answers = {('GET', '/repos/samclement/notes/git/ref/heads/main'): {'object': {'sha': 'init000'}},
                   ('POST', '/repos/samclement/notes/git/blobs'): {'sha': 'blob'},
                   ('POST', '/repos/samclement/notes/git/trees'): {'sha': 'tree'},
                   ('POST', '/repos/samclement/notes/git/commits'): {'sha': APP_COMMIT},
                   ('PATCH', '/repos/samclement/notes/git/refs/heads/main'): {'ref': 'refs/heads/main'}}
        if (method, path) == ('GET', '/repos/samclement/notes/actions/runs'):
            run = self.runs.pop(0) if len(self.runs) > 1 else self.runs[0]
            runs = [] if run is None else [{'name': 'Container', 'run_number': 1, 'html_url': 'https://run/1',
                                            'conclusion': None, **run}]
            return httpx.Response(200, json={'workflow_runs': runs})
        if (method, path) == ('POST', '/repos/samclement/notes/hooks'):
            return httpx.Response(self.hooks_status, json={'message': 'Resource not accessible by personal access token'})
        if (method, path) in answers:
            return httpx.Response(200, json=answers[(method, path)])
        return httpx.Response(404, json={'message': 'Not Found'})

    def writes(self):
        return [(r.method, r.url.path) for r in self.requests if r.method != 'GET']


COPIER_YML = """_subdirectory: template
app_name:
  type: str
description:
  type: str
kind:
  type: str
  help: web or worker
  choices: {web: web, worker: worker}
  default: web
database:
  type: str
  help: none or sqlite
  choices: [none, sqlite]
  default: none
api:
  type: str
  choices: [none, openapi]
  default: none
events:
  type: str
  choices: [none, asyncapi]
  default: none
flags:
  type: str
  choices: [none, example]
  default: none
frontend:
  type: str
  choices: [none, vite]
  default: none
"""


def ghcr_opener(request, seed=False):
    """GHCR serving every tag except a seed (0-<sha>), unless ``seed``."""
    if '/contents/copier.yml?ref=' in request.full_url:
        return {}, COPIER_YML.encode()
    if '/manifests/0-' in request.full_url and not seed:
        raise urllib.error.HTTPError(request.full_url, 404, 'not found', {}, None)
    if 'token' in request.full_url:
        return {}, json.dumps({'token': 'anon'}).encode()
    return {'Docker-Content-Digest': DIGEST}, b''


def render_into_dest(args, _input):
    dest = Path(args[-1])
    (dest / '.github/workflows').mkdir(parents=True)
    (dest / 'swhurl.yaml').write_text('version: 1\n')
    (dest / '.github/workflows/container.yml').write_text('name: Container\n')
    return Result(args, 0)


def repo_runner(runner=None):
    runner = runner or FakeRunner()
    return runner.on('copier', handler=render_into_dest).on('uvx', handler=render_into_dest)


class AppReposTests(unittest.TestCase):
    def job(self):
        return actions.Job(1, 'new app and repository', 'notes/staging', 'sam@swhurl.com', None)

    def test_creates_pushes_waits_and_returns_the_first_image(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        api = FakeAppGitHub()
        client, job = repos.AppRepos(repo_runner(), api.config), self.job()
        image = repos.create_app_repo(client.runner, client, job, repo.Request('notes'), sleep=lambda _: None,
                                      opener=ghcr_opener)
        self.assertEqual(image, f'ghcr.io/samclement/notes:1-9f8e7d6@{DIGEST}')
        self.assertEqual(api.writes(), [('POST', '/user/repos')] + [('POST', '/repos/samclement/notes/git/blobs')] * 2
                         + [('POST', '/repos/samclement/notes/git/trees'), ('POST', '/repos/samclement/notes/git/commits'),
                            ('PATCH', '/repos/samclement/notes/git/refs/heads/main'),
                            ('POST', '/repos/samclement/notes/hooks')])
        hook = json.loads(next(r.content for r in api.requests if r.url.path.endswith('/hooks')))
        self.assertEqual((hook['events'], hook['config']['secret']), (['package', 'registry_package'],
                                                                      'fixture-image-webhook-token'))
        self.assertIn('/hook/', hook['config']['url'])
        self.assertIn('Added the image webhook: Flux scans as soon as an image is published', job.lines)
        self.assertNotIn('fixture-image-webhook-token', '\n'.join(job.lines))
        created = json.loads(api.requests[1].content)
        self.assertEqual((created['private'], created['auto_init']), (False, True))
        tree = json.loads(next(r.content for r in api.requests if r.url.path.endswith('/git/trees')))
        self.assertEqual(sorted(e['path'] for e in tree['tree']), ['.github/workflows/container.yml', 'swhurl.yaml'])
        self.assertNotIn('base_tree', tree, 'the rendered files replace the initial README')
        commit = json.loads(next(r.content for r in api.requests if r.url.path.endswith('/git/commits')))
        self.assertEqual(commit['parents'], ['init000'])
        self.assertIn('Requested-by: sam@swhurl.com', commit['message'])
        self.assertIn('A swhurl.yaml', job.lines)
        self.assertIn('run 1 in_progress', job.lines)
        self.assertNotIn('github_pat_app_repos_fixture_0123', '\n'.join(job.lines))

    def test_a_seed_published_before_the_build_finishes_is_the_first_image(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        api = FakeAppGitHub(runs=[{'status': 'in_progress'}])
        client, job = repos.AppRepos(repo_runner(), api.config), self.job()
        seen = []

        def opener(request):
            seen.append(request.full_url)
            return ghcr_opener(request, seed=True)
        image = repos.create_app_repo(client.runner, client, job, repo.Request('notes'), sleep=lambda _: None,
                                      opener=opener)
        self.assertEqual(image, f'ghcr.io/samclement/notes:0-9f8e7d6@{DIGEST}')
        self.assertIn('https://ghcr.io/v2/samclement/notes/manifests/0-9f8e7d6', seen)
        self.assertTrue(job.lines[-1].startswith(f'Seeded {image}'), job.lines[-1])
        self.assertFalse([r for r in api.requests if r.url.path.endswith('/actions/runs')], 'no wait for the build')

    def test_a_missing_or_refused_image_webhook_is_a_warning_not_a_failure(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        for name, api, expected in (('no token', FakeAppGitHub(webhook=''), 'IMAGE_WEBHOOK_TOKEN is not set'),
                                    ('refused', FakeAppGitHub(hooks_status=403), 'Webhooks: Read and write')):
            with self.subTest(name=name):
                client, job = repos.AppRepos(repo_runner(), api.config), self.job()
                image = repos.create_app_repo(client.runner, client, job, repo.Request('notes'), sleep=lambda _: None,
                                              opener=ghcr_opener)
                self.assertTrue(image.startswith('ghcr.io/samclement/notes:1-'))
                warning = next(line for line in job.lines if line.startswith('Warning: no image webhook'))
                self.assertIn(expected, warning)
                self.assertIn('make app-hooks', warning)

    def test_a_permission_refusal_names_the_permission_to_check(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        api = FakeAppGitHub()
        handle = api.handle
        api.handle = None
        def refuse_blobs(request):
            if request.url.path.endswith('/git/blobs'):
                return httpx.Response(403, json={'message': 'Resource not accessible by personal access token'})
            return handle(request)
        config = repos.AppReposToken(api.config.token, transport=httpx.MockTransport(refuse_blobs))
        client = repos.AppRepos(repo_runner(), config)
        with self.assertRaisesRegex(actions.ActionError, 'Contents: Read and write and Workflows: Read and write'):
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None)

    def test_writes_only_to_a_repository_it_created(self):
        from swhurl.console import repos
        api = FakeAppGitHub()
        client = repos.AppRepos(FakeRunner(), api.config)
        with self.assertRaisesRegex(actions.ActionError, 'writes only to a repository it created'):
            client.push_first_commit('hello-ts', Path('.'), 'x', sleep=lambda _: None)
        self.assertEqual(api.requests, [])
        self.assertFalse([name for name in dir(client) if 'delete' in name.lower()], 'no deleting method')

    def test_an_existing_name_or_a_failed_first_run_stops_with_the_reason(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        api = FakeAppGitHub(existing=('notes',))
        client = repos.AppRepos(repo_runner(), api.config)
        with self.assertRaisesRegex(
                actions.ActionError,
                r'Repository samclement/notes already exists on GitHub\. Setup stopped before creating a new '
                r'repository\. Choose another name and retry from New app\.'):
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None)
        self.assertEqual(api.writes(), [])
        self.assertEqual(client.runner.calls, [], 'nothing rendered')
        api = FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'failure'}])
        client = repos.AppRepos(repo_runner(), api.config)
        add = 'run make app-new NAME=notes ARGS="--from-repo samclement/notes --env staging --image <image the run prints>"'
        with self.assertRaises(actions.ActionError) as failed:
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None,
                                  opener=ghcr_opener)
        self.assertIn('ended failure: https://run/1', str(failed.exception))
        self.assertIn(add, str(failed.exception))
        ticks = iter(range(0, 10_000, 100))
        client = repos.AppRepos(repo_runner(), FakeAppGitHub(runs=[None]).config)
        with self.assertRaises(actions.ActionError) as late:
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None,
                                  clock=lambda: next(ticks), opener=ghcr_opener)
        self.assertIn('no finished Container run after 10 minutes', str(late.exception))
        self.assertIn(add, str(late.exception))


class TeamsPageTests(unittest.TestCase):
    """The Teams page: the registry on main beside the apps that run, and changes as pull requests."""

    def client(self, namespaces=None, **kwargs):
        staging = (staging_app('orders'), staging_app('billing'), staging_app('ghost'))
        self.runner = tree_fake(staging=staging)
        labels = namespaces or {'orders-staging': 'payments', 'billing-staging': 'payments', 'ghost-staging': 'retired'}
        self.runner._rules.insert(0, (('kubectl', 'get', 'namespaces'), Result((), 0, json.dumps({'items': [
            {'metadata': {'name': name, 'labels': {'platform.swhurl.com/team': team}}} for name, team in labels.items()]}))))
        self.jobs = actions.Jobs(self.runner, audit=lambda line: None, inline=True)
        self.api = FakeGitHub()
        return TestClient(app_under_test(self.runner, jobs=self.jobs, github=self.api.github, **kwargs))

    def test_shows_each_team_with_its_apps_and_the_apps_left_without_one(self):
        page = self.client().get('/teams', headers=WHO).text
        self.assertIn('<h2>platform</h2>', page)
        self.assertLess(page.index('<h2>payments</h2>'), page.index('<h2>platform</h2>'), 'alphabetical, not the registry\'s order')
        self.assertNotIn('>default</span>', page, 'no team is special')
        self.assertIn('The platform itself', page)
        self.assertIn('<a href="/apps/billing/staging" title="billing/staging:', page, 'each app, with what runs where')
        self.assertIn('<a href="/apps?team=payments">2 apps on the Deployments list</a>', page)
        self.assertIn('Apps without a registered team', page)
        self.assertIn('<td>retired</td>', page)
        self.assertIn('<a href="/apps/ghost/staging">ghost</a>', page)
        self.assertIn('Move its 2 app(s) to', page, 'a team with apps is deleted only by moving them')
        self.assertEqual(page.count('action="/teams/remove"'), 2, 'every team can be deleted: none is special')
        self.assertEqual(page.count('name="rename"'), 2, 'and renamed')
        self.assertNotIn('default team', page)
        self.assertEqual(page.count('action="/teams/edit"'), 2, 'one Edit per team: its description and its name are one form')
        self.assertEqual(page.count('<details class="act" name="team-form"'), 7,
                         'new team, and per team edit, channel and delete: one open at a time')
        self.assertIn('href="/teams" class="here"', page)
        self.assertTrue(all(call[0] == 'kubectl' and 'get' in call for call in self.runner.calls), 'the page only reads')

    def test_an_apps_language_comes_from_its_own_repository_and_is_asked_for_once(self):
        asked = []

        def opener(request):
            asked.append(request.full_url)
            if 'ghost' in request.full_url:
                raise OSError('GitHub is away')
            return {}, b'version: 1\nkind: worker\nstack: kotlin\n'
        c = self.client(repo_opener=opener)
        page = c.get('/teams', headers=WHO).text
        self.assertEqual(asked[0], 'https://api.github.com/repos/samclement/billing/contents/swhurl.yaml')
        self.assertEqual(page.count('<span class="pill">Kotlin</span>'), 1, 'billing; orders is not asked for once GitHub fails')
        self.assertEqual(len(asked), 2, 'after one failure the rest are not asked for: the page must not wait once per app')
        c.get('/teams', headers=WHO)
        self.assertEqual(len(asked), 2, 'kept: an app does not change language')

    def test_changes_run_the_clones_team_commands_and_wait_for_review(self):
        cases = (
            ('add', {'name': 'billing', 'description': ' Invoices  and\nrefunds '},
             ('team-add', 'billing', '--description=Invoices and refunds'), '[console] teams: add billing'),
            ('edit', {'name': 'payments', 'description': 'Orders', 'rename': 'orders'},
             ('team-edit', 'payments', '--description=Orders', '--rename=orders'), '[console] teams: change payments'),
            ('edit', {'name': 'payments', 'description': '', 'rename': ''},
             ('team-edit', 'payments', '--description='), '[console] teams: change payments'),
            ('remove', {'name': 'payments', 'move_apps_to': 'platform'},
             ('team-remove', 'payments', '--move-apps-to=platform'), '[console] teams: remove payments'),
        )
        for action, form, command, title in cases:
            with self.subTest(action=action, form=form):
                c = self.client()
                response = c.post(f'/teams/{action}', data=form, headers=WHO, follow_redirects=False)
                self.assertEqual(response.status_code, 303, response.text)
                tool = next(call for call in self.runner.calls if call[1:3] == ('-m', 'swhurl'))
                self.assertEqual(tool[3:], command)
                job = self.jobs.get(1)
                self.assertEqual((job.state, job.link), ('succeeded', 'https://github.com/x/pull/7'))
                self.assertEqual(self.api.sent('POST /pulls')[0]['title'], title)
                self.assertEqual(self.api.sent('POST /issues/7/labels'), [], 'a team change waits for review')
                self.assertEqual(cluster.target(job.unit), (f"team {form['name']}", f"/teams#{form['name']}"))

    def test_each_team_card_says_whether_it_has_a_channel_and_offers_to_set_replace_or_remove_it(self):
        page = self.client().get('/teams', headers=WHO).text
        self.assertEqual(page.count('<span class="pill ok" title="Gets a copy of the deployment and health messages of this '
                                    'team\'s apps">notification channel · ntfy</span>'), 1, 'payments has one')
        self.assertEqual(page.count('No notification channel: messages about its apps go to the platform topic only.'), 1)
        self.assertEqual(page.count('action="/team-channel/set"'), 2, 'every team can be given a channel or a new address')
        self.assertEqual(page.count('Open new address PR'), 1)
        self.assertEqual(page.count('Open channel PR'), 1)
        self.assertEqual(page.count('action="/team-channel/remove"'), 1, 'only a team that has one can remove it')
        self.assertEqual(page.count('data-generate hidden>Generate</button>'), 2)
        self.assertNotIn('payments-phone', page, 'a channel has no name to manage: it is the team\'s')
        self.assertNotIn('name="notify"', page)
        self.assertNotIn('ntfy.sh/', page.replace('https://ntfy.sh/your-topic', ''), 'no address is ever on the page')
        unreadable = FakeGitHub(fail={'GET /contents/platform/notify/routes.yaml': (404, 'Not Found')})
        page = TestClient(app_under_test(self.runner, jobs=self.jobs, github=unreadable.github)).get('/teams', headers=WHO).text
        self.assertEqual(page.count('No notification channel'), 2, 'unreadable routes: no channel is claimed')
        page = self.client().get('/new', headers=WHO).text
        self.assertEqual(page.count('>notification channel</span>'), 1, 'payments, on New app')
        self.assertIn('>no notification channel</span>', page)

    def test_a_channels_address_reaches_only_the_commands_standard_input(self):
        address = 'https://ntfy.sh/swhurl-0123456789abcdef01234567'
        c = self.client()
        received = []
        self.runner._rules.insert(0, ((sys.executable, '-m', 'swhurl', 'team-channel'), lambda args, text: (
            received.append(text), Result(args, 0, '[OK] channel payments (ntfy) added; its address is not shown again\n'))[1]))
        response = c.post('/team-channel/set', data={'name': 'payments', 'transport': 'ntfy', 'address': f' {address} '},
                          headers=WHO, follow_redirects=False)
        self.assertEqual(response.status_code, 303, response.text)
        self.assertEqual(received, [address + '\n'])
        tool = next(call for call in self.runner.calls if call[1:3] == ('-m', 'swhurl'))
        self.assertEqual(tool[3:], ('team-channel', 'payments', '--transport=ntfy'))
        job = self.jobs.get(1)
        self.assertEqual((job.state, cluster.target(job.unit)), ('succeeded', ('team payments', '/teams#payments')))
        self.assertEqual(self.api.sent('POST /pulls')[0]['title'], '[console] teams: set the notification channel of payments')
        self.assertEqual(self.api.sent('POST /issues/7/labels'), [], 'a channel change waits for review')
        everywhere = '\n'.join([*job.lines, *(' '.join(call) for call in self.runner.calls), *self.runner.echoed,
                                json.dumps(self.api.sent('POST /pulls')), json.dumps(self.api.sent('POST /git/commits')),
                                c.get('/jobs/1', headers=WHO).text, c.get('/teams', headers=WHO).text])
        self.assertNotIn('0123456789abcdef', everywhere)
        self.assertIn('<redacted>', self.runner.redact(f'posting to {address}'))
        c = self.client()
        self.assertEqual(c.post('/team-channel/remove', data={'name': 'payments'}, headers=WHO, follow_redirects=False).status_code, 303)
        tool = next(call for call in self.runner.calls if call[1:3] == ('-m', 'swhurl'))
        self.assertEqual(tool[3:], ('team-channel', 'payments', '--remove'))
        self.assertEqual(self.api.sent('POST /pulls')[0]['title'], '[console] teams: remove the notification channel of payments')

    def test_a_malformed_channel_opens_nothing_and_never_echoes_the_address(self):
        c = self.client()
        cases = (('set', {'name': 'Bad Name', 'address': 'https://ntfy.sh/private-topic'}),
                 ('set', {'name': 'x', 'address': 'http://ntfy.sh/private-topic'}),
                 ('set', {'name': 'x', 'address': 'https://ntfy.sh/private-topic?x=1'}),
                 ('set', {'name': 'x', 'address': ''}), ('set', {'name': 'x', 'transport': 'pigeon', 'address': 'https://ntfy.sh/t'}),
                 ('remove', {'name': '--root=/'}), ('explode', {'name': 'x', 'address': 'https://ntfy.sh/private-topic'}))
        for action, form in cases:
            with self.subTest(action=action, form=form):
                response = c.post(f'/team-channel/{action}', data=form, headers=WHO)
                self.assertEqual(response.status_code, 400)
                self.assertNotIn('private-topic', response.text)
        self.assertFalse(any(call[1:3] == ('-m', 'swhurl') for call in self.runner.calls))
        without_token = TestClient(app_under_test(tree_fake(), jobs=actions.Jobs(tree_fake(), audit=lambda line: None, inline=True)))
        self.assertEqual(without_token.post('/team-channel/set', data={'name': 'x', 'address': 'https://ntfy.sh/t'}, headers=WHO).status_code, 409)

    def test_malformed_input_opens_nothing_and_a_refused_command_shows_its_words(self):
        c = self.client()
        for action, form in (('add', {'name': 'Bad Name'}), ('add', {'name': ''}), ('edit', {'name': 'a', 'rename': '--root=/'}),
                             ('remove', {'name': 'a', 'move_apps_to': 'x y'}), ('add', {'name': 'a', 'description': 'd' * 201}),
                             ('explode', {'name': 'a'})):
            with self.subTest(action=action, form=form):
                self.assertEqual(c.post(f'/teams/{action}', data=form, headers=WHO).status_code, 400)
        self.assertFalse(any(call[1:3] == ('-m', 'swhurl') for call in self.runner.calls))
        message = '[ERROR] team payments still owns billing, orders: move them with --move-apps-to <team>'
        c = self.client()
        self.runner._rules.insert(0, ((sys.executable, '-m', 'swhurl'), Result((), 2, '', message + '\n')))
        c.post('/teams/remove', data={'name': 'payments'}, headers=WHO)
        job = self.jobs.get(1)
        self.assertEqual(job.state, 'failed')
        self.assertIn(message, job.lines)
        self.assertEqual(self.api.sent('POST /pulls'), [])
        without_token = TestClient(app_under_test(tree_fake(), jobs=actions.Jobs(tree_fake(), audit=lambda line: None, inline=True)))
        self.assertEqual(without_token.post('/teams/add', data={'name': 'billing'}, headers=WHO).status_code, 409)
        self.assertIn('cannot open pull requests', without_token.get('/teams', headers=WHO).text)

    def test_an_unreadable_registry_is_said_so_and_raises_no_false_alarm(self):
        self.client()
        unreadable = FakeGitHub(fail={'GET /contents/apps/teams.yaml': (404, 'Not Found')})
        page = TestClient(app_under_test(self.runner, jobs=self.jobs, github=unreadable.github)).get('/teams', headers=WHO).text
        self.assertIn('no app can be called unregistered', page)
        self.assertNotIn('Apps without a registered team', page)
        self.assertIn('<section class="card" id="payments">', page, 'the teams apps name are still shown')
        self.assertIn('<section class="card" id="retired">', page)


class NewRepoRouteTests(unittest.TestCase):
    def test_the_form_creates_the_repository_then_opens_the_platform_pr(self):
        runner = repo_runner(tree_fake())
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        platform_api, app_api = FakeGitHub(), FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}])
        c = TestClient(app_under_test(runner, jobs=jobs, github=platform_api.github, app_repos=app_api.config))
        page = c.get('/new', headers=WHO).text
        self.assertIn('What happens', page)
        self.assertNotIn('Deploy an existing image', page)
        self.assertIn('action="/new/repo"', page)
        self.assertIn('name="stack" value="typescript" checked', page)
        self.assertIn('name="type" value="web-app" data-exposure="authenticated-web" data-kind="web" checked', page)
        self.assertIn('name="type" value="api" data-exposure="private" data-kind="web" >', page)
        self.assertIn('<span class="pill ok">SQLite</span>', page, 'what each type includes')
        self.assertIn('data-types="web-app api worker scheduled-job"><input type="radio" name="stack" value="typescript"', page)
        self.assertIn('data-types="api worker scheduled-job"><input type="radio" name="stack" value="kotlin"', page, 'no Kotlin web app')
        response = c.post('/new/repo', data={'name': 'notes', 'type': 'web-app', 'stack': 'typescript', 'team': 'platform', 'description': 'Take notes',
                                             'exposure': 'authenticated-web', 'host': ''}, headers=WHO,
                          follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (303, '/jobs/1'))
        job = jobs.get(1)
        self.assertEqual((job.state, job.link, job.unit), ('succeeded', 'https://github.com/x/pull/7', 'notes/staging'),
                         '\n'.join(job.lines))
        app_new = next(c for c in runner.calls if c[3:4] == ('app-new',))
        self.assertEqual(app_new[4:], ('notes', '--from-repo=samclement/notes', '--env=staging',
                                       f'--image=ghcr.io/samclement/notes:1-9f8e7d6@{DIGEST}',
                                       '--team=platform', '--exposure=authenticated-web'))
        copy = next(c for c in runner.calls if 'copy' in c)
        for answer in ('kind=web', 'database=sqlite', 'api=openapi', 'frontend=vite'):
            self.assertIn(answer, copy, 'the type sets every answer')
        self.assertEqual(platform_api.sent('POST /issues/7/labels'), [{'labels': ['auto-merge']}], 'a new staging app')
        self.assertIn('https://github.com/x/pull/7', c.get('/jobs/1', headers=WHO).text)
        body = platform_api.sent('POST /pulls')[0]['body']
        self.assertIn('created https://github.com/samclement/notes from', body)

    def test_a_new_app_that_connects_passes_its_choices_and_waits_for_review(self):
        staging = (staging_app('orders', {'SWHURL_EVENTS_PUBLISH': 'created'}), staging_app('billing'))
        runner = repo_runner(tree_fake(staging=staging))
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        platform_api, app_api = FakeGitHub(), FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}])
        c = TestClient(app_under_test(runner, jobs=jobs, github=platform_api.github, app_repos=app_api.config))
        form = 'name=notes&type=worker&stack=typescript&team=payments&calls=orders&calls=billing&subscribe=orders.created'
        response = c.post('/new/repo', content=form, headers={**WHO, 'content-type': 'application/x-www-form-urlencoded'},
                          follow_redirects=False)
        self.assertEqual(response.status_code, 303, response.text)
        self.assertEqual(jobs.get(1).state, 'succeeded', '\n'.join(jobs.get(1).lines))
        app_new = next(c for c in runner.calls if c[3:4] == ('app-new',))
        self.assertEqual(app_new[7:], ('--image=ghcr.io/samclement/notes:1-9f8e7d6@' + DIGEST, '--team=payments',
                                       '--calls=orders,billing', '--subscribe=orders.created'))
        self.assertEqual(platform_api.sent('POST /issues/7/labels'), [],
                         'it changes other apps, so it is not labelled to merge itself')
        refused = c.post('/new/repo', content='name=notes2&type=worker&stack=typescript&team=platform&calls=stranger',
                         headers={**WHO, 'content-type': 'application/x-www-form-urlencoded'})
        self.assertEqual(refused.status_code, 400)
        self.assertIn('stranger: not a web app, or an event, that staging has now', refused.text)
        self.assertEqual(len(app_api.writes()), len([w for w in app_api.writes() if 'notes2' not in w[1]]),
                         'no repository is created for a refused form')

    def test_a_worker_gets_no_route_and_its_types_answers(self):
        runner = repo_runner(tree_fake())
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        c = TestClient(app_under_test(runner, jobs=jobs, github=FakeGitHub().github, app_repos=FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}]).config))
        c.post('/new/repo', data={'name': 'notes', 'type': 'worker', 'stack': 'typescript', 'team': 'platform',
                                  'exposure': 'authenticated-web', 'host': ''}, headers=WHO)
        self.assertEqual(jobs.get(1).state, 'succeeded', '\n'.join(jobs.get(1).lines))
        app_new = next(c for c in runner.calls if c[3:4] == ('app-new',))
        self.assertFalse([a for a in app_new if a.startswith(('--exposure', '--host'))], 'swhurl.yaml makes a worker private')
        copy = next(c for c in runner.calls if 'copy' in c)
        self.assertIn('kind=worker', copy)
        self.assertIn('database=none', copy)
        self.assertIn('Rendering samclement/swhurl-app-template-typescript (typescript) for notes with api=none, database=none, '
                      'kind=worker', jobs.get(1).lines)
        for form, message in (({'type': 'cron'}, 'type must be one of web-app, api, worker'),
                              ({}, 'type must be one of web-app, api, worker'),
                              ({'type': 'web-app', 'stack': 'kotlin'}, 'a Web app is built with typescript, not kotlin'),
                              ({'type': 'worker', 'host': 'notes.homelab.swhurl.com'}, 'a worker has no web address')):
            with self.subTest(message=message):
                response = c.post('/new/repo', data={'name': 'notes2', 'stack': 'typescript', 'team': 'platform', **form}, headers=WHO)
                self.assertEqual(response.status_code, 400)
                self.assertIn(message, response.text)

    def test_kotlin_is_a_stack_with_its_own_template(self):
        runner = repo_runner(tree_fake())
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        c = TestClient(app_under_test(runner, jobs=jobs, github=FakeGitHub().github,
                                      app_repos=FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}]).config))
        page = c.get('/new', headers=WHO).text
        self.assertIn('name="stack" value="kotlin"', page)
        self.assertIn('Kotlin on Micronaut', page)
        c.post('/new/repo', data={'name': 'notes', 'type': 'api', 'stack': 'kotlin', 'team': 'platform'}, headers=WHO)
        self.assertEqual(jobs.get(1).state, 'succeeded', '\n'.join(jobs.get(1).lines))
        copy = next(c for c in runner.calls if 'copy' in c)
        self.assertIn('https://github.com/samclement/swhurl-app-template-kotlin.git', copy)
        app_new = next(c for c in runner.calls if c[3:4] == ('app-new',))
        self.assertIn('--exposure=private', app_new, 'an API starts private')

    def test_template_questions_are_read_on_first_use_whatever_the_clock(self):
        from unittest import mock
        runner = repo_runner(tree_fake())
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        with mock.patch('swhurl.console.server.time.monotonic', return_value=5.0):  # a host booted 5 s ago
            c = TestClient(app_under_test(runner, jobs=jobs, github=FakeGitHub().github,
                                          app_repos=FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}]).config))
            response = c.post('/new/repo', data={'name': 'notes', 'type': 'api', 'stack': 'typescript', 'team': 'platform'},
                              headers=WHO, follow_redirects=False)
        self.assertEqual(response.status_code, 303, response.text)

    def test_bad_input_or_a_missing_token_creates_nothing(self):
        runner = repo_runner(tree_fake())
        app_api = FakeAppGitHub()
        c = TestClient(app_under_test(runner, jobs=actions.Jobs(runner, inline=True), github=FakeGitHub().github,
                                         app_repos=app_api.config))
        for form, message in (({'name': 'Notes', 'stack': 'typescript', 'team': 'platform'}, 'DNS label'),
                              ({'name': 'notes', 'stack': 'cobol'}, 'stack must be one of'),
                              ({'name': 'notes', 'type': 'web-app', 'stack': 'typescript', 'team': 'platform', 'host': 'bad host'}, 'not a DNS name')):
            with self.subTest(message=message):
                response = c.post('/new/repo', data=form, headers=WHO)
                self.assertEqual(response.status_code, 400)
                self.assertIn(message, response.text)
        c = TestClient(app_under_test(runner, jobs=actions.Jobs(runner, inline=True), github=FakeGitHub().github))
        self.assertIn('No token for creating repositories', c.get('/new', headers=WHO).text)
        self.assertEqual(c.post('/new/repo', data={'name': 'notes', 'stack': 'typescript', 'team': 'platform'}, headers=WHO).status_code, 409)
        self.assertTrue(all(call[0] == 'kubectl' and 'get' in call for call in runner.calls), 'only the form\'s reads')
        self.assertEqual(app_api.requests, [])
