import unittest
from pathlib import Path

from swhurl.apps.instance import ENVIRONMENTS, Instance, instance_folder, instance_unit_file


class InstanceTests(unittest.TestCase):
    def test_every_name_follows_from_the_app_and_its_environment(self):
        found = Instance('demo-orders', 'staging')
        root = Path('/clone')
        self.assertEqual((found.namespace, found.unit, str(found)), ('demo-orders-staging', 'app-demo-orders-staging', 'demo-orders/staging'))
        self.assertEqual(found.folder(root), root / 'apps/demo-orders/staging')
        self.assertEqual(found.folder(root, Path('tests/fixtures/apps/apps')), root / 'tests/fixtures/apps/apps/demo-orders/staging')
        self.assertEqual(found.unit_file(root), root / 'clusters/home/app-demo-orders-staging.yaml')
        self.assertEqual((instance_folder(root, 'Not A Name', 'dev'), instance_unit_file(root, 'x', 'dev')),
                         (root / 'apps/Not A Name/dev', root / 'clusters/home/app-x-dev.yaml'),
                         'the layout alone, for commands that say themselves what is wrong with a name')

    def test_an_instance_is_always_a_valid_one(self):
        for app, env in (('Hello', 'prod'), ('hello', 'dev'), ('', 'prod'), ('-x', 'prod'), ('x-', 'prod'), ('1x', 'prod'),
                         ('a' * 41, 'prod'), ('../x', 'prod'), ('a/b', 'staging'), (None, 'prod')):
            with self.subTest(app=app, env=env):
                with self.assertRaises(ValueError):
                    Instance(app, env)
                self.assertIsNone(Instance.named(app, env))
        self.assertIn('APP must be a DNS label and ENV one of staging, prod', str(self.failure('Hello', 'prod')))
        self.assertEqual([Instance.named('a' * 40, env).env for env in ENVIRONMENTS], ['staging', 'prod'])

    def failure(self, app, env):
        try:
            Instance(app, env)
        except ValueError as error:
            return error

    def test_names_from_outside_are_read_back_or_refused(self):
        try_10 = Instance('swhurl-try-10', 'staging')
        self.assertEqual(Instance.of_namespace('swhurl-try-10-staging'), try_10, "an app's own hyphens are its own")
        self.assertEqual(Instance.of_unit('app-swhurl-try-10-staging'), try_10)
        self.assertEqual(Instance.of_unit('app-app-prod'), Instance('app', 'prod'))
        self.assertEqual(Instance.of_path('./apps/swhurl-try-10/staging'), try_10)
        self.assertEqual(Instance.of_path('apps/swhurl-try-10/staging'), try_10)
        self.assertEqual(Instance.of_path('./tests/fixtures/apps/apps/smoke-web/staging'), Instance('smoke-web', 'staging'))
        for refused in (Instance.of_namespace('kube-system'), Instance.of_namespace('staging'), Instance.of_namespace('web-dev'),
                        Instance.of_unit('platform-console'), Instance.of_unit('web-prod'), Instance.of_unit('app-prod'),
                        Instance.of_unit('app-Web-prod'), Instance.of_path('./platform/console'), Instance.of_path('./apps/web'),
                        Instance.of_path('./apps/web/prod/extra'), Instance.of_path('./elsewhere/apps/web/prod'),
                        Instance.of_path('./apps/web/dev'), Instance.of_path('')):
            self.assertIsNone(refused)


if __name__ == '__main__':
    unittest.main()
