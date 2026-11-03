"""Console/Go status seam: immutable inputs, exact schema, live request scope, and POST enforcement.

Health/currentness/readiness policy is exercised by v2/observe/changes_test.go and state_test.go;
these tests script Go's answers and prove the console does not replace its decisions.
"""
import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import httpx
from console_observation import DIGEST, HEAD, answer, archive, report
from starlette.testclient import TestClient
from test_console import WHO, fake, unit

from swhurl.apps import ops, state
from swhurl.console import actions, bundle, observation, server, status
from swhurl.console import github as gh
from swhurl.console.reading import Files
from swhurl.run import CommandError, FakeRunner, Result

OLD, NEW = 'b' * 40, 'c' * 40
ORIGIN = {**WHO, 'Origin': 'http://testserver'}


def inputs(app='web', env='staging', revision=HEAD):
    return {f'v2-records/{app}/app.json': json.dumps({'application': app, 'team': 'demo'}),
            f'v2-records/{app}/{env}/instance.json': json.dumps({'configuration': {'replicas': 2, 'memoryMiB': 256},
                                                             'grants': {'exposure': 'private'}}),
            f'v2-records/{app}/{env}/release.json': json.dumps({'runtime': {'kind': 'worker'}, 'database': 'sqlite',
                                                            'artifact': {'image': 'ghcr.io/x/web', 'digest': DIGEST}}),
            f'v2-apps/instances/{app}/{env}/helmrelease.json': json.dumps({'spec': {'values': {'controllers': {'main': {
                'containers': {'main': {'env': {'PINNED': revision}}}}}}}}),
            'v2/testdata/profile.json': json.dumps({'pinned': revision}),
            'v2-records/calls.json': json.dumps({'pinned': revision}),
            'platform/egress/allocation.json': json.dumps({'pinned': revision})}


class Repository:
    def __init__(self):
        self.head = HEAD
        self.requests = []
        self.comparison = {'status': 'ahead', 'files': []}
        self.fail = ''
        self.production = False
        self.github = gh.GitHub('x/y', 'fixture-token', transport=httpx.MockTransport(self.handle))

    def handle(self, request):
        self.requests.append(request)
        path = request.url.path
        if self.fail and self.fail in path:
            return httpx.Response(503)
        if '/git/ref/' in path:
            return httpx.Response(200, json={'object': {'sha': self.head}})
        if '/tarball/' in path:
            files = inputs(revision=path.rsplit('/', 1)[-1])
            if self.production:
                files.update(inputs(env='prod'))
                release = json.loads(files['v2-records/web/prod/release.json'])
                release['artifact']['digest'] = 'sha256:' + '0' * 64
                files['v2-records/web/prod/release.json'] = json.dumps(release)
            return httpx.Response(200, content=archive(files))
        if '/compare/' in path:
            return httpx.Response(200, json=self.comparison)
        if path.endswith('/pulls'):
            return httpx.Response(200, json=[])
        if '/contents/' in path:
            return httpx.Response(404)
        raise AssertionError(path)


