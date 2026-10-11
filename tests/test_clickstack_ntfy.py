"""The ClickStack ntfy command keeps one destination without exposing the topic."""
import io
import json
import re
import unittest

from test_clickstack import EMAIL, URI, b64

from swhurl import clickstack, clickstack_ntfy
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

TOPIC = 'swhurl-test-private-topic-012345'
ACCESS_KEY = 'private-admin-access-key'


class Fixture:
    def __init__(self, live=(), *, topic=TOPIC, dry_run=False):
        self.live, self.calls = list(live), []
        self.runner = (FakeRunner(dry_run=dry_run)
                       .on(*clickstack_ntfy.NTFY_SECRET, stdout=json.dumps({'data': {
                           'NTFY_PLATFORM_URL': b64(f'https://ntfy.sh/{topic}')}}))
                       .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.INPUTS_SECRET,
                           stdout=json.dumps({'data': {'CLICKSTACK_ADMIN_EMAIL': b64(EMAIL)}}))
                       .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.MONGO_URI_SECRET,
                           stdout=json.dumps({'data': {'connectionString.standard': b64(URI)}}))
                       .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.MONGO_POD,
                           stdout='RESULT ' + json.dumps({'key': ACCESS_KEY}))
                       .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.APP, handler=self.api))

    def api(self, argv, program):
        url, request = re.search(r'fetch\(("[^"]*"), (\{.*\})\);', program).groups()
        path, request = json.loads(url).removeprefix('http://127.0.0.1:8000/api/v2'), json.loads(request)
        assert request['headers']['authorization'] == f'Bearer {ACCESS_KEY}'
        body = json.loads(request['body']) if 'body' in request else None
        self.calls.append((request['method'], path, body))
        answer = {'data': self.live, 'meta': {'total': len(self.live)}} if request['method'] == 'GET' else {'data': {}}
        return Result(argv, 0, 'RESULT ' + json.dumps({'status': 200, 'body': answer}), '')

    def run(self):
        output = io.StringIO()
        code = clickstack_ntfy.sync(self.runner, Report(output, redact=self.runner.redact))
        return code, output.getvalue()


class WebhookTests(unittest.TestCase):
    def test_creates_once_and_keeps_existing_destination(self):
        new = Fixture()
        code, output = new.run()
        self.assertEqual(code, 0, output)
        self.assertEqual([(m, p) for m, p, _ in new.calls], [('GET', '/webhooks'), ('POST', '/webhooks')])
        body = new.calls[-1][2]
        self.assertEqual((body['service'], body['url']), ('generic', 'https://ntfy.sh/'))
        self.assertNotIn('{{/if}}}', body['body'])  # Handlebars treats this as a triple-brace close.
        rendered = (body['body'].replace('{{title}}', 'CPU \\"high\\"')
                    .replace('{{body}}', 'More than 90%')
                    .replace('{{link}}', 'https://clickstack.example/alerts')
                    .replace('{{#if (eq state "ALERT")}}4{{else}}3{{/if}}', '4'))
        self.assertEqual(json.loads(rendered)['topic'], TOPIC)
        self.assertNotIn(TOPIC, output)
        self.assertNotIn(ACCESS_KEY, output)

        existing = Fixture([{**body, 'id': 'webhook-1'}])
        code, output = existing.run()
        self.assertEqual((code, [(m, p) for m, p, _ in existing.calls]), (0, [('GET', '/webhooks')]), output)
        self.assertIn('up to date', output)

    def test_updates_only_the_owned_webhook_and_dry_run_writes_nothing(self):
        wanted = clickstack_ntfy.destination(Fixture().runner)
        live = [{**wanted, 'id': 'owned', 'body': 'stale'},
                {'id': 'other', 'name': 'manual', 'service': 'generic', 'body': 'keep'}]
        preview = Fixture(live, dry_run=True)
        code, output = preview.run()
        self.assertEqual((code, [m for m, _, _ in preview.calls]), (0, ['GET']), output)
        self.assertIn('would update', output)

        update = Fixture(live)
        code, output = update.run()
        self.assertEqual((code, [(m, p) for m, p, _ in update.calls]),
                         (0, [('GET', '/webhooks'), ('PUT', '/webhooks/owned')]), output)

    def test_rejects_invalid_or_truncated_sources_before_writing(self):
        bad = Fixture(topic='bad/topic')
        code, output = bad.run()
        self.assertEqual((code, bad.calls), (1, []), output)
        self.assertNotIn('bad/topic', output)

        truncated = Fixture()
        truncated.live = []
        truncated.api = lambda argv, program: Result(argv, 0, 'RESULT ' + json.dumps({
            'status': 200, 'body': {'data': [], 'meta': {'total': 1}}}), '')
        # Replace the registered handler with the response above.
        truncated.runner._rules[-1] = (truncated.runner._rules[-1][0], truncated.api)
        code, output = truncated.run()
        self.assertEqual(code, 1, output)
        self.assertIn('truncated', output)


if __name__ == '__main__':
    unittest.main()


class NotifyWebhookTests(unittest.TestCase):
    """The webhook that hands app alerts to the notify service carries the cluster's alert secret in its body."""

    def test_it_points_at_the_notify_service_and_never_shows_the_secret(self):
        fixture = Fixture()
        fixture.runner.on('kubectl', '-n', 'notify', 'get', 'secret', clickstack_ntfy.ALERT_SECRET,
                          stdout=json.dumps({'data': {'secret': b64('private-alert-secret')}}))
        output = io.StringIO()
        code = clickstack_ntfy.main_notify([], fixture.runner, Report(output, redact=fixture.runner.redact))
        self.assertEqual(code, 0, output.getvalue())
        self.assertEqual([(m, p) for m, p, _ in fixture.calls], [('GET', '/webhooks'), ('POST', '/webhooks')])
        body = fixture.calls[-1][2]
        self.assertEqual((body['name'], body['service'], body['url']),
                         ('swhurl notify', 'generic', 'http://notify.notify.svc.cluster.local:8080/v1/alert'))
        rendered = (body['body'].replace('{{title}}', 'x').replace('{{body}}', 'line one\\nline two')
                    .replace('{{link}}', 'https://clickstack.example/d').replace('{{state}}', 'ALERT'))
        self.assertEqual(json.loads(rendered), {'secret': 'private-alert-secret', 'title': 'x', 'body': 'line one\nline two',
                                                'link': 'https://clickstack.example/d', 'state': 'ALERT'})
        self.assertNotIn('private-alert-secret', output.getvalue() + ' '.join(fixture.runner.echoed))
        self.assertIn('[OK] created swhurl notify', output.getvalue())
        kept = Fixture([{'id': 'w1', **body}])
        kept.runner.on('kubectl', '-n', 'notify', 'get', 'secret', clickstack_ntfy.ALERT_SECRET,
                       stdout=json.dumps({'data': {'secret': b64('private-alert-secret')}}))
        output = io.StringIO()
        self.assertEqual(clickstack_ntfy.main_notify([], kept.runner, Report(output, redact=kept.runner.redact)), 0)
        self.assertEqual([(m, p) for m, p, _ in kept.calls], [('GET', '/webhooks')])
        missing = Fixture()
        missing.runner.on('kubectl', '-n', 'notify', 'get', 'secret', returncode=1, stderr='NotFound')
        output = io.StringIO()
        self.assertEqual(clickstack_ntfy.main_notify([], missing.runner, Report(output, redact=missing.runner.redact)), 1)
        self.assertEqual(missing.calls, [])
