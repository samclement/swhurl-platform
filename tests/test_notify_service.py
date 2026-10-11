"""The notify service: who may send, what is accepted, and the outbox it delivers from (offline)."""
import base64
import json
import unittest

from starlette.testclient import TestClient

from swhurl.apps import channels
from swhurl.notifications import NotificationError, service
from swhurl.run import FakeRunner, Result

NOW = 1_800_000_000.0
CHECKER = 'system:serviceaccount:console:notification-check'
TOKENS = {'checker-token': CHECKER, 'app-token': 'system:serviceaccount:orders-staging:orders',
          'other-app-token': 'system:serviceaccount:billing-prod:billing', 'operator-token': 'kubernetes-admin',
          'console-token': 'system:serviceaccount:console:console', 'stranger-token': 'system:serviceaccount:orders-staging:default'}
BODY = {'app': 'orders', 'env': 'staging', 'key': 'event:1@notify', 'title': 'orders/staging deployed',
        'message': 'Flux UpgradeSucceeded', 'priority': 'normal', 'click': 'https://console.example.test/apps/orders/staging'}
ADDRESS = 'https://ntfy.sh/private-topic'


def cluster(*, teams=None, state=None, fails=(), addresses=None, alert_secret='webhook-secret'):
    """A fake cluster: orders-staging belongs to team payments, whose channels are ``teams['payments']``."""
    runner, saved = FakeRunner(), []
    for prefix in fails:
        runner.on(*prefix, returncode=1, stderr='API down')

    def review(args, text):
        spec = json.loads(text)['spec']
        who = TOKENS.get(spec['token'])
        status = {'authenticated': True, 'audiences': spec['audiences'], 'user': {'username': who}} if who else {}
        if spec['token'] == 'other-audience-token':
            status = {'authenticated': True, 'audiences': ['orders'], 'user': {'username': CHECKER}}
        return Result(args, 0, json.dumps({'spec': spec, 'status': status}))

    def patch(args, text):
        saved.append(json.loads(json.loads(text)['data']['state.json']))
        return Result(args, 0, '{}')
    routes = {'channels': {}, 'teams': teams if teams is not None else {'payments': ['payments-phone', 'payments-desk']}}
    secrets = [{'metadata': {'name': f'channel-{name}', 'labels': {channels.TRANSPORT: 'ntfy'}},
                'data': {'address': base64.b64encode(address.encode()).decode()}}
               for name, address in (addresses or {'payments-phone': ADDRESS, 'payments-desk': ADDRESS + '-desk'}).items()]
    (runner.on(*service.REVIEW, handler=review)
     .on('kubectl', 'get', 'namespace', 'orders-staging', stdout=json.dumps(
         {'metadata': {'name': 'orders-staging', 'labels': {'platform.swhurl.com/team': 'payments'}}}))
     .on('kubectl', 'get', 'namespace', stdout='')
     .on('kubectl', '-n', 'notify', 'get', 'configmaps', stdout=json.dumps({'items': [
         {'metadata': {'name': 'notification-routes'}, 'data': {'routes.json': json.dumps(routes)}}]}))
     .on('kubectl', '-n', 'notify', 'get', 'configmap', 'notify-outbox', stdout=json.dumps(
         {'data': {'state.json': json.dumps(state)}} if state is not None else {}))
     .on('kubectl', '-n', 'notify', 'patch', 'configmap', 'notify-outbox', handler=patch)
     .on('kubectl', '-n', 'notify', 'get', 'secrets', stdout=json.dumps({'items': secrets + (
         [{'metadata': {'name': 'notify-alerts'}, 'data': {'secret': base64.b64encode(alert_secret.encode()).decode()}}]
         if alert_secret else [])})))
    return runner, saved


def service_under_test(runner, sender=None):
    sent = []
    outbox = service.Outbox(runner, sender=sender or (lambda address, notice: sent.append((address, notice))), clock=lambda: NOW)
    return TestClient(service.create_app(runner, outbox)), outbox, sent


def post(client, body=BODY, token='checker-token', **kwargs):
    headers = {'Authorization': f'Bearer {token}'} if token else {}
    return client.post('/v1/notify', json=body, headers=headers, **kwargs)


