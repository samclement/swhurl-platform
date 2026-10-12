"""A job outlives a restart of the console: it is finished first, remembered, and a new app can be finished later."""
import datetime as dt
import json
import threading
import unittest

import httpx
from starlette.testclient import TestClient
from test_console_changes import DIGEST, WHO, FakeAppGitHub, FakeGitHub, app_under_test, repo_runner, tree_fake

from swhurl.console import actions, jobstore
from swhurl.run import FakeRunner, Result

NOW = dt.datetime(2026, 10, 12, 19, 10, tzinfo=dt.UTC)


class MemoryStore:
    """What the ConfigMap holds, shared by the 'pods' of a test; ``alive`` names the pods that still exist."""

    def __init__(self, owner='pod-a', shared=None, alive=None):
        self.owner = owner
        self.records = shared if shared is not None else {}
        self.pods = alive if alive is not None else {owner}

    def load(self):
        return sorted((job for job in map(jobstore.parse, self.records.values()) if job), key=lambda job: job.id)

    def save(self, job, forget=()):
        self.records[job.id] = jobstore.record(job, str)
        for old in forget:
            self.records.pop(old, None)

    def alive(self, owner):
        return owner in self.pods


def jobs_of(store, **kwargs):
    return actions.Jobs(FakeRunner(), audit=lambda line: None, inline=True, now=lambda: NOW, store=store, **kwargs)


