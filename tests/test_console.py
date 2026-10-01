"""The read-only console: identity, pages and what it reads, offline, with FakeRunner."""
import io
import json
import unittest
from contextlib import redirect_stderr

from starlette.testclient import TestClient

from swhurl.console import actions, changes, cluster, server
from swhurl.run import FakeRunner, Result

REV = 'main@sha1:abc1234def'
WHO = {'X-Auth-Request-Email': 'sam@swhurl.com'}


def unit(name, depends=(), ready='True', message='Applied', suspend=False):
    return {'metadata': {'name': name},
            'spec': {'dependsOn': [{'name': d} for d in depends], **({'suspend': True} if suspend else {})},
            'status': {'lastAppliedRevision': REV, 'conditions': [{'type': 'Ready', 'status': ready, 'message': message}]}}


def release(ns, name, tag='1.0'):
    return {'metadata': {'namespace': ns, 'name': name}, 'spec': {'values': {'controllers': {'main': {
        'containers': {'main': {'image': {'repository': 'docker.io/x/web', 'tag': tag}}}}}}}}


UNITS = [unit('cluster-sources'), unit('infra-base'), unit('platform-oauth2-proxy', ['infra-base']),
         unit('app-web-prod', ['infra-base', 'platform-oauth2-proxy']),
         unit('app-my-api-staging', ['infra-base'], ready='False', message='<script>x</script>', suspend=True)]


RELEASES = [release('web-prod', 'web')]


TRAEFIK_REDIRECTS = {'spec': {'template': {'spec': {'containers': [
    {'args': ['--entryPoints.web.http.redirections.entryPoint.scheme=https']}]}}}}