class CallerTests(unittest.TestCase):
    def test_only_a_token_kubernetes_vouches_for_this_audience_is_a_caller(self):
        runner, saved = cluster()
        client, _, _ = service_under_test(runner)
        for token in (None, 'forged-token', 'other-audience-token'):
            with self.subTest(token=token):
                response = post(client, token=token)
                self.assertEqual((response.status_code, response.json()),
                                 (401, {'error': 'a service-account token for the audience notify is required'}))
        self.assertEqual(client.post('/v1/notify', json=BODY, headers={'Authorization': 'Basic checker-token'}).status_code, 401)
        self.assertEqual(saved, [])
        reviews = [call for call in runner.calls if call[:2] == ('kubectl', 'create')]
        self.assertEqual(len(reviews), 2, 'no token, no question to Kubernetes')
        self.assertIn('--validate=false', service.REVIEW, 'client-side validation lists CRDs, which the service may not')
        self.assertNotIn('token', ' '.join(' '.join(call) for call in runner.calls) + ' '.join(runner.echoed),
                         'the token goes to kubectl on standard input, never as an argument')

    def test_a_person_or_a_service_account_that_is_not_an_apps_may_not_send(self):
        runner, saved = cluster()
        client, _, _ = service_under_test(runner)
        for token in ('operator-token', 'console-token', 'stranger-token'):
            with self.subTest(token=token), self.assertLogs('swhurl.notifications.service', 'WARNING') as logs:
                response = post(client, token=token)
                self.assertEqual((response.status_code, response.json()), (403, {'error': 'this caller may not send notifications'}))
                self.assertIn(TOKENS[token], logs.output[0])
        self.assertEqual(saved, [])

    def test_an_app_sends_as_itself_with_its_name_on_the_title_and_needs_no_key(self):
        runner, saved = cluster()
        client, _, _ = service_under_test(runner)
        with self.assertLogs('swhurl.notifications.service', 'INFO') as logs:
            response = post(client, {'title': ' Order backlog ', 'message': '412 waiting', 'priority': 'high'}, token='app-token')
        self.assertEqual((response.status_code, response.json()), (202, {'channels': ['payments-phone', 'payments-desk']}))
        entry = saved[-1]['pending'][0]
        self.assertEqual((entry['title'], entry['message'], entry['priority'], entry['app'], entry['env']),
                         ('orders/staging: Order backlog', '412 waiting', 4, 'orders', 'staging'))
        self.assertRegex(entry['key'], r'^app:orders:staging:[0-9a-f]{32}@payments-phone$')
        self.assertIn('about orders/staging from orders for 2 channel(s)', logs.output[0])
        self.assertEqual(post(client, {'title': 'again', 'key': 'backlog-1'}, token='app-token').json()['channels'],
                         ['payments-phone', 'payments-desk'])
        self.assertEqual(post(client, {'title': 'again', 'key': 'backlog-1'}, token='app-token').json(), {'channels': []},
                         'its own key makes a retry harmless')
        self.assertEqual(post(client, {'title': 'x', 'app': 'orders', 'env': 'staging'}, token='app-token').status_code, 202)

    def test_an_app_cannot_send_about_another_app_or_environment(self):
        runner, saved = cluster()
        client, _, _ = service_under_test(runner)
        for body in ({'title': 'x', 'app': 'billing'}, {'title': 'x', 'env': 'prod'}, BODY | {'app': 'billing'}):
            with self.subTest(body=body):
                response = post(client, body, token='app-token')
                self.assertEqual((response.status_code, response.json()), (403, {'error': 'an app sends about itself only'}))
        self.assertEqual(post(client, {'message': 'no title'}, token='app-token').status_code, 400)
        self.assertEqual(saved, [])

    def test_an_app_is_limited_to_a_number_of_messages_an_hour_and_the_checker_is_not(self):
        runner, _ = cluster(teams={})
        now = [NOW]
        outbox = service.Outbox(runner, sender=lambda *_: None, clock=lambda: now[0])
        client = TestClient(service.create_app(runner, outbox, service.RateLimit(clock=lambda: now[0])))
        for _ in range(service.RATE[0]):
            self.assertEqual(post(client, {'title': 'x'}, token='app-token').status_code, 202)
        response = post(client, {'title': 'x'}, token='app-token')
        self.assertEqual((response.status_code, response.json()),
                         (429, {'error': 'orders/staging has sent 30 messages in the last hour; try again later'}))
        self.assertEqual(post(client, {'title': 'x'}, token='other-app-token').status_code, 202, 'each app has its own count')
        self.assertEqual(post(client).status_code, 202, 'the checker is not an app')
        now[0] += service.RATE[1] + 1
        self.assertEqual(post(client, {'title': 'x'}, token='app-token').status_code, 202)

    def test_a_token_that_cannot_be_checked_is_not_trusted(self):
        runner, _ = cluster(fails=[('kubectl', 'create')])
        client, _, _ = service_under_test(runner)
        self.assertEqual(post(client).status_code, 503)