class RememberedJobsTests(unittest.TestCase):
    def test_a_finished_job_is_shown_by_the_pod_that_replaces_its_own(self):
        first = MemoryStore('pod-a')
        ran = jobs_of(first).submit('scale', 'web/prod', 'sam@swhurl.com', lambda job: job.lines.append('Opened the pull request'))
        self.assertEqual((ran.id, ran.state, ran.owner), (1, 'succeeded', 'pod-a'))
        later = jobs_of(MemoryStore('pod-b', first.records, alive={'pod-b'}))
        found = later.get(1)
        self.assertEqual((found.state, found.lines, found.identity, found.live), ('succeeded', ['Opened the pull request'], 'sam@swhurl.com', False))
        self.assertEqual([job.id for job in later.recent()], [1])
        self.assertEqual(later.submit('scale', 'web/prod', 'sam@swhurl.com', lambda job: None).id, 2, 'numbers carry on')
        self.assertEqual([job.id for job in later.recent()], [2, 1])

    def test_a_job_whose_pod_is_gone_is_interrupted_and_one_still_being_finished_is_not(self):
        shared = {}
        running = actions.Job(4, 'new app and repository', 'notes/staging', 'sam@swhurl.com', NOW, ['Created the repository'],
                              owner='pod-a', resume={'kind': 'new-app', 'name': 'notes'})
        shared[4] = jobstore.record(running, str)
        audit = []
        draining = actions.Jobs(FakeRunner(), audit=audit.append, inline=True, now=lambda: NOW, clock=lambda: 0.0,
                                store=MemoryStore('pod-b', shared, alive={'pod-a', 'pod-b'}))
        self.assertEqual(draining.get(4).state, 'running', 'pod-a still exists: it is finishing its jobs')
        with self.assertRaisesRegex(actions.ActionError, 'a job for notes/staging is already running'):
            draining.submit('remove', 'notes/staging', 'sam@swhurl.com', lambda job: None)
        clock = [0.0]
        store = MemoryStore('pod-b', shared, alive={'pod-a', 'pod-b'})
        watching = actions.Jobs(FakeRunner(), audit=audit.append, inline=True, now=lambda: NOW, clock=lambda: clock[0], store=store)
        finished = actions.Job(4, 'new app and repository', 'notes/staging', 'sam@swhurl.com', NOW, ['Created the repository', 'done'],
                               'succeeded', NOW, 'https://github.com/x/pull/9', 'pod-a')
        shared[4] = jobstore.record(finished, str)
        self.assertEqual(watching.get(4).state, 'running', 'read again only every few seconds')
        clock[0] = actions.FOREIGN_TTL + 1
        self.assertEqual((watching.get(4).state, watching.get(4).link), ('succeeded', 'https://github.com/x/pull/9'))

        shared[4] = jobstore.record(running, str)
        gone = actions.Jobs(FakeRunner(), audit=audit.append, inline=True, now=lambda: NOW, store=MemoryStore('pod-c', shared, alive={'pod-c'}))
        found = gone.get(4)
        self.assertEqual((found.state, found.finished, found.resume), ('interrupted', NOW, {'kind': 'new-app', 'name': 'notes'}))
        self.assertIn('interrupted: the console restarted before this job finished', found.lines[-1])
        self.assertEqual(found.lines[0], 'Created the repository', 'what it had done is kept')
        self.assertEqual(jobstore.parse(shared[4]).state, 'interrupted', 'recorded, so the next pod says the same')
        self.assertIn('[ERROR] sam@swhurl.com new app and repository notes/staging: interrupted (job 4)', audit)
        self.assertEqual(gone.submit('remove', 'notes/staging', 'sam@swhurl.com', lambda job: None).id, 5, 'the target is free again')

    def test_a_stopping_console_takes_no_job_and_waits_for_the_running_ones(self):
        release, started = threading.Event(), threading.Event()
        jobs = actions.Jobs(FakeRunner(), audit=lambda line: None, sleep=lambda seconds: release.wait(0.01))

        def work(job):
            started.set()
            release.wait(5)
            job.lines.append('finished after the stop was asked for')
        job = jobs.submit('scale', 'web/prod', 'sam@swhurl.com', work)
        started.wait(5)
        waited = []
        waiter = threading.Thread(target=lambda: waited.append(jobs.drain(timeout=5)))
        waiter.start()
        while not jobs.draining:
            pass
        with self.assertRaisesRegex(actions.ActionError, 'the console is restarting and takes no new jobs'):
            jobs.submit('scale', 'api/prod', 'sam@swhurl.com', lambda job: None)
        self.assertEqual(job.state, 'running')
        release.set()
        waiter.join(5)
        self.assertEqual((waited, job.state, job.lines), ([True], 'succeeded', ['finished after the stop was asked for']))
        clock = iter([0.0, 0.0, 10.0])
        stuck = actions.Jobs(FakeRunner(), audit=lambda line: None, sleep=lambda seconds: None, clock=lambda: next(clock))
        stuck._jobs[1] = actions.Job(1, 'scale', 'web/prod', 'sam@swhurl.com', NOW)
        self.assertFalse(stuck.drain(timeout=5), 'it says so when a job outlasts the wait')

    def test_only_the_newest_jobs_are_remembered_and_never_a_running_one_is_forgotten(self):
        store = MemoryStore()
        jobs = jobs_of(store)
        for number in range(jobstore.KEEP + 3):
            jobs.submit('scale', f'app{number}/prod', 'sam@swhurl.com', lambda job: None)
        self.assertEqual(sorted(store.records), list(range(4, jobstore.KEEP + 4)))
        self.assertEqual(len(jobs_of(MemoryStore('pod-b', store.records)).recent()), jobstore.KEEP)

    def test_a_record_is_bounded_redacted_and_never_trusted(self):
        job = actions.Job(7, 'new app and repository', 'notes/staging', 'sam@swhurl.com', NOW,
                          [f'A file-{n} ' + 'x' * 300 for n in range(400)] + ['token hunter2', '[ERROR] the end'], 'failed', NOW)
        raw = jobstore.record(job, lambda text: text.replace('hunter2', '***'))
        self.assertLessEqual(len(raw.encode()), jobstore.RECORD_BYTES)
        found = jobstore.parse(raw)
        self.assertEqual((found.lines[0], found.lines[-2:]), ('… earlier output is not kept', ['token ***', '[ERROR] the end']))
        self.assertEqual((found.id, found.state, found.started, found.finished), (7, 'failed', NOW, NOW))
        for bad in ('', 'not json', '[]', '{"id": 1}', json.dumps({**json.loads(raw), 'state': 'paused'}),
                    json.dumps({**json.loads(raw), 'started': 'yesterday'})):
            self.assertIsNone(jobstore.parse(bad), bad)
        self.assertIsNone(jobstore.parse(json.dumps({**json.loads(raw), 'resume': 'text'})).resume)


