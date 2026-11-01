"""App-page Git evidence has one owner, one commit and one answer per file."""
import contextvars
import json
import threading
import unittest
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from unittest import mock

import httpx
from test_console import WHO, client, fake, release, unit

from swhurl.apps.instance import Instance
from swhurl.apps.state import State
from swhurl.console import actions, cluster, reading, server
from swhurl.console import github as gh
from swhurl.console.routes import instance
from swhurl.run import FakeRunner

HEAD = 'a' * 40
RELEASE = ('version: 1\nimage: {repository: ghcr.io/samclement/web}\nkind: web\nuid: 1000\n'
           'port: 8080\nhealthPath: /\nresources: {memory: 64Mi}\n')


def app_runner():
    answers = {f'kubectl -n {"flux-system" if kind in ("kustomization", "gitrepository") else "web-prod"} get {kind}': json.dumps(value)
               for kind, value in (('kustomization', unit('app-web-prod')),
                                   ('gitrepository', {'status': {'artifact': {'revision': f'main@sha1:{HEAD}'}}}),
                                   ('helmrelease', release('web-prod', 'web')),
                                   ('pods', {'items': []}), ('deploy,statefulset,daemonset,cronjob', {'items': []}),
                                   ('ingress', {'items': []}), ('certificate', {'items': []}))}
    return fake(**answers)