ALERT = {'secret': 'webhook-secret', 'state': 'ALERT', 'title': '🚨 swhurl:orders:error-logs',
         'link': 'https://clickstack.example.test/dashboards/d1?from=1',
         'body': 'Group: "arrayElement(ResourceAttributes, \'k8s.namespace.name\'):orders-staging"\n16 meets or exceeds 5\n'
                 'Time Range (UTC): [Oct 11 5:58:00 PM - Oct 11 5:59:00 PM)\n'}


class AlertTests(unittest.TestCase):
    """ClickStack's webhook: a shared secret instead of a token, and the app from the alert's name and group."""

    def client(self, **kwargs):
        runner, saved = cluster(**kwargs)
        client, _, _ = service_under_test(runner)
        return runner, saved, client

    def test_a_firing_alert_becomes_a_high_priority_message_for_the_apps_team(self):
        runner, saved, client = self.client()
        with self.assertLogs('swhurl.notifications.service', 'INFO') as logs:
            response = client.post('/v1/alert', json=ALERT)
        self.assertEqual((response.status_code, response.json()), (202, {'channels': ['payments-phone', 'payments-desk']}))
        entry = saved[-1]['pending'][0]
        self.assertEqual((entry['title'], entry['priority'], entry['click'], entry['app'], entry['env']),
                         ('orders/staging alert: error logs', 4, ALERT['link'], 'orders', 'staging'))
        self.assertEqual(entry['message'], '16 meets or exceeds 5\nTime Range (UTC): [Oct 11 5:58:00 PM - Oct 11 5:59:00 PM)')
        self.assertIn('from ClickStack for 2 channel(s)', logs.output[0])
        self.assertEqual(client.post('/v1/alert', json=ALERT).json(), {'channels': []}, 'the same alert twice is sent once')
        self.assertNotIn('webhook-secret', ' '.join(logs.output) + json.dumps(saved))

    def test_recovery_is_a_normal_priority_message_and_an_unknown_preset_keeps_its_name(self):
        runner, saved, client = self.client()
        response = client.post('/v1/alert', json=ALERT | {'state': 'OK', 'title': '✅ swhurl:orders:slow-pages'})
        self.assertEqual(response.status_code, 202)
        entry = saved[-1]['pending'][0]
        self.assertEqual((entry['title'], entry['priority'], entry['tags']), ('orders/staging recovered: slow-pages', 3, ['white_check_mark']))

    def test_the_secret_is_required_and_compared_with_the_clusters(self):
        runner, saved, client = self.client()
        for body in (ALERT | {'secret': 'guess'}, ALERT | {'secret': ''}, {k: v for k, v in ALERT.items() if k != 'secret'}):
            response = client.post('/v1/alert', json=body)
            self.assertEqual((response.status_code, response.json()), (401, {'error': 'the alert webhook secret is required'}))
        self.assertEqual(client.post('/v1/alert', json=ALERT, headers={'Authorization': 'Bearer checker-token'}
                                     | {}).status_code, 202, 'a token is neither needed nor a way in without the secret')
        self.assertEqual(client.post('/v1/alert', json=ALERT | {'secret': 'guess'},
                                     headers={'Authorization': 'Bearer checker-token'}).status_code, 401)
        runner, saved, client = self.client(alert_secret=None)
        response = client.post('/v1/alert', json=ALERT)
        self.assertEqual((response.status_code, response.json()), (503, {'error': 'the alert webhook secret is not set up'}))
        self.assertEqual(saved, [])

    def test_only_an_apps_own_alert_for_its_own_namespace_is_accepted(self):
        runner, saved, client = self.client()
        cases = (ALERT | {'title': '🚨 CPU high'}, ALERT | {'body': '16 meets or exceeds 5'},
                 ALERT | {'body': ALERT['body'].replace('orders-staging', 'billing-staging')},
                 ALERT | {'body': ALERT['body'].replace('orders-staging', 'orders-dev')}, ALERT | {'title': 7})
        for body in cases:
            with self.subTest(body=str(body)[:60]):
                self.assertEqual(client.post('/v1/alert', json=body).status_code, 400)
        self.assertEqual(client.post('/v1/alert', content=b'[]').status_code, 400)
        self.assertEqual(saved, [])