class JobStoreTests(unittest.TestCase):
    def test_each_pod_writes_only_its_own_jobs_keys_and_failures_never_fail_a_job(self):
        runner = FakeRunner()
        runner.on('kubectl', '-n', 'console', 'get', 'configmap', 'console-jobs', stdout=json.dumps({'data': {
            'job-2': jobstore.record(actions.Job(2, 'scale', 'web/prod', 'sam@swhurl.com', NOW, state='succeeded', finished=NOW), str),
            'job-1': jobstore.record(actions.Job(1, 'scale', 'api/prod', 'sam@swhurl.com', NOW, state='failed', finished=NOW), str),
            'note': 'not a job'}}))
        runner.on('kubectl', '-n', 'console', 'get', 'pod', 'pod-a', stdout=json.dumps({'metadata': {'name': 'pod-a'}}))
        runner.on('kubectl', '-n', 'console', 'get', 'pod', 'pod-gone', stdout='')
        sent = []
        runner.on('kubectl', '-n', 'console', 'patch', 'configmap', 'console-jobs',
                  handler=lambda argv, text: sent.append(json.loads(text)['data']) or Result(argv, 0))
        store = jobstore.JobStore(runner, 'pod-a')
        self.assertEqual([(job.id, job.state) for job in store.load()], [(1, 'failed'), (2, 'succeeded')])
        self.assertEqual((store.alive('pod-a'), store.alive('pod-gone'), store.alive('')), (True, False, False))
        store.save(actions.Job(3, 'scale', 'web/prod', 'sam@swhurl.com', NOW, owner='pod-a'), forget=(1,))
        patch = next(call for call in runner.calls if 'patch' in call)
        self.assertEqual(patch[:6], ('kubectl', '-n', 'console', 'patch', 'configmap', 'console-jobs'))
        self.assertIn('--type=merge', patch)
        self.assertEqual((sorted(sent[0]), sent[0]['job-1'], jobstore.parse(sent[0]['job-3']).owner), (['job-1', 'job-3'], None, 'pod-a'),
                         'one patch: its own key written, the forgotten one removed, no other key named')
        broken = FakeRunner().on('kubectl', returncode=1, stderr='forbidden')
        store = jobstore.JobStore(broken, 'pod-a')
        with self.assertLogs('swhurl.console.jobs', 'WARNING'):
            self.assertEqual(store.load(), [])
        with self.assertLogs('swhurl.console.jobs', 'WARNING'):
            store.save(actions.Job(3, 'scale', 'web/prod', 'sam@swhurl.com', NOW))
        self.assertTrue(store.alive('pod-b'), 'unknown is not gone')