class FileTests(unittest.TestCase):
    def test_concurrent_duplicate_reads_share_values_absence_and_failures(self):
        for value in ('content', None, ValueError('unavailable')):
            with self.subTest(value=value):
                entered, release_read = threading.Event(), threading.Event()
                calls = []
                barrier = threading.Barrier(reading.FILE_WORKERS)

                def source(path, calls=calls, entered=entered, release_read=release_read, value=value):
                    calls.append(path)
                    entered.set()
                    self.assertTrue(release_read.wait(5))
                    if isinstance(value, Exception):
                        raise value
                    return value

                with reading.Files(FakeRunner(), None, source) as files:
                    def read(barrier=barrier):
                        barrier.wait(5)
                        try:
                            return files('same')
                        except ValueError as error:
                            return error
                    with ThreadPoolExecutor(max_workers=1) as pool:
                        work = pool.submit(files.together, *([read] * reading.FILE_WORKERS))
                        try:
                            self.assertTrue(entered.wait(5))
                        finally:
                            release_read.set()
                        self.assertEqual(work.result(5), [value] * reading.FILE_WORKERS)
                    self.assertEqual(calls, ['same'])
                    self.assertEqual(files.hits, reading.FILE_WORKERS - 1)

    def test_independent_reads_are_bounded_and_carry_request_context(self):
        barrier = threading.Barrier(reading.FILE_WORKERS)
        lock = threading.Lock()
        active = peak = 0
        context = contextvars.ContextVar('request', default='missing')
        context.set('this page')

        def source(path):
            nonlocal active, peak
            with lock:
                active += 1
                peak = max(peak, active)
            try:
                if int(path) < reading.FILE_WORKERS:
                    barrier.wait(5)  # all four have to be running, not merely submitted
                self.assertEqual(context.get(), 'this page')
                return path
            finally:
                with lock:
                    active -= 1

        with reading.Files(FakeRunner(), None, source) as files:
            paths = [str(i) for i in range(reading.FILE_WORKERS + 3)]
            self.assertEqual(files.together(*(lambda path=path: files(path) for path in paths)), paths)
        self.assertEqual(peak, reading.FILE_WORKERS)

    def test_nested_batches_share_the_same_four_file_slots(self):
        all_attempted, four_started, release_reads = threading.Event(), threading.Event(), threading.Event()
        lock = threading.Lock()
        attempted = active = peak = 0
        count = reading.FILE_WORKERS ** 2

        def source(path):
            nonlocal active, peak
            with lock:
                active += 1
                peak = max(peak, active)
                if active == reading.FILE_WORKERS:
                    four_started.set()
            try:
                self.assertTrue(release_reads.wait(5))
                return path
            finally:
                with lock:
                    active -= 1

        with reading.Files(FakeRunner(), None, source) as files:
            def read(path):
                nonlocal attempted
                with lock:
                    attempted += 1
                    if attempted == count:
                        all_attempted.set()
                return files(path)

            def batch(group):
                return files.together(*(lambda i=i: read(str(i)) for i in group))

            groups = [range(i, i + reading.FILE_WORKERS) for i in range(0, count, reading.FILE_WORKERS)]
            with ThreadPoolExecutor(max_workers=1) as pool:
                work = pool.submit(files.together, *(lambda group=group: batch(group) for group in groups))
                try:
                    self.assertTrue(all_attempted.wait(5))
                    self.assertTrue(four_started.wait(5))
                finally:
                    release_reads.set()
                self.assertEqual(work.result(5), [[str(i) for i in group] for group in groups])
        self.assertEqual(peak, reading.FILE_WORKERS)

    def test_special_characters_in_file_paths_cannot_hide_the_pinned_ref(self):
        asked = []
        path = 'apps/web/prod/offers/api?# space.yaml'

        def handle(request):
            if '/git/ref/' in request.url.path:
                return httpx.Response(200, json={'object': {'sha': HEAD}})
            asked.append(request.url)
            return httpx.Response(200, text='content')

        github = gh.GitHub('x/y', 'tok', transport=httpx.MockTransport(handle))
        with reading.Files(FakeRunner(), github) as files:
            self.assertEqual(files(path), 'content')
        self.assertEqual(asked[0].path, f'/repos/x/y/contents/{path}')
        self.assertEqual(dict(asked[0].params), {'ref': HEAD})
        self.assertEqual(asked[0].fragment, '')
        self.assertIn(b'%3F%23%20', asked[0].raw_path)

    def test_client_closes_even_when_rendering_raises_and_logs_no_contents(self):
        github = gh.GitHub('x/y', 'do-not-log-token', transport=httpx.MockTransport(
            lambda request: httpx.Response(200, json={'object': {'sha': HEAD}}) if '/git/ref/' in request.url.path
            else httpx.Response(200, text='do-not-log-content')))
        files = reading.Files(FakeRunner(), github)
        with self.assertLogs('swhurl.console.timing', 'INFO') as logs:
            with self.assertRaisesRegex(ValueError, 'render failed'), files:
                self.assertEqual(files('example'), 'do-not-log-content')
                raise ValueError('render failed')
        self.assertTrue(files.api.client.is_closed)
        self.assertIn('files=1 hits=0 failed=0 resolve_ms=', logs.output[0])
        self.assertIn('file_ms=', logs.output[0])
        self.assertNotIn(github.token, str(logs.output))
        self.assertNotIn('do-not-log-content', str(logs.output))

    def test_failed_or_invalid_head_never_reads_moving_main(self):
        for response in (httpx.Response(503), httpx.Response(200, json={'object': {'sha': ''}})):
            asked = []

            def handle(request, asked=asked, response=response):
                asked.append(request.url.path)
                return response

            github = gh.GitHub('x/y', 'tok', transport=httpx.MockTransport(handle))
            with reading.Files(FakeRunner(), github) as files:
                for path in ('one', 'two', 'one'):
                    with self.assertRaises(actions.ActionError):
                        files(path)
            self.assertEqual(asked, ['/repos/x/y/git/ref/heads/main'])
            self.assertTrue(files.api.client.is_closed)

    def test_two_concurrent_pages_do_not_share_answers_or_clients(self):
        barrier = threading.Barrier(2)
        asked = []
        lock = threading.Lock()

        def handle(request):
            if '/git/ref/' in request.url.path:
                barrier.wait(5)
                return httpx.Response(200, json={'object': {'sha': HEAD}})
            with lock:
                asked.append(request.url.path)
            return httpx.Response(200, text='content')

        github = gh.GitHub('x/y', 'tok', transport=httpx.MockTransport(handle))

        def page():
            with reading.Files(FakeRunner(), github) as files:
                self.assertEqual(files('same'), 'content')
                self.assertEqual(files('same'), 'content')
            return files.api.client

        with ThreadPoolExecutor(max_workers=2) as pool:
            clients = list(pool.map(lambda _: page(), range(2)))
        self.assertIsNot(clients[0], clients[1])
        self.assertTrue(all(c.is_closed for c in clients))
        self.assertEqual(len(asked), 2)


