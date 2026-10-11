"""The team registry commands: the registry and the apps that name a team always change together."""
import io
import shutil
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import contract, policy, teams
from swhurl.apps.yaml_file import EditError

SAMPLE = ROOT / 'tests/fixtures/instance'  # one app, hello, in staging and prod, team platform


class TeamTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        for rel in ('apps', 'clusters'):
            shutil.copytree(SAMPLE / rel, self.root / rel)
        (self.root / 'apps/teams.yaml').write_text(
            '# Teams that own apps.\nteams:\n  platform:\n    description: The platform itself\n  payments: {}\n')

    def quiet(self, fn, *args, **kwargs):
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()):
            fn(self.root, *args, **kwargs)
        return out.getvalue()

    def registered(self):
        return contract.teams(self.root)

    def team_of(self, env):
        return yaml.safe_load((self.root / 'apps/hello' / env / 'namespace.yaml').read_text())['metadata']['labels'][contract.TEAM]

    def test_add_and_describe(self):
        self.quiet(teams.add, 'insights', '  Reporting\n and  dashboards ')
        self.assertEqual(self.registered(), {'platform': 'The platform itself', 'payments': '', 'insights': 'Reporting and dashboards'})
        self.assertTrue((self.root / 'apps/teams.yaml').read_text().startswith('# Teams that own apps.'), 'comments are kept')
        self.quiet(teams.change, 'payments', description='Orders')
        self.assertEqual(self.registered()['payments'], 'Orders')
        self.quiet(teams.change, 'payments', description='')
        self.assertEqual(self.registered()['payments'], '')
        cases = ((teams.add, ('payments',), 'already exists'), (teams.add, ('Bad Name',), 'a DNS label'),
                 (teams.add, ('x', 'd' * 201), 'at most 200 characters'), (teams.change, ('nobody',), 'no team nobody'),
                 (teams.change, ('payments',), 'give --description and/or --rename'))
        for fn, args, message in cases:
            with self.subTest(args=args), self.assertRaisesRegex(EditError, message):
                fn(self.root, *args)

    def test_a_rename_relabels_every_app_of_the_team(self):
        self.quiet(teams.remove, 'payments')
        self.quiet(teams.add, 'core')
        self.quiet(teams.remove, 'core')  # round trip of an empty team
        (self.root / 'apps/teams.yaml').write_text('teams:\n  platform: {}\n  legacy: {description: Old}\n  payments: {}\n')
        for env in ('staging', 'prod'):
            path = self.root / 'apps/hello' / env / 'namespace.yaml'
            path.write_text(path.read_text().replace('team: platform', 'team: legacy'))
        out = self.quiet(teams.change, 'legacy', rename='orders')
        self.assertIn('renamed to orders; relabelled 1 app(s)', out)
        self.assertEqual(list(self.registered()), ['platform', 'orders', 'payments'], 'it keeps its place and description')
        self.assertEqual(self.registered()['orders'], 'Old')
        self.assertEqual((self.team_of('staging'), self.team_of('prod')), ('orders', 'orders'))
        self.assertEqual(teams.apps_of(self.root), {'orders': ['hello']})
        self.assertEqual(policy.drift([self.root / 'apps/hello/prod', self.root / 'apps/hello/staging']), [])
        with self.assertRaisesRegex(EditError, 'already exists; to merge the two, remove orders with --move-apps-to payments'):
            teams.change(self.root, 'orders', rename='payments')
        with self.assertRaisesRegex(EditError, 'default team for new apps and cannot be renamed'):
            teams.change(self.root, 'platform', rename='core')

    def test_a_team_with_apps_is_not_removed_unless_they_are_moved(self):
        self.quiet(teams.add, 'legacy')
        for env in ('staging', 'prod'):
            path = self.root / 'apps/hello' / env / 'namespace.yaml'
            path.write_text(path.read_text().replace('team: platform', 'team: legacy'))
        before = (self.root / 'apps/teams.yaml').read_text()
        with self.assertRaisesRegex(EditError, 'team legacy still owns hello: move them with --move-apps-to'):
            teams.remove(self.root, 'legacy')
        for target, message in (('legacy', 'another registered team'), ('nobody', 'another registered team: platform, payments')):
            with self.subTest(target=target), self.assertRaisesRegex(EditError, message):
                teams.remove(self.root, 'legacy', target)
        self.assertEqual(((self.root / 'apps/teams.yaml').read_text(), self.team_of('staging')), (before, 'legacy'),
                         'a refused removal changes nothing')
        out = self.quiet(teams.remove, 'legacy', 'payments')
        self.assertIn('removed team legacy; moved hello to payments', out)
        self.assertNotIn('legacy', self.registered())
        self.assertEqual((self.team_of('staging'), self.team_of('prod')), ('payments', 'payments'))
        with self.assertRaisesRegex(EditError, 'default team for new apps and cannot be removed'):
            teams.remove(self.root, 'platform')

    def test_an_app_naming_an_unregistered_team_is_found(self):
        path = self.root / 'apps/hello/staging/namespace.yaml'
        path.write_text(path.read_text().replace('team: platform', 'team: ghost'))
        self.assertEqual(teams.apps_of(self.root), {'ghost': ['hello'], 'platform': ['hello']})
        self.quiet(teams.add, 'ghost')  # registering it is one of the two ways out
        self.assertIn('ghost', self.registered())

    def test_commands_exit_2_on_refusal(self):
        err = io.StringIO()
        with redirect_stderr(err), redirect_stdout(io.StringIO()):
            self.assertEqual(teams.main_remove(['platform', '--root', str(self.root)]), 2)
            self.assertEqual(teams.main_add(['insights', '--description', 'Reporting', '--root', str(self.root)]), 0)
            self.assertEqual(teams.main_edit(['insights', '--rename', 'reports', '--root', str(self.root)]), 0)
        self.assertIn('[ERROR] platform is the default team', err.getvalue())
        self.assertIn('reports', self.registered())


if __name__ == '__main__':
    unittest.main()