class AdapterTests(unittest.TestCase):
    def test_one_bulk_invocation_per_request_and_only_bytes_cached_by_sha(self):
        remote, runner, cached, roots = Repository(), FakeRunner(), {}, []
        def run(args, _input):
            root = Path(args[args.index('--repo-root') + 1])
            roots.append(root)
            self.assertEqual(Path(args[args.index('--changes') + 1]).parent, root)
            for path in ('v2/testdata/profile.json', 'v2-records/calls.json', 'platform/egress/allocation.json'):
                self.assertEqual(json.loads((root / path).read_text())['pinned'], HEAD)
            self.assertEqual((root / 'v2-records/web/staging/instance.json').read_text(), inputs()['v2-records/web/staging/instance.json'])
            self.assertIn('--in-cluster', args)
            self.assertIn('--all', args)
            self.assertEqual(args[args.index('--source') + 1], 'console')
            return answer(args, {('web', 'staging'): report(env='staging')})
        runner.on('swhurl-v2', 'status', handler=run)
        for _ in range(2):
            with Files(runner, remote.github, bundles=cached) as files:
                self.assertIs(status.read(runner, files, [], in_cluster=True),
                              status.read(runner, files, [], in_cluster=True))
                self.assertEqual(json.loads(files('v2-apps/instances/web/staging/helmrelease.json')),
                                 json.loads(inputs()['v2-apps/instances/web/staging/helmrelease.json']))
                self.assertTrue(roots[-1].is_dir())
            self.assertFalse(roots[-1].exists())
        self.assertEqual(len(runner.calls), 2, 'live status is never cached between requests')
        self.assertEqual(sum('/tarball/' in r.url.path for r in remote.requests), 1)
        self.assertEqual(sum('/git/ref/' in r.url.path for r in remote.requests), 2)
        self.assertEqual(set(cached), {HEAD})

    def test_distinct_comparisons_once_and_rename_names_are_facts_not_python_scopes(self):
        remote = Repository()
        remote.comparison['files'] = [{'filename': 'v2-records/web/staging/instance.json', 'previous_filename': 'removed.json'}]
        units = [unit('app-web-staging'), unit('app-other-staging'), unit('app-third-staging')]
        for one, sha in zip(units, (OLD, OLD, NEW), strict=True):
            one['status']['lastAppliedRevision'] = 'main@sha1:' + sha
        with Files(FakeRunner(), remote.github) as files:
            result = status.comparisons(FakeRunner(), files, units, HEAD)
        self.assertEqual(len(result['comparisons']), 2)
        self.assertEqual(sum('/compare/' in r.url.path for r in remote.requests), 2)
        for pair in result['comparisons']:
            self.assertEqual(pair['status'], 'complete')
            self.assertEqual(pair['paths'], ['v2-records/web/staging/instance.json', 'removed.json'])

    def test_failed_truncated_nonancestor_or_malformed_compare_never_means_empty_complete(self):
        for response in ({'status': 'ahead', 'files': [{'filename': f'{i}.json'} for i in range(300)]},
                         {'status': 'diverged', 'files': []}, {'status': 'behind', 'files': []},
                         {'status': 'ahead', 'files': None}, {'status': 'ahead'}, None):
            remote = Repository()
            remote.comparison = response
            one = unit('app-web-staging')
            one['status']['lastAppliedRevision'] = 'main@sha1:' + OLD
            with Files(FakeRunner(), remote.github) as files:
                pair = status.comparisons(FakeRunner(), files, [one], HEAD)['comparisons'][0]
            self.assertEqual((pair['status'], pair['paths']), ('unavailable', []))
        remote.fail = '/compare/'
        with Files(FakeRunner(), remote.github) as files:
            self.assertEqual(status.comparisons(FakeRunner(), files, [one], HEAD)['comparisons'][0]['status'], 'unavailable')

    def test_host_credential_is_explicit_private_and_removed_even_on_command_failure(self):
        remote, runner, roots = Repository(), FakeRunner(), []
        runner.on('kubectl', 'config', 'view', stdout='sensitive-config')
        def refuse(args, _input):
            path = Path(args[args.index('--kubeconfig') + 1])
            roots.append(path.parent)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(path.read_text(), 'sensitive-config')
            self.assertNotIn('--in-cluster', args)
            return Result(args, 1, stderr='unavailable')
        runner.on('swhurl-v2', 'status', handler=refuse)
        with self.assertRaises(actions.ActionError), Files(runner, remote.github) as files:
            status.read(runner, files, [], in_cluster=False)
        self.assertFalse(roots[0].exists())
        self.assertEqual(runner.redact('sensitive-config'), '<redacted>')

    def test_local_dev_pins_head_and_uses_runner_archive_not_github_or_working_files(self):
        runner = fake(units=[unit('app-web-staging')], releases=[])
        with tempfile.TemporaryDirectory() as tmp, Files(runner, None, local_root=Path(tmp)) as files:
            sha, root = files.inputs()
            self.assertEqual(sha, HEAD)
            self.assertEqual(files('v2-records/web/staging/instance.json'),
                             runner.status_inputs['v2-records/web/staging/instance.json'])
            self.assertTrue(root.is_dir())
        self.assertFalse(root.exists())
        self.assertEqual(runner.calls[0], ('git', 'rev-parse', 'HEAD'))
        self.assertIn(HEAD, runner.calls[1])

    def test_archive_rejects_traversal_and_filters_unneeded_material(self):
        with self.assertRaises(actions.ActionError):
            bundle.filtered(archive({'../escape.json': '{}'}))
        self.assertEqual(bundle.filtered(archive({'README.md': 'ignored', 'age.agekey': 'not used'})), {})