class AppFileTests(unittest.TestCase):
    def test_page_and_related_apps_fetch_each_file_once_at_one_commit(self):
        documents = {
            'apps/web/app.yaml': 'version: 1\nteam: demo\n',
            'apps/web/prod/instance.yaml': 'version: 1\ncalls: [other]\n',
            'apps/web/prod/release.yaml': RELEASE,
            'apps/other/app.yaml': 'version: 1\nteam: demo\n',
            'apps/other/prod/instance.yaml': 'version: 1\nallows: [web]\n',
            'apps/other/prod/release.yaml': RELEASE,
        }
        asked = []
        heads = []
        owned = []
        factory = instance.Files

        def owner(*args):
            files = factory(*args)
            owned.append(files)
            return files

        def handle(request):
            if '/git/ref/' in request.url.path:
                head = chr(ord('a') + len(heads)) * 40
                heads.append(head)
                return httpx.Response(200, json={'object': {'sha': head}})
            self.assertEqual(request.headers['Accept'], 'application/vnd.github.raw+json')
            path = request.url.path.split('/contents/', 1)[1]
            asked.append((path, request.url.params['ref']))
            return httpx.Response(200, text=documents[path]) if path in documents else httpx.Response(404)

        github = gh.GitHub('x/y', 'tok', transport=httpx.MockTransport(handle))
        c = client(app_runner(), github=github)
        with mock.patch.object(instance, 'Files', side_effect=owner):
            for _ in range(2):
                response = c.get('/apps/web/prod', headers=WHO)
                self.assertEqual(response.status_code, 200)
                self.assertIn('connected in Git, not yet observed live', response.text)
        self.assertEqual(len(heads), 2)
        # Every file it reads once: the instance's records, its target's, the v1 Secret, and the v2 probe that
        # says whether this instance is v2's at all (it is not, in this fixture).
        expected = set(documents) | {'apps/web/prod/secret.sops.yaml', 'v2-records/web/prod/instance.json',
                                     'v2-records/other/prod/instance.json'}
        for head in heads:
            self.assertEqual(Counter(p for p, ref in asked if ref == head), Counter(dict.fromkeys(expected, 1)))
        self.assertEqual(len(owned), 2)
        self.assertTrue(all(files.api.client.is_closed for files in owned))

    def test_failed_head_preserves_live_page_and_retries_on_next_request(self):
        asked = []

        def handle(request):
            asked.append(request.url.path)
            return httpx.Response(503)

        c = client(app_runner(), github=gh.GitHub('x/y', 'tok', transport=httpx.MockTransport(handle)))
        for _ in range(2):
            response = c.get('/apps/web/prod', headers=WHO)
            self.assertEqual(response.status_code, 200)
            self.assertIn('its records could not be read', response.text)
            self.assertNotIn("The image's minimum", response.text)
        self.assertEqual(asked, ['/repos/x/y/git/ref/heads/main'] * 2)

    def test_injected_reader_deduplicates_missing_invalid_and_failed_files(self):
        for settings, expected in ((None, 'v1 holds part of its records but not apps/web/prod/instance.yaml'),
                                   ('version: [', 'its records could not be read'),
                                   (OSError('unavailable'), 'its records could not be read')):
            asked = []

            def source(path, asked=asked, settings=settings):
                asked.append(path)
                if path.endswith('instance.yaml'):
                    if isinstance(settings, Exception):
                        raise settings
                    return settings
                if path.endswith('release.yaml'):
                    return RELEASE
                return None

            c = client(app_runner(), documents=source)
            response = c.get('/apps/web/prod', headers=WHO)
            self.assertEqual(response.status_code, 200)
            self.assertIn(expected, response.text)
            # What Git does say is shown even when one of the three records is not there; a reader that fails
            # says nothing, so nothing is claimed.
            self.assertNotIn('memory request', response.text)
            self.assertTrue(all(count == 1 for count in Counter(asked).values()), asked)

    def test_template_reads_use_the_same_scope_and_client_closes_on_template_failure(self):
        owned = []
        factory = instance.Files

        def owner(*args):
            files = factory(*args)
            owned.append(files)
            return files

        row = cluster.AppRow(Instance('other', 'prod'), State('healthy'), False, '',
                             connections=cluster.Connections(offers=HEAD))
        documents = {
            'apps/other/prod/offers/source.yaml': f'repository: samclement/other\ncommit: {HEAD}\napi: openapi.json\n',
            'apps/other/prod/offers/openapi.json': '{"openapi":"3.1.0","info":{"title":"Other","description":"Template description"},"paths":{}}',
        }
        asked = []

        def handle(request):
            if '/git/ref/' in request.url.path:
                return httpx.Response(200, json={'object': {'sha': HEAD}})
            path = request.url.path.split('/contents/', 1)[1]
            asked.append((path, request.url.params['ref']))
            return httpx.Response(200, text=documents[path]) if path in documents else httpx.Response(404)

        def render(request, name, context, **kwargs):
            # Jinja invokes this after the view has gathered its facts: it must still use the pinned reader.
            self.assertFalse(owned[-1].api.client.is_closed)
            self.assertEqual(context['offer_text'](row), 'Template description')
            raise ValueError('template failed')

        c = client(app_runner(), github=gh.GitHub('x/y', 'tok', transport=httpx.MockTransport(handle)))
        with mock.patch.object(instance, 'Files', side_effect=owner), \
                mock.patch.object(server.TEMPLATES, 'TemplateResponse', side_effect=render):
            with self.assertRaisesRegex(ValueError, 'template failed'):
                c.get('/apps/web/prod', headers=WHO)
        self.assertTrue(owned[0].api.client.is_closed)
        self.assertTrue(all((path, HEAD) in asked for path in documents))
