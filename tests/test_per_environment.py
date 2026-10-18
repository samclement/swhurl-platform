"""The table of what differs between environments, and that its three readers agree with it."""
import unittest

from swhurl.apps import calling, contract, events, offers, per_environment
from swhurl.apps.per_environment import (
    CARRY,
    HELD,
    REGENERATE,
    SETTINGS,
    STAYS,
    STRIP,
    WITH_IMAGE,
    WITH_OFFERS,
)

KEY = 'U' + 'A' * 55


def staging() -> dict:
    """A staging release that has every setting of the table."""
    env = {'SWHURL_APP': 'orders', calling.ENVIRONMENT_VARIABLE: 'staging', calling.CALLERS_VARIABLE: 'reports',
           'SWHURL_SERVICE_STOCK_URL': 'http://stock.stock-$(SWHURL_ENV).svc.cluster.local:8080',
           events.PUBLISH_VARIABLE: 'created', events.SUBSCRIBE_VARIABLE: 'stock.low', events.URL_VARIABLE: events.URL,
           events.NKEY_VARIABLE: KEY, calling.API_VARIABLE: '/openapi.json', 'LOG_LEVEL': 'info'}
    host = contract.default_host('orders', 'staging')
    return {
        'apiVersion': 'helm.toolkit.fluxcd.io/v2', 'kind': 'HelmRelease',
        'metadata': {'name': 'orders', 'namespace': 'orders-staging', 'annotations': {offers.ANNOTATION: 'abc1234'}},
        'spec': {'values': {
            'defaultPodOptions': {'labels': {contract.ENVIRONMENT: 'staging'}},
            'controllers': {'main': {
                'replicas': 1, 'annotations': {contract.RELOAD: 'orders-secret,orders-events'},
                'containers': {'main': {
                    'image': {'repository': 'ghcr.io/samclement/orders', 'tag': '7-abc1234', 'digest': 'sha256:' + '1' * 64},
                    'resources': {'requests': {'cpu': '50m'}}, 'env': env,
                    'envFrom': [{'secretRef': {'name': 'orders-secret'}}, {'secretRef': {'name': 'orders-events'}}]}}}},
            'service': {'main': {'controller': 'main', 'ports': {'http': {'port': 8080}}}},
            'persistence': {calling.TOKENS_VOLUME: calling.tokens_volume(['stock'])},
            'ingress': {'main': {'annotations': {'cert-manager.io/cluster-issuer': 'letsencrypt-prod'},
                                 'hosts': [{'host': host, 'paths': [{'path': '/'}]}], 'tls': [{'hosts': [host]}]}}}}}


def leaves(node, path: tuple = ()) -> dict:
    """Every leaf value by its path of keys, nothing left out."""
    if isinstance(node, dict):
        return {k: v for key, value in node.items() for k, v in leaves(value, (*path, key)).items()}
    if isinstance(node, list):
        return {k: v for index, value in enumerate(node) for k, v in leaves(value, (*path, index)).items()}
    return {path: node}


class TableTests(unittest.TestCase):
    def test_every_row_uses_the_stated_words_and_names_one_setting(self):
        for row in (*SETTINGS, *per_environment.FILES):
            with self.subTest(row=getattr(row, 'key', getattr(row, 'name', ''))):
                self.assertIn(row.origin, (per_environment.DECLARED, per_environment.DERIVED, per_environment.GENERATED))
                self.assertIn(row.first, (CARRY, STRIP, REGENERATE))
                self.assertTrue(row.note)
        for setting in SETTINGS:
            self.assertIn(setting.later, (STAYS, WITH_IMAGE, WITH_OFFERS, HELD))
            if setting.first == STRIP:
                self.assertTrue(setting.where, f'{setting.key}: a promotion must know where to find what it strips')
        keys = [setting.key for setting in SETTINGS]
        self.assertEqual(len(keys), len(set(keys)))

    def test_may_differ_reads_the_conditions(self):
        self.assertTrue(per_environment.may_differ('replicas', 2))
        self.assertTrue(per_environment.may_differ('SWHURL_SERVICE_ORDERS_URL', 'http://x'), 'a prefix row')
        self.assertFalse(per_environment.may_differ('SWHURL_APP', 'orders'))
        for key in ('probes', 'runAsUser', 'fsGroup', 'OTEL_SERVICE_NAME', contract.OTLP_HOST_IP):
            self.assertTrue(per_environment.may_differ(key, 'x'), f'{key} moves with the image')
        for key in ('port', 'envFrom', 'persistence'):
            self.assertFalse(per_environment.may_differ(key, 'x'), f'{key} waits while the app has production')
        self.assertTrue(per_environment.may_differ('hosts', ['a.example.com']), 'a list of names (TLS)')
        self.assertFalse(per_environment.may_differ('hosts', [{'host': 'a.example.com'}]), 'a list of rules is compared')
        for key in ('ingress', contract.EXPOSURE):
            self.assertFalse(per_environment.may_differ(key, {}))
            self.assertTrue(per_environment.may_differ(key, {}, exposure_differs=True))

    def test_which_files_are_compared(self):
        for name in ('helmrelease.yaml', 'namespace.yaml', 'kustomization.yaml', contract.QUOTA_FILE, 'extra.yaml'):
            self.assertTrue(per_environment.compared(name), name)
        for name in (contract.IMAGE_AUTOMATION_FILE, contract.NETWORK_POLICY_FILE, events.EVENTS_FILE, events.SECRET_FILE,
                     'secret.sops.yaml', 'other.sops.yaml'):
            self.assertFalse(per_environment.compared(name), name)
        self.assertEqual(per_environment.NOT_CARRIED, {contract.IMAGE_AUTOMATION_FILE, events.EVENTS_FILE, events.SECRET_FILE})


if __name__ == '__main__':
    unittest.main()
