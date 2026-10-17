import unittest

from swhurl.apps import connections


class ConnectionRowsTests(unittest.TestCase):
    def test_every_connection_is_one_row_with_the_same_shape(self):
        rows = connections.rows(calls=['orders'], callers=['reports'], publishes=['seen'],
                                subscribes=[('orders', 'created')], subscribers=[('seen', 'audit')],
                                teams={'orders': 'payments', 'audit': 'platform'}, reaches=['api.stripe.com:443'])
        self.assertEqual([(r.role, r.direction, r.kind, r.app, r.event, r.team, r.what) for r in rows], [
            ('calls', 'out', 'call', 'orders', '', 'payments', 'its API'),
            ('subscribes', 'out', 'event', 'orders', 'created', 'payments', 'receives'),
            ('publishes', 'out', 'event', '', 'seen', '', 'publishes'),
            ('reaches', 'out', 'host', '', 'api.stripe.com:443', '', 'outside the cluster'),
            ('called-by', 'in', 'call', 'reports', '', '', 'calls this app'),
            ('subscriber', 'in', 'event', 'audit', 'seen', 'platform', 'receives')])
        self.assertEqual({r.role for r in rows}, set(connections.ROLES))
        self.assertEqual(connections.rows(), ())


if __name__ == '__main__':
    unittest.main()