CHECKS = {'kubectl get --raw=/version': '{}',
          'kubectl -n kube-system get deploy traefik': json.dumps(TRAEFIK_REDIRECTS),
          'kubectl -n flux-system get imageupdateautomations.image.toolkit.fluxcd.io apps-staging -o json':
              json.dumps({'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}),
          'kubectl -n flux-system get imagepolicies.image.toolkit.fluxcd.io': json.dumps({'items': []}),
          'kubectl -n flux-system get alerts.notification.toolkit.fluxcd.io': json.dumps({'items': [
              {'metadata': {'name': n}, 'spec': {'providerRef': {'name': 'p'}}} for n in ('failures', 'staging-deploys')]}),
          'kubectl -n flux-system get providers.notification.toolkit.fluxcd.io': json.dumps({'items': [{'metadata': {'name': 'p'}}]})}
"""Answers for the cluster-only platform checks (Overview and Platform run them)."""


def fake(units=UNITS, releases=RELEASES, **extra):
    runner = FakeRunner()
    runner.on(*cluster.UNITS[:5], stdout=json.dumps({'items': units}))
    runner.on(*cluster.RELEASES[:3], stdout=json.dumps({'items': releases}))
    for prefix, stdout in extra.items():
        runner.on(*prefix.split(), stdout=stdout)
    return runner


def client(runner, **kwargs):
    return TestClient(server.create_app(runner, **kwargs))


class IdentityTests(unittest.TestCase):
    def test_pages_need_the_oauth2_proxy_identity(self):
        runner = fake()
        for path in ('/', '/apps', '/units', '/units/infra-base', '/platform', '/activity', '/apps/web/prod'):
            with self.subTest(path):
                response = client(runner).get(path)
                self.assertEqual(response.status_code, 401)
        self.assertEqual(runner.calls, [], 'an unauthenticated request must not reach the cluster')

    def test_blank_identity_is_refused(self):
        self.assertEqual(client(fake()).get('/', headers={'X-Auth-Request-Email': '  '}).status_code, 401)

    def test_healthz_needs_nothing_and_reads_nothing(self):
        runner = fake()
        response = client(runner).get('/healthz')
        self.assertEqual((response.status_code, response.text), (200, 'ok\n'))
        self.assertEqual(runner.calls, [])

    def test_identity_is_shown(self):
        self.assertIn('sam@swhurl.com', client(fake()).get('/apps', headers=WHO).text)
        self.assertIn(server.DEV_IDENTITY, client(fake(), dev_identity=server.DEV_IDENTITY).get('/apps').text)

    def test_dev_identity_only_binds_to_loopback(self):
        with redirect_stderr(io.StringIO()) as err:
            self.assertEqual(server.main(['--dev', '--host', '192.168.1.10']), 2)
        self.assertIn('only binds to loopback', err.getvalue())


class AppsTests(unittest.TestCase):
    def test_lists_app_units_only_with_state_and_image(self):
        text = client(fake()).get('/apps', headers=WHO).text
        self.assertIn('href="/apps/web/prod"', text)
        self.assertIn('docker.io/x/web:1.0', text)
        self.assertIn('href="/apps/my-api/staging"', text)
        self.assertIn('Suspended', text)
        self.assertIn('no HelmRelease', text)
        self.assertNotIn('infra-base</a>', text)

    def test_cluster_values_are_escaped(self):
        units = [unit('app-x-prod', ready='False', message='<script>x</script>')]
        text = client(fake(units=units)).get('/apps', headers=WHO).text
        self.assertNotIn('<script>x</script>', text)
        self.assertIn('&lt;script&gt;', text)

    def test_unit_names_map_to_instances(self):
        self.assertEqual(cluster.instance_of_unit('app-my-api-staging'), cluster.ops.Instance('my-api', 'staging'))
        for name in ('infra-base', 'app-web-dev', 'app-prod', 'app-Web-prod'):
            self.assertIsNone(cluster.instance_of_unit(name), name)

    def test_unreadable_cluster_is_a_502_page_not_a_crash(self):
        runner = FakeRunner().on('kubectl', returncode=1, stderr='connection refused')
        response = client(runner).get('/apps', headers=WHO)
        self.assertEqual(response.status_code, 502)
        self.assertIn('connection refused', response.text)


class PolishTests(unittest.TestCase):
    def test_long_hashes_are_shortened_with_the_full_value_on_hover_and_still_escaped(self):
        sha = '1edf37052ebd0a94814cac0407f3380aa6a9c614'
        self.assertEqual(str(server.short_hashes(f'Applied revision: main@sha1:{sha}')),
                         f'Applied revision: main@sha1:<span class="hash" title="{sha}">1edf37052ebd…</span>')
        self.assertEqual(str(server.short_hashes('<script>1edf370</script>')), '&lt;script&gt;1edf370&lt;/script&gt;')

    def test_header_breadcrumbs_and_updated_time(self):
        c, jobs, _ = operate(fake())
        jobs._jobs[3] = actions.Job(3, 'reconcile', 'app-web-prod', 'sam@swhurl.com', jobs.now())
        job = c.get('/jobs/3', headers=WHO).text
        self.assertIn('<a href="/activity">Activity</a> › Job #3', job)
        self.assertIn('↻ Refresh</a>', job)
        self.assertRegex(job, r'Updated \d\d:\d\d ·')


class StateTests(unittest.TestCase):
    def test_one_vocabulary_and_waiting_is_updating_not_failing(self):
        state = cluster.state_of
        self.assertEqual(state(('True', 'Applied')), cluster.State('healthy'))
        self.assertEqual(state(('False', "dependency 'flux-system/platform-oauth2-proxy' is not ready"), reason='DependencyNotReady'),
                         cluster.State('updating', 'waiting for platform-oauth2-proxy'))
        self.assertEqual(state(('False', 'Reconciliation in progress'), reason='Progressing').key, 'updating')
        self.assertEqual(state(('Unknown', 'Running health checks')).key, 'updating')
        self.assertEqual(state(('False', 'health check failed'), reason='HealthCheckFailed'), cluster.State('failing', 'health check failed'))
        self.assertEqual(state(('True', ''), suspended=True).key, 'suspended')

    def test_a_dependency_wait_is_not_a_problem(self):
        waiting = unit('app-web-prod', ready='False', message="dependency 'flux-system/infra-base' is not ready")
        text = client(fake(units=[unit('infra-base'), waiting], **CHECKS)).get('/', headers=WHO).text
        self.assertIn('<strong>Updating</strong>', text)
        self.assertIn('<a href="/apps/web/prod">web/prod: waiting for infra-base</a>', text)
        self.assertNotIn('Needs attention', text)


class AppSummaryTests(unittest.TestCase):
    def row(self, env, tag, digest='', app='hello-ts'):
        return cluster.AppRow(cluster.ops.Instance(app, env), ('True', ''), False, f'ghcr.io/x/{app}:{tag}', tag, digest)

    def test_compares_staging_with_prod(self):
        cases = {('5-aaaaaaa', 'sha256:1', '4-bbbbbbb', 'sha256:2'): 'ahead',
                 ('4-bbbbbbb', 'sha256:2', '5-aaaaaaa', 'sha256:1'): 'differs',
                 ('1.27', 'sha256:1', '1.27', 'sha256:1'): 'same',
                 ('1.28', 'sha256:3', '1.27', 'sha256:1'): 'differs'}
        for (st, sd, pt, pd), expected in cases.items():
            with self.subTest(staging=st, prod=pt):
                (summary,) = cluster.summaries([self.row('staging', st, sd), self.row('prod', pt, pd)])
                self.assertEqual(summary.comparison, expected)
        (only,) = cluster.summaries([self.row('staging', '1')])
        self.assertEqual(only.comparison, '')

    def test_apps_list_offers_promote_when_staging_differs(self):
        units = [unit('app-web-staging'), unit('app-web-prod')]
        releases = [release('web-staging', 'web', tag='2.0'), release('web-prod', 'web', tag='1.0')]
        text = client(fake(units=units, releases=releases), github=changes.GitHub('x/y', 'token')).get('/apps', headers=WHO).text
        self.assertIn('Staging and prod differ', text)
        self.assertIn('action="/apps/web/staging/promote"><button  onclick', text, 'enabled with a token')

    def test_every_page_names_app_instances_the_same_way(self):
        self.assertEqual(cluster.target('app-hello-staging'), ('hello/staging', '/apps/hello/staging'))
        self.assertEqual(cluster.target('hello/staging'), ('hello/staging', '/apps/hello/staging'))
        self.assertEqual(cluster.target('infra-base'), ('infra-base', '/units/infra-base'))


class AppDetailTests(unittest.TestCase):
    def test_shows_gathered_status(self):
        def get(kind, value):
            return (f'kubectl -n {"flux-system" if kind in ("kustomization", "gitrepository") else "web-prod"} get {kind}',
                    json.dumps(value))
        answers = dict([
            get('kustomization', unit('app-web-prod')),
            get('gitrepository', {'status': {'artifact': {'revision': 'main@sha1:fff0000'}}}),
            get('helmrelease', {**release('web-prod', 'web'), **unit('web')}),
            get('pods', {'items': [{'metadata': {'name': 'web-1'}, 'status': {'containerStatuses': [
                {'ready': False, 'state': {'waiting': {'reason': 'CrashLoopBackOff'}}}]}}]}),
            get('deploy,statefulset,daemonset', {'items': []}),
            get('ingress', {'items': [{'spec': {'rules': [{'host': 'web.homelab.swhurl.com'}]}}]}),
            get('certificate', {'items': []}),
        ])
        text = client(fake(**answers)).get('/apps/web/prod', headers=WHO).text
        self.assertIn('not yet applied', text)
        self.assertIn('id="replicas" name="replicas" value="" placeholder="1"', text)
        self.assertEqual(text.count('<form class="wizard"'), 2, 'the app page uses the New app form layout')
        self.assertIn('action="/apps/web/prod/remove"', text)
        self.assertIn('<section class="danger">', text)
        self.assertIn('<strong>Failing</strong>', text, 'a failing pod makes the verdict')
        self.assertNotIn('/promote', text, 'promote is offered only on staging')
        self.assertIn('https://web.homelab.swhurl.com', text)
        self.assertIn('CrashLoopBackOff', text)
        self.assertIn('public (no sign-in)', text, 'a route without the sign-in middleware reads as public')
        self.assertIn('action="/apps/web/prod/expose"', text)
        self.assertIn('name="exposure" value="public" checked>', text)
        self.assertIn('<strong>Public</strong> <span class="muted">(now)</span>', text)

    def test_unknown_or_invalid_instance_is_404_without_odd_kubectl_calls(self):
        runner = fake().on('kubectl', '-n', 'flux-system', 'get', 'kustomization', 'app-nope-prod',
                           returncode=1, stderr='NotFound')
        self.assertEqual(client(runner).get('/apps/nope/prod', headers=WHO).status_code, 404)
        before = len(runner.calls)
        for path in ('/apps/web/dev', '/apps/Web/prod', '/apps/--all/prod'):
            self.assertEqual(client(runner).get(path, headers=WHO).status_code, 404, path)
        self.assertEqual(len(runner.calls), before, 'invalid names must not reach kubectl')


class UnitsTests(unittest.TestCase):
    def test_columns_follow_depends_on(self):
        levels = {u.name: u.level for u in cluster.units(fake())}
        self.assertEqual(levels, {'cluster-sources': 0, 'infra-base': 0, 'platform-oauth2-proxy': 1,
                                  'app-web-prod': 2, 'app-my-api-staging': 1})

    def test_missing_dependency_and_cycles_do_not_break_levels(self):
        units = [unit('a', ['b']), unit('b', ['a']), unit('c', ['gone'])]
        self.assertEqual({u.name: u.level for u in cluster.units(fake(units=units))}, {'a': 1, 'b': 0, 'c': 0})

    def test_platform_groups_units_by_layer_without_buttons(self):
        text = client(fake(**CHECKS)).get('/platform', headers=WHO).text
        for layer in ('Cluster', 'Infrastructure', 'Platform services', 'Apps'):
            self.assertIn(f'<h3>{layer} ', text)
        self.assertIn('href="/units/infra-base"', text)
        self.assertIn('abc1234', text)
        self.assertIn('infra-base, platform-oauth2-proxy', text, 'what each unit waits for')
        self.assertIn('<details class="section" open>', text, 'a unit not Ready opens the units list')
        self.assertNotIn('<form', text, 'actions live on each unit\'s page')
        self.assertNotIn('<strong>Flux Kustomizations</strong>', text, 'the units list replaces that check group')
        self.assertIn('my-api/staging is suspended', text)

    def test_unit_page_offers_its_actions_and_explains_them(self):
        c = client(fake())
        text = c.get('/units/infra-base', headers=WHO).text
        self.assertIn('action="/units/infra-base/reconcile"', text)
        self.assertIn('action="/units/infra-base/suspend"', text)
        self.assertIn('href="/units/platform-oauth2-proxy"', text, 'needed by')
        resume = c.get('/units/app-my-api-staging', headers=WHO).text
        self.assertIn('action="/units/app-my-api-staging/resume"', resume)
        self.assertIn('href="/apps/my-api/staging"', resume)
        root = c.get('/units/cluster-sources', headers=WHO).text
        self.assertNotIn('<form', root)
        self.assertIn('make flux-bootstrap', root)
        self.assertEqual(c.get('/units/nope', headers=WHO).status_code, 404)

    def test_old_addresses_redirect(self):
        c = client(fake())
        for old, new in (('/units', '/platform#units'), ('/jobs', '/activity')):
            response = c.get(old, headers=WHO, follow_redirects=False)
            self.assertEqual((response.status_code, response.headers['location']), (301, new))


class OverviewTests(unittest.TestCase):
    def test_lists_what_needs_attention_with_links(self):
        text = client(fake(**CHECKS)).get('/', headers=WHO).text
        self.assertIn('things need attention', text)
        self.assertIn('<a href="/apps/my-api/staging">my-api/staging is not Ready', text)
        self.assertIn('my-api/staging is suspended', text)
        self.assertNotIn('app-my-api-staging', text, 'Overview names app instances <app>/<env>')
        self.assertIn('2 app instances', text)
        self.assertIn('more checks need your terminal', text)

    def test_healthy_cluster_says_so(self):
        text = client(fake(units=[unit('infra-base')], **CHECKS)).get('/', headers=WHO).text
        self.assertIn('Everything is healthy', text)
        self.assertNotIn('Needs attention', text)


class PlatformTests(unittest.TestCase):
    def test_runs_only_cluster_checks_and_says_what_it_skipped(self):
        traefik = {'spec': {'template': {'spec': {'containers': [{'args': []}]}}}}
        runner = fake(**{'kubectl get --raw=/version': '{}',
                         'kubectl -n kube-system get deploy traefik': json.dumps(traefik),
                         'kubectl -n flux-system get imageupdateautomations.image.toolkit.fluxcd.io apps-staging -o json':
                             json.dumps({'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}),
                         'kubectl -n flux-system get imagepolicies.image.toolkit.fluxcd.io': json.dumps({'items': []}),
                         'kubectl -n flux-system get alerts.notification.toolkit.fluxcd.io': json.dumps({'items': []}),
                         'kubectl -n flux-system get providers.notification.toolkit.fluxcd.io': json.dumps({'items': []})})
        response = client(runner).get('/platform', headers=WHO)
        self.assertEqual(response.status_code, 200)
        self.assertIn('<strong>Ingress</strong> · <span class="bad">1 of 1 not passing</span>', response.text)
        self.assertIn('Traefik does not redirect HTTP to HTTPS', response.text)
        self.assertIn('8 more checks read Secrets, run commands in pods or need your machine', response.text)
        self.assertFalse([c for c in runner.calls if 'secret' in c or 'exec' in c], runner.calls)

    def test_unreachable_cluster_is_a_502(self):
        runner = FakeRunner().on('kubectl', 'get', '--raw=/version', returncode=1, stderr='refused')
        with redirect_stderr(io.StringIO()):
            self.assertEqual(client(runner).get('/platform', headers=WHO).status_code, 502)


ORIGIN = {**WHO, 'Origin': 'http://testserver'}


class FluxAPI:
    """Answers the console's kubectl patches, annotations and gets like Flux's controllers.

    An annotated object reports the request handled after ``polls`` more gets,
    with its Ready condition set to ``ready``."""

    def __init__(self, runner, ready='True', message='', polls=0, suspended=False):
        self.ready, self.message, self.polls, self.suspended = ready, message, polls, suspended
        self.tokens, self.gets = {}, {}
        runner.on('kubectl', '-n', 'flux-system', 'annotate', handler=self.annotate)
        runner.on('kubectl', '-n', 'flux-system', 'patch', handler=lambda args, _: Result(args, 0, 'patched\n'))
        runner.on('kubectl', '-n', 'flux-system', 'get', handler=self.get)

    def annotate(self, args, _input):
        self.tokens[args[5]] = args[6].split('=', 1)[1]
        return Result(args, 0)

    def get(self, args, _input):
        target = args[4]
        if target.startswith(actions.KUSTOMIZATION) and target not in self.tokens:
            return Result(args, 0, json.dumps({'spec': {'suspend': self.suspended, 'sourceRef': {
                'kind': 'GitRepository', 'name': 'swhurl-platform'}}}))
        self.gets[target] = self.gets.get(target, 0) + 1
        handled = self.gets[target] > self.polls
        status = {'observedGeneration': 3, 'lastAppliedRevision': REV, 'artifact': {'revision': REV},
                  'conditions': [{'type': 'Ready', 'status': self.ready if handled else 'Unknown', 'message': self.message}]}
        if handled:
            status['lastHandledReconcileAt'] = self.tokens[target]
        return Result(args, 0, json.dumps({'metadata': {'generation': 3}, 'status': status}))


def operate(runner, **flux):
    """A client whose jobs run inline against a FluxAPI, with audit lines and sleeps collected."""
    audit, sleeps = [], []
    api = FluxAPI(runner, **flux)
    jobs = actions.Jobs(runner, audit=audit.append, inline=True, sleep=sleeps.append)
    jobs.api, jobs.sleeps = api, sleeps
    return client(runner, jobs=jobs), jobs, audit


def mutations(runner):
    return [call[3:] for call in runner.calls if call[:1] == ('kubectl',) and call[3] in ('annotate', 'patch')]


class ActionTests(unittest.TestCase):
    def test_reconcile_requests_source_then_unit_records_output_and_audits(self):
        runner = fake()
        c, jobs, audit = operate(runner)
        response = c.post('/units/app-web-prod/reconcile', headers=ORIGIN, follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (303, '/jobs/1'))
        source, unit = 'gitrepositories.source.toolkit.fluxcd.io/swhurl-platform', f'{actions.KUSTOMIZATION}/app-web-prod'
        self.assertEqual([m[:3] for m in mutations(runner)], [('annotate', '--overwrite', source),
                                                             ('annotate', '--overwrite', unit)])
        self.assertTrue(mutations(runner)[0][3].startswith('reconcile.fluxcd.io/requestedAt='))
        self.assertEqual(jobs.get(1).state, 'succeeded', jobs.get(1).lines)
        self.assertEqual(jobs.get(1).lines[-1], f'✔ applied revision {REV}')
        self.assertIn(f'✔ fetched revision {REV}', jobs.get(1).lines)
        self.assertEqual(audit, ['[AUDIT] sam@swhurl.com reconcile app-web-prod: started (job 1)',
                                 '[AUDIT] sam@swhurl.com reconcile app-web-prod: succeeded (job 1)'])
        page = c.get('/jobs/1', headers=WHO).text
        self.assertIn('✔ applied revision', page)
        self.assertNotIn('http-equiv="refresh"', page)
        self.assertIn('reconcile <a href="/apps/web/prod">web/prod</a>', c.get('/activity', headers=WHO).text,
                      'an app instance is <app>/<env> on every page, not its unit name')
        self.assertFalse([call for call in runner.calls if call[0] == 'flux'])

    def test_suspend_patches_only_and_resume_patches_then_waits(self):
        runner = fake()
        c, jobs, _ = operate(runner, polls=2)
        c.post('/units/infra-base/suspend', headers=ORIGIN)
        self.assertEqual(mutations(runner), [('patch', f'{actions.KUSTOMIZATION}/infra-base', '--type=merge',
                                              '--patch', '{"spec": {"suspend": true}}')])
        self.assertEqual((jobs.get(1).state, jobs.sleeps), ('succeeded', []))
        self.assertEqual(jobs.get(1).lines[0], '► suspending Kustomization infra-base')
        c.post('/units/infra-base/resume', headers=ORIGIN)
        self.assertEqual(mutations(runner)[1][-1], '{"spec": {"suspend": false}}')
        self.assertEqual(mutations(runner)[2][:3], ('annotate', '--overwrite', f'{actions.KUSTOMIZATION}/infra-base'))
        self.assertEqual(jobs.get(2).state, 'succeeded', jobs.get(2).lines)
        self.assertEqual(jobs.get(2).lines[0], '► resuming Kustomization infra-base')
        self.assertEqual(jobs.sleeps, [actions.POLL, actions.POLL], 'polled until the request was handled')

    def test_failure_is_shown_and_audited_as_error(self):
        c, jobs, audit = operate(fake(), ready='False', message='health check failed after 5m')
        c.post('/units/infra-base/resume', headers=ORIGIN)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertEqual(jobs.get(1).lines[-1],
                         '[ERROR] Kustomization infra-base reconciliation failed: health check failed after 5m')
        self.assertTrue(audit[-1].startswith('[ERROR] sam@swhurl.com resume infra-base: failed'))

    def test_timeout_fails_the_job(self):
        runner = fake()
        FluxAPI(runner, polls=10**6)
        ticks = iter(range(0, 10**4, 60))
        jobs = actions.Jobs(runner, audit=lambda _: None, inline=True, sleep=lambda _: None, clock=lambda: next(ticks))
        jobs.start('resume', 'infra-base', 'sam@swhurl.com')
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('timed out after 10m waiting for Kustomization infra-base', jobs.get(1).lines[-1])

    def test_reconciling_a_suspended_unit_is_refused(self):
        runner = fake()
        c, jobs, _ = operate(runner, suspended=True)
        c.post('/units/app-my-api-staging/reconcile', headers=ORIGIN)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('is suspended; resume it instead', jobs.get(1).lines[-1])
        self.assertEqual(mutations(runner), [])

    def test_refused_unknown_or_busy_actions_run_nothing(self):
        runner = fake()
        c, jobs, audit = operate(runner)
        cases = {'/units/cluster-stack/suspend': 'make flux-bootstrap', '/units/cluster-sources/reconcile': 'make flux-bootstrap',
                 '/units/nope/reconcile': 'no Flux unit', '/units/infra-base/delete': 'unknown action'}
        for path, message in cases.items():
            with self.subTest(path=path):
                response = c.post(path, headers=ORIGIN)
                self.assertEqual(response.status_code, 409)
                self.assertIn(message, response.text)
        busy = actions.Job(99, 'reconcile', 'infra-base', 'x', jobs.now())
        jobs._jobs[99] = busy
        self.assertIn('already running', c.post('/units/infra-base/suspend', headers=ORIGIN).text)
        self.assertEqual(mutations(runner), [])
        self.assertEqual(audit, [])

    def test_posts_must_come_from_a_console_page(self):
        runner = fake()
        c, _, _ = operate(runner)
        for headers in (WHO, {**WHO, 'Origin': 'https://evil.example'}, {**WHO, 'Origin': 'null'}):
            with self.subTest(origin=headers.get('Origin')):
                self.assertEqual(c.post('/units/infra-base/reconcile', headers=headers).status_code, 403)
        self.assertEqual(c.post('/units/infra-base/reconcile', headers={'Origin': 'http://testserver'}).status_code, 401)
        self.assertEqual(runner.calls, [])

    def test_running_job_page_refreshes(self):
        c, jobs, _ = operate(fake())
        jobs._jobs[7] = actions.Job(7, 'resume', 'infra-base', 'sam@swhurl.com', jobs.now())
        self.assertIn('http-equiv="refresh"', c.get('/jobs/7', headers=WHO).text)
        self.assertEqual(c.get('/jobs/8', headers=WHO).status_code, 404)

    def test_running_job_shows_in_the_header_and_activity(self):
        c, jobs, _ = operate(fake())
        jobs._jobs[7] = actions.Job(7, 'resume', 'infra-base', 'sam@swhurl.com', jobs.now())
        self.assertIn('1 running</span>', c.get('/apps', headers=WHO).text)
        activity = c.get('/activity', headers=WHO).text
        self.assertIn('href="/jobs/7"', activity)
        self.assertIn('href="/units/infra-base">Back to infra-base', c.get('/jobs/7', headers=WHO).text)

    def test_dry_run_plans_the_patches_and_does_not_wait(self):
        runner = fake()
        runner.dry_run = True
        c, jobs, _ = operate(runner)
        c.post('/units/infra-base/suspend', headers=ORIGIN)
        c.post('/units/infra-base/resume', headers=ORIGIN)
        self.assertEqual([p[3] for p in runner.planned], ['patch', 'patch', 'annotate'])
        self.assertEqual(mutations(runner), [])
        self.assertEqual((jobs.get(2).state, jobs.sleeps), ('succeeded', []))

if __name__ == '__main__':
    unittest.main()
