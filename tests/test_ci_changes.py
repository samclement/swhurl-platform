"""Path classification for the app-only Validate workflow fast path."""

import unittest

from ci_changes import classify, records_only


class CIChangeClassificationTests(unittest.TestCase):
    def test_app_instance_and_matching_flux_unit_use_fast_path(self):
        self.assertEqual(classify([
            'apps/weather/staging/helmrelease.yaml',
            'clusters/home/app-weather-staging.yaml',
        ]), (True, False))

    def test_a_fixture_path_keeps_tooling_tests(self):
        self.assertEqual(classify(['tests/fixtures/apps/apps/smoke-web/staging/helmrelease.yaml']), (False, True))
        self.assertEqual(classify(['apps/hello/staging/helmrelease.yaml']), (True, False))

    def test_mixed_and_unclassifiable_changes_run_everything(self):
        for paths in ([], ['README.md'], ['apps/weather/staging/helmrelease.yaml', 'clusters/home/platform.yaml']):
            with self.subTest(paths=paths):
                self.assertEqual(classify(paths), (False, True))

    def test_only_app_unit_files_are_accepted_from_cluster_root(self):
        self.assertEqual(classify(['clusters/home/app-weather-prod.yaml']), (True, False))
        self.assertEqual(classify(['clusters/home/app-weather-prod.yml']), (False, True))

    def test_a_new_instance_registering_its_unit_is_app_only(self):
        self.assertEqual(classify(['apps/weather/prod/helmrelease.yaml', 'clusters/home/app-weather-prod.yaml',
                                   'clusters/home/kustomization.yaml']), (True, False))
        self.assertEqual(classify(['clusters/home/flux-system/kustomization.yaml']), (False, True))

    def test_records_only_changes_are_the_input_records(self):
        for paths in (['apps/weather/prod/release.yaml', 'apps/weather/prod/offers/api.yaml'],
                      ['apps/weather/app.yaml', 'apps/weather/staging/instance.yaml',
                       'apps/weather/staging/events.sops.yaml']):
            with self.subTest(paths=paths):
                self.assertTrue(records_only(paths))

    def test_team_and_channel_inputs_are_records_only_but_generated_routes_are_not(self):
        paths = ['v2-records/teams.json', 'apps/weather/app.yaml', 'apps/weather/staging/instance.yaml',
                 'platform/notify/channels/channel-payments-phone.sops.yaml']
        self.assertTrue(records_only(paths))
        self.assertEqual(classify(paths), (False, True), 'shared inputs still need the full tooling checks')
        for extra in ('platform/notify/routes.json', 'platform/notify/namespace.yaml',
                      'platform/notify/channels/kustomization.yaml', 'platform/notify/channel-phone.sops.yaml',
                      'platform/notify/channels/other.sops.yaml', 'clusters/home/platform.yaml', 'apps/teams.yaml'):
            with self.subTest(extra=extra):
                self.assertFalse(records_only([*paths, extra]))

    def test_a_rendered_file_or_anything_else_is_not_records_only(self):
        for paths in ([], ['apps/weather/prod/release.yaml', 'apps/weather/prod/helmrelease.yaml'],
                      ['apps/weather/prod/release.yaml', 'clusters/home/kustomization.yaml'],
                      ['apps/weather/qa/release.yaml'], ['tests/fixtures/apps/apps/x/staging/release.yaml'],
                      ['apps/weather/prod/release.yaml', 'tools/swhurl/apps/instance.py']):
            with self.subTest(paths=paths):
                self.assertFalse(records_only(paths))


if __name__ == '__main__':
    unittest.main()