class AcceptTests(unittest.TestCase):
    def test_a_message_becomes_one_saved_entry_per_channel_of_the_apps_team_before_anything_is_sent(self):
        runner, saved = cluster()
        client, outbox, sent = service_under_test(runner)
        with self.assertLogs('swhurl.notifications.service', 'INFO') as logs:
            response = post(client)
        self.assertEqual((response.status_code, response.json()), (202, {'channels': ['payments-phone', 'payments-desk']}))
        self.assertEqual(sent, [], 'accepting only queues')
        self.assertEqual([(e['key'], e['channel'], e['title'], e['priority'], e['app'], e['env']) for e in saved[-1]['pending']],
                         [('event:1@notify@payments-phone', 'payments-phone', 'orders/staging deployed', 3, 'orders', 'staging'),
                          ('event:1@notify@payments-desk', 'payments-desk', 'orders/staging deployed', 3, 'orders', 'staging')])
        self.assertIn('Accepted "orders/staging deployed" about orders/staging from notification-check for 2 channel(s)', logs.output[0])
        self.assertTrue(outbox.wake.is_set(), 'delivery starts at once')
        self.assertEqual(post(client).json(), {'channels': []}, 'the same key is queued once')
        self.assertEqual(len(saved), 1)
        self.assertEqual(client.get('/healthz').text, 'ok\n')

    def test_an_app_without_a_team_channel_or_namespace_is_accepted_for_no_channel(self):
        for body, teams in ((BODY, {}), (BODY | {'app': 'ghost'}, None), (BODY | {'env': 'prod'}, None)):
            with self.subTest(app=body['app'], env=body['env']):
                runner, saved = cluster(teams=teams)
                client, _, _ = service_under_test(runner)
                response = post(client, body)
                self.assertEqual((response.status_code, response.json(), saved), (202, {'channels': []}, []))

    def test_a_malformed_message_is_refused_and_says_why(self):
        runner, saved = cluster()
        client, _, _ = service_under_test(runner)
        cases = ((BODY | {'app': '../x'}, 'app must be an app name'), (BODY | {'env': 'dev'}, 'env staging or prod'),
                 (BODY | {'title': ' '}, 'a title of at most 250'), (BODY | {'title': 'x' * 251}, 'a title of at most 250'),
                 (BODY | {'message': 'x' * 3501}, 'a message is at most 3500'), (BODY | {'priority': 'urgent'}, 'priority is one of normal, high'),
                 (BODY | {'click': 'javascript:alert(1)'}, 'click must be an https URL'), (BODY | {'key': ''}, 'key is required'),
                 (BODY | {'channel': 'payments-phone'}, 'unknown field channel'), (BODY | {'title': 7}, 'are strings'),
                 ([BODY], 'one JSON object'))
        for body, reason in cases:
            with self.subTest(reason=reason):
                response = post(client, body)
                self.assertEqual(response.status_code, 400)
                self.assertIn(reason, response.json()['error'])
        self.assertEqual(post(client, content=b'{', body=None).status_code, 400)
        self.assertEqual(post(client, BODY | {'message': 'x' * 3000, 'title': 'y' * 250, 'key': 'k' * 300}).status_code, 202)
        big = client.post('/v1/notify', content=b'x' * (service.MAX_BODY + 1), headers={'Authorization': 'Bearer checker-token'})
        self.assertEqual(big.status_code, 413)
        self.assertEqual(len(saved), 1)

    def test_what_cannot_be_read_or_saved_is_answered_503_and_nothing_is_kept_in_memory_only(self):
        for fails in ([('kubectl', '-n', 'notify', 'get', 'configmaps')], [('kubectl', 'get', 'namespace')],
                      [('kubectl', '-n', 'notify', 'patch')], [('kubectl', '-n', 'notify', 'get', 'configmap', 'notify-outbox')]):
            with self.subTest(fails=fails):
                runner, saved = cluster(fails=fails)
                client, outbox, _ = service_under_test(runner)
                self.assertEqual(post(client).status_code, 503)
                self.assertEqual(saved, [])
                self.assertFalse((outbox.state or {}).get('pending'))
        runner, _ = cluster(state={'version': 1, 'seen': {}, 'pending': [{'key': str(i)} for i in range(service.MAX_PENDING - 1)]})
        client, _, _ = service_under_test(runner)
        response = post(client)
        self.assertEqual((response.status_code, response.json()), (503, {'error': 'the outbox is full; try again later'}))


