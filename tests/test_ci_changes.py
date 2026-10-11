"""Path classification for the app-only Validate workflow fast path."""

import unittest

from ci_changes import classify


class CIChangeClassificationTests(unittest.TestCase):
    def test_app_instance_and_matching_flux_unit_use_fast_path(self):
        self.assertEqual(classify([
            'apps/weather/staging/helmrelease.yaml',
            'clusters/home/app-weather-staging.yaml',
        ]), (True, False))

    def test_sample_instance_changes_keep_tooling_tests(self):
        self.assertEqual(classify(['tests/fixtures/instance/apps/hello/staging/helmrelease.yaml']), (False, True))
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


if __name__ == '__main__':
    unittest.main()