class SchemaTests(unittest.TestCase):
    def test_null_is_not_false_zero_or_empty_and_no_defaults_are_created(self):
        for suspended, count, digests in ((None, None, None), (False, 0, [])):
            found = report()
            found['live']['unit'].update(suspended=suspended, conditions=None)
            found['live']['workload']['readyReplicas'] = count
            found['live']['running']['digests'] = digests
            decoded = observation.decode(json.dumps(found), HEAD, single=True)
            self.assertIs(decoded['live']['unit']['suspended'], suspended)
            self.assertEqual(decoded['live']['workload']['readyReplicas'], count)
            self.assertEqual(decoded['live']['running']['digests'], digests)

    def test_malformed_versions_missing_fields_unknown_shape_and_wrong_types_are_refused(self):
        for path, value in ((('version',), 4), (('version',), True), (('extra',), 1), (('promotion', 'ready'), None),
                            (('live', 'unit', 'conditions'), {}), (('live', 'workload', 'readyReplicas'), False),
                            (('routes',), None), (('state', 'name'), 'new_state'), (('target', 'digest'), None)):
            found = report()
            target = found
            for part in path[:-1]:
                target = target[part]
            target[path[-1]] = value
            with self.subTest(path=path, value=value), self.assertRaises(actions.ActionError):
                observation.decode(json.dumps(found), HEAD, single=True)
        for text in ('{}', 'null', '{"kind":1,"kind":2}', json.dumps({**report(), 'promotion': {}})):
            with self.assertRaises(actions.ActionError):
                observation.decode(text, HEAD, single=True)
        with self.assertRaises(actions.ActionError):
            observation.decode(json.dumps(report()), NEW, single=True)

    def test_bulk_envelope_requires_its_version_and_array(self):
        valid = json.loads(answer(('--revision', HEAD, '--all'), {}).stdout)
        self.assertEqual(observation.decode(json.dumps(valid), HEAD), {})
        for key, value in (('version', 2), ('instances', None), ('revision', OLD), ('kind', 'v2-observation')):
            with self.assertRaises(actions.ActionError):
                observation.decode(json.dumps({**valid, key: value}), HEAD)


