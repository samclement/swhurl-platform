"""Webhook templates preserve their JSON escaping and match the delivery readers."""
import json
import os
import subprocess
import sys
import unittest

from test_clickstack import b64
from test_dashboards import TOPIC, credentials

from swhurl import ROOT, clickstack_ntfy
from swhurl.notifications import service
from swhurl.notifications.notice import Notice
from swhurl.run import FakeRunner


def filled(template, **values):
    """A webhook body as HyperDX sends it: each variable JSON-escaped, the condition on the state decided."""
    body = template.replace('{{#if (eq state "ALERT")}}4{{else}}3{{/if}}', '4' if values['state'] == 'ALERT' else '3')
    for name, value in values.items():
        body = body.replace('{{' + name + '}}', json.dumps(value)[1:-1])
    return body


class TemplateTests(unittest.TestCase):
    """Each webhook's template, filled in, is what its reader takes: the template and the reader change together."""
    FIRED = {'title': '🚨 swhurl:orders:error-logs', 'link': 'https://clickstack.example.test/dashboards/d1?from=1',
             'body': 'Group: "arrayElement(ResourceAttributes, \'k8s.namespace.name\'):orders-staging"\n16 meets or exceeds 5\n'
                     'Time Range (UTC): [Oct 11 5:58:00 PM - Oct 11 5:59:00 PM)\n'}

    def test_the_notify_service_reads_what_the_notify_webhook_sends(self):
        runner = credentials(FakeRunner())
        runner.on('kubectl', '-n', 'notify', 'get', 'secret', clickstack_ntfy.ALERT_SECRET,
                  stdout=json.dumps({'data': {'secret': b64('private-alert-secret')}}))
        runner.on('kubectl', '-n', 'notify', 'get', 'secrets', stdout=json.dumps({'items': [
            {'metadata': {'name': service.ALERT_SECRET}, 'data': {'secret': b64('private-alert-secret')}}]}))
        template = clickstack_ntfy.notify_destination(runner)['body']
        for state, title, high in (('ALERT', 'orders/staging alert: error logs', True),
                                   ('OK', 'orders/staging recovered: error logs', False)):
            with self.subTest(state=state):
                wanted = service.alert(runner, filled(template, state=state, **self.FIRED).encode())
                self.assertEqual((wanted['app'], wanted['env']), ('orders', 'staging'))
                self.assertEqual(wanted['notice'], Notice(
                    title, '16 meets or exceeds 5\nTime Range (UTC): [Oct 11 5:58:00 PM - Oct 11 5:59:00 PM)', high, self.FIRED['link']))

    def test_the_platform_webhook_sends_what_a_notice_sends(self):
        template = clickstack_ntfy.destination(credentials(FakeRunner()))['body']
        self.assertNotIn('{{/if}}}', template)  # Handlebars treats this as a triple-brace close.
        for state, high in (('ALERT', True), ('OK', False)):
            with self.subTest(state=state):
                sent = json.loads(filled(template, state=state, **self.FIRED))
                same = Notice(f"ClickStack: {self.FIRED['title']}", self.FIRED['body'], high, self.FIRED['link']).to_ntfy(TOPIC)
                del same['tags']  # the webhook sends none
                self.assertEqual(sent, same)

    def test_the_dashboard_command_loads_without_httpx(self):
        """make runs them with the system interpreter, which has no httpx: only sending a notice may need it."""
        loaded = subprocess.run([sys.executable, '-c', "import sys; sys.modules['httpx'] = None; import swhurl.dashboards"],
                                env=os.environ | {'PYTHONPATH': str(ROOT / 'tools')}, capture_output=True, text=True)
        self.assertEqual(loaded.returncode, 0, loaded.stderr)
