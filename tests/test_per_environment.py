"""The table of what differs between environments, and that its three readers agree with it."""
import copy
import unittest

from swhurl.apps import calling, contract, events, offers, per_environment, policy
from swhurl.apps.per_environment import (
    CARRY,
    HELD,
    REGENERATE,
    SETTINGS,
    STAYS,
    STRIP,
    VARIABLE,
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


class FirstPromotionTests(unittest.TestCase):
    def setUp(self):
        self.source = staging()
        self.before = copy.deepcopy(self.source)
        self.production = per_environment.for_production(self.source, 'orders', 'orders.homelab.swhurl.com')

    def test_each_variable_and_annotation_gets_what_its_row_says(self):
        self.assertEqual(self.source, self.before, "staging's release is not changed")
        was = self.source['spec']['values']['controllers']['main']['containers']['main']['env']
        now = self.production['spec']['values']['controllers']['main']['containers']['main']['env']
        for setting in SETTINGS:
            if setting.where != VARIABLE:
                continue
            for key in [k for k in was if (k.startswith(setting.key) if setting.prefix else k == setting.key)]:
                with self.subTest(key=key, first=setting.first):
                    if setting.first == CARRY:
                        self.assertEqual(now[key], was[key])
                    elif setting.first == STRIP:
                        self.assertNotIn(key, now)
                    else:
                        self.assertNotEqual(now[key], was[key])
        self.assertEqual(now['LOG_LEVEL'], 'info', 'what the table does not list is carried')
        self.assertEqual(now[calling.ENVIRONMENT_VARIABLE], 'prod')
        self.assertNotIn('annotations', self.production['metadata'], 'the offers note is stripped, and the empty mapping with it')

    def test_identity_address_and_the_events_secret(self):
        values = self.production['spec']['values']
        self.assertEqual(self.production['metadata']['namespace'], 'orders-prod')
        self.assertEqual(values['defaultPodOptions']['labels'][contract.ENVIRONMENT], 'prod')
        self.assertEqual(values['ingress']['main']['hosts'][0]['host'], 'orders.homelab.swhurl.com')
        self.assertEqual(values['ingress']['main']['tls'][0]['hosts'], ['orders.homelab.swhurl.com'])
        main = values['controllers']['main']
        self.assertEqual(main['containers']['main']['envFrom'], [{'secretRef': {'name': 'orders-secret'}}])
        self.assertEqual(main['annotations'][contract.RELOAD], 'orders-secret')
        self.assertNotIn('staging', str(self.production))

    def test_a_release_with_only_the_events_secret_loses_the_empty_lists(self):
        main = self.source['spec']['values']['controllers']['main']
        main['containers']['main']['envFrom'] = [{'secretRef': {'name': 'orders-events'}}]
        main['annotations'] = {contract.RELOAD: 'orders-events'}
        main = per_environment.for_production(self.source, 'orders', None)['spec']['values']['controllers']['main']
        self.assertNotIn('envFrom', main['containers']['main'])
        self.assertNotIn('annotations', main)

    def test_whatever_a_first_promotion_changes_the_drift_rule_allows(self):
        """The two readers of the table agree: production as created does not drift from staging."""
        per_environment.without_reference(self.source, 'orders-events')  # as policy.source does for both
        self.assertEqual(policy.flatten(self.production), policy.flatten(self.source))


class LaterPromotionTests(unittest.TestCase):
    def setUp(self):
        self.production = per_environment.for_production(staging(), 'orders', 'orders.homelab.swhurl.com')
        self.image = {'repository': 'ghcr.io/samclement/orders', 'tag': '8-def5678', 'digest': 'sha256:' + '2' * 64}

    def changed(self, after: dict) -> set[str]:
        was, now = leaves(self.production), leaves(after)
        return {path[-1] for path in was.keys() | now.keys() if was.get(path) != now.get(path)}

    def test_the_image_alone(self):
        staging = {calling.DESCRIPTION_VARIABLE: 'Orders, kept'}
        after = per_environment.after_promotion(self.production, self.image, staging_variables=staging)
        self.assertEqual(self.changed(after), {s.key for s in SETTINGS if s.later == WITH_IMAGE})
        self.assertEqual(after['spec']['values']['controllers']['main']['containers']['main']['env'][calling.DESCRIPTION_VARIABLE],
                         'Orders, kept', "staging's description moves with the image")
        without = per_environment.after_promotion(after, self.image)
        self.assertNotIn(calling.DESCRIPTION_VARIABLE, without['spec']['values']['controllers']['main']['containers']['main']['env'],
                         'an image without one takes it away')
        self.assertEqual(after['spec']['values']['controllers']['main']['containers']['main']['image'], self.image)
        untagged = per_environment.after_promotion(self.production, {k: v for k, v in self.image.items() if k != 'tag'})
        self.assertNotIn('tag', untagged['spec']['values']['controllers']['main']['containers']['main']['image'])

    def test_with_what_the_image_offers(self):
        after = per_environment.after_promotion(self.production, self.image, commit='def5678', served='/v2/openapi.json',
                                                staging_variables={calling.DESCRIPTION_VARIABLE: 'Orders'})
        self.assertEqual(self.changed(after), {s.key for s in SETTINGS if s.later in (WITH_IMAGE, WITH_OFFERS)})
        self.assertEqual(after['metadata']['annotations'][offers.ANNOTATION], 'def5678')
        self.assertNotIn(HELD, {s.later for s in SETTINGS if s.key in self.changed(after)}, 'what it publishes is not moved')

    def test_a_worker_gets_no_api_path_and_no_offers_means_no_note(self):
        del self.production['spec']['values']['service']
        del self.production['spec']['values']['controllers']['main']['containers']['main']['env'][calling.API_VARIABLE]
        after = per_environment.after_promotion(self.production, self.image, commit='def5678', served='/openapi.json')
        self.assertNotIn(calling.API_VARIABLE, after['spec']['values']['controllers']['main']['containers']['main']['env'])
        self.assertNotIn('annotations', per_environment.after_promotion(self.production, self.image, served='/x')['metadata'])


if __name__ == '__main__':
    unittest.main()
