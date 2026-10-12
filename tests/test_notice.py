"""A notice has one shape, whoever sends it and wherever it goes."""
import base64
import json
import unittest

import httpx

from swhurl.notifications.errors import NotificationError
from swhurl.notifications.notice import PLATFORM_KEY, Notice, Ntfy, Recorded, ntfy_target, platform_topic
from swhurl.run import FakeRunner

ADDRESS = 'https://ntfy.example/private-topic-0123'
FAILED = Notice('orders/prod unhealthy', 'main 0/1 Ready', True, 'https://console.example.test/apps/orders/prod')
DEPLOYED = Notice('orders/prod deployed')


class NoticeTests(unittest.TestCase):
    def test_priority_is_a_name_for_the_notify_service_and_a_number_for_ntfy(self):
        self.assertEqual(FAILED.to_ntfy('t'), {'topic': 't', 'title': 'orders/prod unhealthy', 'message': 'main 0/1 Ready',
                                              'priority': 4, 'tags': ['warning'], 'click': FAILED.link})
        self.assertEqual(DEPLOYED.to_ntfy('t'), {'topic': 't', 'title': 'orders/prod deployed', 'message': '',
                                                'priority': 3, 'tags': ['white_check_mark']})
        self.assertEqual(FAILED.to_notify_request('orders', 'prod', 'k'),
                         {'app': 'orders', 'env': 'prod', 'key': 'k', 'title': 'orders/prod unhealthy',
                          'message': 'main 0/1 Ready', 'priority': 'high', 'click': FAILED.link})
        self.assertEqual(DEPLOYED.to_notify_request('orders', 'prod', 'k')['priority'], 'normal')

    def test_an_outbox_entry_gives_the_same_notice_back(self):
        for notice in (FAILED, DEPLOYED):
            entry = json.loads(json.dumps(notice.fields() | {'key': 'k', 'queued': 1.0, 'channel': 'payments-phone'}))
            self.assertEqual(Notice.of(entry), notice)

    def test_ntfy_posts_the_notice_to_the_server_with_the_topic_in_the_body(self):
        requests = []

        def accept(request):
            requests.append(request)
            return httpx.Response(200)

        Ntfy(httpx.MockTransport(accept))(ADDRESS, FAILED)
        self.assertEqual(str(requests[0].url), 'https://ntfy.example')
        self.assertEqual(json.loads(requests[0].content), FAILED.to_ntfy('private-topic-0123'))

    def test_an_address_that_is_not_an_https_topic_is_refused_without_repeating_it(self):
        for bad in ('http://ntfy.example/private-topic', 'https://ntfy.example/private/topic', 'https://ntfy.example/private-topic?x=1',
                    'https://ntfy.example/'):
            with self.subTest(bad=bad), self.assertRaises(NotificationError) as error:
                Ntfy(httpx.MockTransport(lambda _request: httpx.Response(200)))(bad, FAILED)
            self.assertNotIn('private', str(error.exception))
        self.assertEqual(ntfy_target(ADDRESS), ('https://ntfy.example', 'private-topic-0123'))

    def test_a_recorded_sender_keeps_or_refuses(self):
        sender = Recorded()
        sender(ADDRESS, FAILED)
        self.assertEqual(sender.sent, [(ADDRESS, FAILED)])
        with self.assertRaisesRegex(NotificationError, 'down'):
            Recorded(refuse='down')(ADDRESS, FAILED)


class PlatformTopicTests(unittest.TestCase):
    def secret(self, value):
        return FakeRunner().on('kubectl', '-n', 'console', 'get', 'secret', 'notification-ntfy', stdout=json.dumps(
            {'data': {PLATFORM_KEY: base64.b64encode(value.encode()).decode()}}))

    def test_it_is_read_from_the_cluster_and_registered_as_a_secret(self):
        runner = self.secret(ADDRESS)
        self.assertEqual(platform_topic(runner), ADDRESS)
        self.assertNotIn('private-topic', runner.redact(f'{ADDRESS} and private-topic-0123'))

    def test_it_is_read_from_the_environment_where_the_secret_is_mounted(self):
        runner = FakeRunner()
        self.assertEqual(platform_topic(runner, {PLATFORM_KEY: ADDRESS}), ADDRESS)
        self.assertEqual(runner.calls, [])
        with self.assertRaisesRegex(NotificationError, f'{PLATFORM_KEY} is missing'):
            platform_topic(runner, {})

    def test_a_missing_empty_or_invalid_address_is_an_error_that_does_not_repeat_it(self):
        for runner in (self.secret(''), self.secret('https://ntfy.example/private/topic'),
                       FakeRunner().on('kubectl', stdout=json.dumps({'data': {}})),
                       FakeRunner().on('kubectl', returncode=1, stderr='NotFound')):
            with self.assertRaises(NotificationError) as error:
                platform_topic(runner)
            self.assertNotIn('private', str(error.exception))


if __name__ == '__main__':
    unittest.main()