class DeliveryTests(unittest.TestCase):
    def test_entries_go_to_their_channels_address_and_are_remembered(self):
        runner, saved = cluster()
        client, outbox, sent = service_under_test(runner)
        post(client, BODY | {'priority': 'high'})
        outbox.deliver()
        self.assertEqual([(address, notice['title'], notice['priority'], notice['click']) for address, notice in sent],
                         [(ADDRESS, 'orders/staging deployed', 4, BODY['click']), (ADDRESS + '-desk', 'orders/staging deployed', 4, BODY['click'])])
        self.assertEqual(saved[-1]['pending'], [])
        self.assertEqual(saved[-1]['seen'], {'event:1@notify@payments-phone': NOW + service.HISTORY,
                                             'event:1@notify@payments-desk': NOW + service.HISTORY})
        self.assertEqual(post(client).json(), {'channels': []}, 'a repeated request is not sent twice')
        self.assertIn('<redacted>', runner.redact(f'posting to {ADDRESS}'))
        calls = len(runner.calls)
        outbox.deliver()
        self.assertEqual(len(runner.calls), calls, 'an empty outbox reads nothing')

    def test_a_channel_that_fails_or_has_no_secret_is_retried_without_holding_up_the_others(self):
        runner, saved = cluster(addresses={'payments-desk': ADDRESS + '-desk'})
        delivered = []

        def sender(address, notice):
            delivered.append(notice['channel'])
        client, outbox, _ = service_under_test(runner, sender)
        post(client)
        with self.assertLogs('swhurl.notifications.delivery', 'ERROR') as logs:
            outbox.deliver()
        self.assertEqual(delivered, ['payments-desk'])
        self.assertEqual([e['channel'] for e in saved[-1]['pending']], ['payments-phone'])
        self.assertIn('channel payments-phone: no ntfy channel Secret with that name in the cluster (will retry)', logs.output[0])

    def test_a_restarted_service_sends_what_the_last_one_accepted(self):
        runner, saved = cluster()
        client, _, _ = service_under_test(runner)
        post(client)
        runner, saved = cluster(state=saved[-1])
        _, outbox, sent = service_under_test(runner)
        outbox.deliver()
        self.assertEqual(len(sent), 2)
        self.assertEqual(saved[-1]['pending'], [])
        runner, _ = cluster(state={'version': 2})
        _, outbox, _ = service_under_test(runner)
        with self.assertRaisesRegex(NotificationError, 'refusing to reset delivery history'):
            outbox.deliver()


if __name__ == '__main__':
    unittest.main()