class ResumeTests(unittest.TestCase):
    RESUME = {'kind': 'new-app', 'name': 'notes', 'stack': 'typescript', 'description': 'Take notes', 'answers': {'kind': 'web'},
              'extra': ['--team=platform', '--exposure=authenticated-web'], 'touches_others': False}

    def console(self, state='interrupted', resume=None, existing=('hello-ts', 'notes'), link=''):
        shared = {4: jobstore.record(actions.Job(4, 'new app and repository', 'notes/staging', 'sam@swhurl.com', NOW,
                                                 ['Created https://github.com/samclement/notes'], state, NOW, link, 'pod-a',
                                                 self.RESUME if resume is None else resume), str)}
        self.runner = repo_runner(tree_fake())
        self.jobs = actions.Jobs(self.runner, audit=lambda line: None, inline=True, store=MemoryStore('pod-b', shared, alive={'pod-b'}))
        self.platform_api = FakeGitHub()
        self.app_api = FakeAppGitHub(existing=existing, runs=[{'status': 'completed', 'conclusion': 'success'}])
        return TestClient(app_under_test(self.runner, jobs=self.jobs, github=self.platform_api.github, app_repos=self.app_api.config))

    def test_an_interrupted_new_app_is_finished_with_its_choices_and_no_second_repository(self):
        c = self.console()
        page = c.get('/jobs/4', headers=WHO).text
        self.assertIn('Interrupted', page)
        self.assertIn('<form class="action-row" method="post" action="/jobs/4/resume"', page)
        self.assertNotIn('EventSource', page, 'nothing more will arrive')
        self.assertEqual(c.get('/jobs/4/events', headers=WHO).status_code, 404)
        self.assertIn('Interrupted', c.get('/activity', headers=WHO).text)
        response = c.post('/jobs/4/resume', headers=WHO, follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (303, '/jobs/5'))
        job = self.jobs.get(5)
        self.assertEqual((job.action, job.state, job.link, job.unit), ('finish new app', 'succeeded', 'https://github.com/x/pull/7', 'notes/staging'),
                         '\n'.join(job.lines))
        app_new = next(call for call in self.runner.calls if call[3:4] == ('app-new',))
        self.assertEqual(app_new[4:], ('notes', '--from-repo=samclement/notes', '--env=staging',
                                       f'--image=ghcr.io/samclement/notes:1-init000@{DIGEST}', '--team=platform', '--exposure=authenticated-web'))
        self.assertEqual(self.app_api.writes(), [], 'the repository is only read')
        self.assertFalse(any('copy' in call for call in self.runner.calls), 'nothing is rendered again')
        self.assertEqual(self.platform_api.sent('POST /issues/7/labels'), [{'labels': ['auto-merge']}])
        self.assertNotIn('action="/jobs/4/resume"', c.get('/jobs/4', headers=WHO).text, 'finished since: offered once')
        self.assertEqual(c.post('/jobs/4/resume', headers=WHO).status_code, 404)

    def test_a_failed_first_build_can_be_finished_too_but_nothing_else_can(self):
        c = self.console(state='failed')
        self.assertIn('action="/jobs/4/resume"', c.get('/jobs/4', headers=WHO).text)
        for state, resume, link in (('succeeded', None, 'https://github.com/x/pull/7'), ('running', None, ''),
                                    ('interrupted', {'kind': 'other'}, ''), ('interrupted', None, 'https://github.com/x/pull/7')):
            with self.subTest(state=state, resume=resume):
                c = self.console(state=state, resume=resume, link=link)
                if state == 'running':
                    self.jobs._before[4].state = 'running'  # as while pod-a still finishes it
                self.assertNotIn('action="/jobs/4/resume"', c.get('/jobs/4', headers=WHO).text)
                self.assertEqual(c.post('/jobs/4/resume', headers=WHO).status_code, 404)
        self.assertEqual(c.post('/jobs/99/resume', headers=WHO).status_code, 404)
        self.assertEqual(c.post('/jobs/4/resume', headers={'X-Auth-Request-Email': 'sam@swhurl.com'}).status_code, 403, 'a write: same-origin only')

    def test_finishing_says_so_when_the_repository_was_never_created_and_refuses_a_record_it_cannot_use(self):
        c = self.console()
        real = self.app_api.handle
        self.app_api.config.transport.handler = lambda request: (
            httpx.Response(404, json={'message': 'Not Found'}) if request.url.path.endswith('/git/ref/heads/main') else real(request))
        c.post('/jobs/4/resume', headers=WHO, follow_redirects=False)
        job = self.jobs.get(5)
        self.assertEqual(job.state, 'failed')
        self.assertIn('samclement/notes does not exist on GitHub: the earlier job stopped before creating it', '\n'.join(job.lines))
        self.assertEqual(self.platform_api.sent('POST /pulls'), [])
        for bad in ({**self.RESUME, 'name': '../x'}, {**self.RESUME, 'stack': 'cobol'}, {**self.RESUME, 'extra': ['rm', '-rf']},
                    {'kind': 'new-app'}):
            with self.subTest(bad=bad):
                c = self.console(resume=bad)
                self.assertEqual(c.post('/jobs/4/resume', headers=WHO).status_code, 409)
                self.assertIsNone(self.jobs.get(5), 'no job was started')


if __name__ == '__main__':
    unittest.main()