class PageAndPromotionTests(unittest.TestCase):
    def setUp(self):
        self.remote = Repository()
        self.runner = fake(units=[unit('app-web-staging')], releases=[])
        self.observed = report(env='staging')
        self.captured = []
        self.runner._rules.insert(0, (('swhurl-v2', 'status'), self.observe))
        self.runner.on('swhurl-v2', '--help', stdout='usage')
        self.jobs = actions.Jobs(self.runner, inline=True)
        self.client = TestClient(server.create_app(self.runner, github=self.remote.github, jobs=self.jobs,
                                                   repo_opener=lambda _: ({}, b'kind: worker\nstack: typescript\n')))

    def observe(self, args, _input):
        self.captured.append(args)
        found = copy.deepcopy(self.observed)
        found['target']['revision'] = args[args.index('--revision') + 1]
        return answer(args, {('web', 'staging'): found})

    def test_list_detail_agree_and_desired_settings_variables_worker_routes_are_explicit(self):
        self.observed['state'] = {'name': 'updating', 'reason': 'replicas_not_ready'}
        for path in ('/apps', '/apps/web/staging'):
            reply = self.client.get(path, headers=WHO)
            self.assertEqual(reply.status_code, 200)
            self.assertIn('state updating', reply.text)
        self.assertEqual(len(self.captured), 2)
        self.assertTrue(all('--all' in call for call in self.captured))
        self.assertIn('Environment (desired)', reply.text)
        self.assertIn('placeholder="256"', reply.text)
        self.assertIn('PINNED', reply.text)
        self.assertIn('sqlite', reply.text)
        self.assertIn('a worker, with nothing to open', reply.text)
        self.assertNotIn('/schedule', reply.text)
        self.assertNotIn('secret.sops.yaml', reply.text)
        self.assertEqual(sum('/tarball/' in r.url.path for r in self.remote.requests), 1)

    def test_routes_tls_problems_and_unknown_evidence_display_without_judgement(self):
        self.observed.update(routes=[{'name': 'web', 'outcome': 'unavailable', 'reason': 'read_failed', 'hosts': []}],
                             certificates=[{'name': 'tls', 'outcome': 'absent', 'reason': 'not_found'}],
                             problems=[{'pod': 'web-2', 'container': 'main', 'reason': 'ImagePullBackOff', 'message': '<not found>'}])
        reply = self.client.get('/apps/web/staging', headers=WHO)
        for text in ('unavailable', 'tls: absent', 'ImagePullBackOff', '&lt;not found&gt;'):
            self.assertIn(text, reply.text)
        self.assertNotIn('Fix:', reply.text, 'legacy policy hints were deliberately removed')

    def test_unavailable_binary_schema_or_repository_never_falls_back_or_offers_forms(self):
        for failure in ('binary', 'schema', 'github'):
            with self.subTest(failure=failure):
                if failure == 'binary':
                    def missing(args, _input):
                        raise CommandError(args, 'missing required command: swhurl-v2')
                    self.runner._rules.insert(0, (('swhurl-v2', 'status'), missing))
                elif failure == 'schema':
                    self.runner._rules.insert(0, (('swhurl-v2', 'status'), Result((), stdout='{}')))
                else:
                    self.remote.fail = '/git/ref/'
                reply = self.client.get('/apps/web/staging', headers=WHO)
                self.assertEqual(reply.status_code, 502)
                self.assertIn('Go status unavailable', reply.text)
                self.assertNotIn('/scale', reply.text)
                if failure != 'github':
                    self.runner._rules.pop(0)

    def test_wrong_report_identity_is_unavailable_not_an_uncaught_page_error(self):
        self.observed['target']['namespace'] = 'other-staging'
        self.assertEqual(self.client.get('/apps', headers=WHO).status_code, 502)

    def test_pending_and_removing_rows_survive_without_fabricated_records(self):
        self.observed['live']['unit']['status'] = 'absent'
        reply = self.client.get('/apps/web/staging', headers=WHO)
        self.assertIn('Deployment pending', reply.text)
        self.runner._rules.insert(0, (('swhurl-v2', 'status'), lambda args, _: answer(args, {})))
        self.assertIn('removing live resources', self.client.get('/apps/web/staging', headers=WHO).text)
        self.assertIn('live cleanup in progress', self.client.get('/apps', headers=WHO).text)

    def test_post_fresh_pin_and_go_decision_prevent_propose_after_any_refusal(self):
        # GET healthy followed by fresh POST: neither the GET decision nor its main SHA is reused.
        self.assertEqual(self.client.get('/apps/web/staging', headers=WHO).status_code, 200)
        self.remote.head = NEW
        for reason in ('reviewed_digest_changed', 'instance_not_healthy', 'revision_not_current', 'reviewed_digest_not_running'):
            self.observed['promotion'] = {'ready': False, 'reason': reason}
            with mock.patch.object(self.jobs, 'submit') as propose:
                reply = self.client.post('/apps/web/staging/promote', data={'image': f'ghcr.io/x/web@{DIGEST}'}, headers=ORIGIN)
                self.assertEqual(reply.status_code, 400)
                self.assertIn(reason.replace('_', ' '), reply.text)
                propose.assert_not_called()
            args = self.captured[-1]
            self.assertNotIn('--all', args)
            self.assertEqual(args[args.index('--reviewed-digest') + 1], DIGEST)
            self.assertEqual(args[args.index('--revision') + 1], NEW)
        self.assertEqual(sum('/git/ref/' in r.url.path for r in self.remote.requests), 5)

    def test_review_get_uses_go_warning_and_disables_submit(self):
        self.remote.production = True
        self.observed['promotion'] = {'ready': False, 'reason': 'revision_not_current'}
        reply = self.client.get('/apps/web/staging/promotion', headers=WHO)
        self.assertEqual(reply.status_code, 200)
        self.assertIn('revision not current', reply.text)
        self.assertIn('<button class="primary" disabled>Promote to production</button>', reply.text)
        self.assertEqual(len(self.captured), 1)
        self.assertIn('--all', self.captured[0])

    def test_empty_submitted_review_is_evaluated_by_go_not_replaced_by_selected_digest(self):
        self.observed['promotion'] = {'ready': False, 'reason': 'reviewed_digest_invalid'}
        with mock.patch.object(self.jobs, 'submit') as propose:
            reply = self.client.post('/apps/web/staging/promote', data={}, headers=ORIGIN)
            self.assertEqual(reply.status_code, 400)
            propose.assert_not_called()
        args = self.captured[-1]
        self.assertEqual(args[args.index('--reviewed-digest') + 1], '')

    def test_post_failed_evidence_cannot_reach_proposal(self):
        self.remote.fail = '/tarball/'
        with mock.patch.object(self.jobs, 'submit') as propose:
            reply = self.client.post('/apps/web/staging/promote', data={'image': f'ghcr.io/x/web@{DIGEST}'}, headers=ORIGIN)
            self.assertNotEqual(reply.status_code, 303)
            propose.assert_not_called()

    def test_post_passes_only_on_go_ready_and_preserves_held_pr_flow(self):
        with mock.patch.object(self.jobs, 'submit', return_value=actions.Job(7, 'promote', 'web/staging', 'sam', self.jobs.now())) as propose:
            reply = self.client.post('/apps/web/staging/promote', data={'image': f'ghcr.io/x/web@{DIGEST}'}, headers=ORIGIN, follow_redirects=False)
            self.assertEqual(reply.status_code, 303)
            propose.assert_called_once()
        self.assertIn('--reviewed-digest', self.captured[-1])

    def test_no_production_module_calls_deleted_status_apis(self):
        root = Path(ops.__file__).parents[1]
        for path in root.rglob('*.py'):
            text = path.read_text()
            for deleted in ('gather_status', 'InstanceStatus', 'status_of(', 'render_applied=', 'settled_status'):
                self.assertNotIn(deleted, text, path)
        for deleted in ('verdict', 'runs', 'job_verdict'):
            self.assertFalse(hasattr(state, deleted))
