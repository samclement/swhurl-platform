"""The team registry commands: the registry and the apps that name a team always change together."""
import io
import json
import shutil
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

import yaml

from swhurl import ROOT
from swhurl.apps import channels, contract, derive, policy, teams
from swhurl.apps.yaml_file import EditError
from swhurl.run import FakeRunner, Result

SAMPLE = ROOT / 'tests/fixtures/instance'  # one app, hello, in staging and prod, team demo


class TeamTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        for rel in ('apps', 'clusters'):
            shutil.copytree(SAMPLE / rel, self.root / rel)
        (self.root / 'apps/teams.yaml').write_text(
            '# Teams that own apps.\nteams:\n  demo:\n    description: The platform itself\n  payments: {}\n')

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
        self.assertEqual(self.registered(), {'demo': 'The platform itself', 'payments': '', 'insights': 'Reporting and dashboards'})
        self.assertTrue((self.root / 'apps/teams.yaml').read_text().startswith('# Teams that own apps.'), 'comments are kept')
        self.quiet(teams.change, 'payments', description='Orders')
        self.assertEqual(self.registered()['payments'], 'Orders')
        self.quiet(teams.change, 'payments', description='')
        self.assertEqual(self.registered()['payments'], '')
        cases = ((teams.add, ('payments',), 'already exists'), (teams.add, ('Bad Name',), 'a DNS label'),
                 (teams.add, ('x', 'd' * 201), 'at most 200 characters'), (teams.change, ('nobody',), 'no team nobody'),
                 (teams.change, ('payments',), 'give --description, --rename and/or --notify'))
        for fn, args, message in cases:
            with self.subTest(args=args), self.assertRaisesRegex(EditError, message):
                fn(self.root, *args)

    def owned_by(self, team):
        """hello belongs to ``team``: its App record, and what that renders."""
        path = self.root / 'apps/hello/app.yaml'
        path.write_text(path.read_text().replace('team: demo', f'team: {team}'))
        self.quiet(derive.sync)

    def test_a_rename_relabels_every_app_of_the_team(self):
        self.quiet(teams.remove, 'payments')
        self.quiet(teams.add, 'core')
        self.quiet(teams.remove, 'core')  # round trip of an empty team
        (self.root / 'apps/teams.yaml').write_text('teams:\n  demo: {}\n  legacy: {description: Old}\n  payments: {}\n')
        self.owned_by('legacy')
        out = self.quiet(teams.change, 'legacy', rename='orders')
        self.assertIn('renamed to orders; relabelled 1 app(s)', out)
        self.assertEqual(list(self.registered()), ['demo', 'orders', 'payments'], 'it keeps its place and description')
        self.assertEqual(self.registered()['orders'], 'Old')
        self.assertEqual((self.team_of('staging'), self.team_of('prod')), ('orders', 'orders'))
        self.assertEqual(teams.apps_of(self.root), {'orders': ['hello']})
        self.assertEqual(policy.drift([self.root / 'apps/hello/prod', self.root / 'apps/hello/staging']), [])
        with self.assertRaisesRegex(EditError, 'already exists; to merge the two, remove orders with --move-apps-to payments'):
            teams.change(self.root, 'orders', rename='payments')
        self.quiet(teams.change, 'demo', rename='core')  # no team is special: any can be renamed
        self.assertEqual(list(self.registered()), ['core', 'orders', 'payments'])

    def test_a_team_with_apps_is_not_removed_unless_they_are_moved(self):
        self.quiet(teams.add, 'legacy')
        self.owned_by('legacy')
        before = (self.root / 'apps/teams.yaml').read_text()
        with self.assertRaisesRegex(EditError, 'team legacy still owns hello: move them with --move-apps-to'):
            teams.remove(self.root, 'legacy')
        for target, message in (('legacy', 'another registered team'), ('nobody', 'another registered team: demo, payments')):
            with self.subTest(target=target), self.assertRaisesRegex(EditError, message):
                teams.remove(self.root, 'legacy', target)
        self.assertEqual(((self.root / 'apps/teams.yaml').read_text(), self.team_of('staging')), (before, 'legacy'),
                         'a refused removal changes nothing')
        out = self.quiet(teams.remove, 'legacy', 'payments')
        self.assertIn('removed team legacy; moved hello to payments', out)
        self.assertNotIn('legacy', self.registered())
        self.assertEqual((self.team_of('staging'), self.team_of('prod')), ('payments', 'payments'))
        self.quiet(teams.remove, 'demo')  # no team is special: one that owns nothing can go
        self.assertEqual(list(self.registered()), ['payments'])

    def notify_unit(self):
        directory = self.root / channels.NOTIFY_DIR
        directory.mkdir(parents=True)
        (directory / 'kustomization.yaml').write_text('# Channels.\nresources:\n  - namespace.yaml\n  - routes.yaml\n')
        teams.write_routes(self.root)
        (self.root / channels.UNIT_FILE).write_text(
            '# The notify unit.\nspec:\n  path: ./platform/notify\n  prune: true\n')

        def encrypt(args, _input):
            path = Path(args[-1])
            document = yaml.safe_load(path.read_text())
            document['stringData'] = {key: 'ENC[...]' for key in document['stringData']}
            path.write_text(yaml.safe_dump(document) + 'sops:\n  version: 3\n')
            return Result(args, 0)
        return FakeRunner().on('sops', '--encrypt', '--in-place', handler=encrypt)

    def routes(self):
        configmap = yaml.safe_load((self.root / channels.ROUTES_FILE).read_text())
        return json.loads(configmap['data']['routes.json'])['teams']

    def test_a_channel_is_written_encrypted_listed_and_never_shown(self):
        runner = self.notify_unit()
        address = 'https://ntfy.sh/swhurl-0123456789abcdef01234567'
        out = self.quiet(teams.channel_add, 'payments-phone', address, runner=runner)
        path = self.root / channels.channel_file('payments-phone')
        self.assertNotIn('0123456789abcdef', out + path.read_text())
        self.assertEqual(channels.channels(self.root), {'payments-phone': 'ntfy'})
        self.assertIn('channel-payments-phone.sops.yaml',
                      yaml.safe_load((path.parent / 'kustomization.yaml').read_text())['resources'])
        self.assertIn('# Channels.', (path.parent / 'kustomization.yaml').read_text())
        self.assertEqual(yaml.safe_load(path.read_text())['metadata']['namespace'], 'notify')
        unit = self.root / channels.UNIT_FILE
        self.assertEqual(yaml.safe_load(unit.read_text())['spec']['decryption'],
                         {'provider': 'sops', 'secretRef': {'name': 'sops-age'}})
        self.assertIn('# The notify unit.', unit.read_text())
        self.assertEqual(channels.problems(self.root), [])
        before = path.read_text()
        cases = ((('payments-phone', address), {}, 'already exists; --replace'),
                 (('other', address), {'replace': True}, 'no channel other to replace'),
                 (('Bad Name', address), {}, 'a DNS label'),
                 (('x', 'http://ntfy.sh/topic'), {}, 'an HTTPS topic URL'),
                 (('x', 'https://ntfy.sh/a/b'), {}, 'an HTTPS topic URL'),
                 (('x', 'https://ntfy.sh/topic?x=1'), {}, 'an HTTPS topic URL'),
                 (('x', address), {'transport': 'pigeon'}, 'unknown transport'))
        for args, kwargs, message in cases:
            with self.subTest(args=args[0], kwargs=kwargs), self.assertRaisesRegex(EditError, message) as raised:
                teams.channel_add(self.root, *args, runner=runner, **kwargs)
            self.assertNotIn('0123456789abcdef', str(raised.exception))
        self.assertEqual(path.read_text(), before)
        self.assertEqual(list(channels.channels(self.root)), ['payments-phone'], 'a refused channel leaves no file')
        self.assertIn('replaced', self.quiet(teams.channel_add, 'payments-phone', address, replace=True, runner=runner))

    def test_a_failed_encryption_leaves_nothing_in_plain_text(self):
        self.notify_unit()
        for runner in (FakeRunner().on('sops', returncode=1, stderr='no key'), FakeRunner().on('sops')):
            with self.subTest(runner=runner), self.assertRaisesRegex(EditError, 'nothing was changed'):
                teams.channel_add(self.root, 'payments-phone', 'https://ntfy.sh/private', runner=runner)
            self.assertEqual(channels.channels(self.root), {})

    def test_a_team_notifies_registered_channels_and_the_routes_follow(self):
        runner = self.notify_unit()
        for name in ('payments-phone', 'payments-desk'):
            self.quiet(teams.channel_add, name, 'https://ntfy.sh/topic', runner=runner)
        with self.assertRaisesRegex(EditError, 'no channel ghost; channels: payments-desk, payments-phone'):
            teams.change(self.root, 'payments', notify=['ghost'])
        with self.assertRaisesRegex(EditError, 'a team notifies one channel, not 2: choose one of payments-phone, payments-desk'):
            teams.change(self.root, 'payments', notify=['payments-phone', 'payments-desk', 'payments-phone'])
        self.quiet(teams.change, 'payments', notify=['payments-phone', 'payments-phone'])
        self.assertEqual(self.routes(), {'payments': ['payments-phone']})
        self.assertEqual(channels.problems(self.root), [])
        registry = self.root / 'apps/teams.yaml'
        one = registry.read_text()
        registry.write_text(one.replace('- payments-phone', '- payments-phone\n    - payments-desk'))
        teams.write_routes(self.root)
        self.assertEqual(channels.problems(self.root), ['notify-channels: team payments notifies 2 channels; a team has one '
                                                        '(make team-edit NAME=payments ARGS="--notify <channel>")'])
        registry.write_text(one)
        teams.write_routes(self.root)
        with self.assertRaisesRegex(EditError, 'channel payments-phone is notified by payments'):
            teams.channel_remove(self.root, 'payments-phone')
        self.quiet(teams.change, 'payments', rename='billing')
        self.assertEqual(self.routes(), {'billing': ['payments-phone']})
        self.quiet(teams.change, 'billing', notify=['payments-desk'])
        self.quiet(teams.channel_remove, 'payments-phone')
        self.assertEqual(channels.channels(self.root), {'payments-desk': 'ntfy'})
        self.assertIn('decryption', (self.root / channels.UNIT_FILE).read_text())
        self.quiet(teams.change, 'billing', notify=[])
        self.quiet(teams.channel_remove, 'payments-desk')
        self.assertNotIn('decryption', (self.root / channels.UNIT_FILE).read_text(), 'no channel, nothing to decrypt')
        self.assertEqual(channels.problems(self.root), [])
        self.quiet(teams.remove, 'billing')
        self.assertEqual(self.routes(), {})
        self.quiet(teams.change, 'demo', notify=[])
        self.assertNotIn('notify', (self.root / 'apps/teams.yaml').read_text())

    def test_the_policy_finds_channels_teams_and_routes_that_disagree(self):
        runner = self.notify_unit()
        self.quiet(teams.channel_add, 'payments-phone', 'https://ntfy.sh/topic', runner=runner)
        self.quiet(teams.change, 'payments', notify=['payments-phone'])
        registry = self.root / 'apps/teams.yaml'
        registry.write_text(registry.read_text().replace('- payments-phone', '- ghost'))
        kustomization = self.root / channels.NOTIFY_DIR / 'kustomization.yaml'
        kustomization.write_text(kustomization.read_text().replace('  - channel-payments-phone.sops.yaml\n', ''))
        found = '\n'.join(channels.problems(self.root))
        for expected in ('team payments notifies ghost, which is not a channel', 'is not in platform/notify/kustomization.yaml',
                         'routes.yaml is not what the channel files and apps/teams.yaml declare'):
            self.assertIn(f'notify-channels: {expected}' if expected.startswith('team') else expected, found)
        scratch = self.root / 'elsewhere'
        scratch.mkdir()
        self.assertEqual(channels.problems(scratch), [], 'a root without the notify unit has nothing to check')

    def test_a_team_is_given_its_channel_a_new_address_and_loses_it_in_one_command_each(self):
        runner = self.notify_unit()
        address = 'https://ntfy.sh/swhurl-0123456789abcdef01234567'
        out = self.quiet(teams.team_channel, 'payments', address, runner=runner)
        self.assertEqual((channels.channels(self.root), self.routes()), ({'payments': 'ntfy'}, {'payments': ['payments']}))
        self.assertNotIn('0123456789abcdef', out)
        first = (self.root / channels.channel_file('payments')).read_text()
        self.assertIn('replaced', self.quiet(teams.team_channel, 'payments', address + 'x', runner=runner))
        self.assertEqual(self.routes(), {'payments': ['payments']}, 'a new address keeps the same channel')
        self.quiet(teams.change, 'payments', rename='billing')
        self.quiet(teams.team_channel, 'billing', address, runner=runner)
        self.assertEqual(channels.channels(self.root), {'payments': 'ntfy'}, 'a renamed team keeps the channel it has')
        self.quiet(teams.team_channel, 'demo', address, runner=runner)
        self.quiet(teams.change, 'demo', notify=['payments'])  # by hand: two teams, one channel
        self.quiet(teams.team_channel, 'billing', None)
        self.assertEqual((channels.channels(self.root), self.routes()), ({'payments': 'ntfy', 'demo': 'ntfy'}, {'demo': ['payments']}),
                         'a channel another team notifies stays')
        self.quiet(teams.team_channel, 'demo', None)
        self.assertEqual((list(channels.channels(self.root)), self.routes()), (['demo'], {}))
        self.assertEqual(channels.problems(self.root), [])
        self.assertIsNotNone(first)
        for team, value, message in (('ghost', address, 'no team ghost'), ('billing', None, 'has no channel to remove'),
                                     ('billing', 'http://ntfy.sh/x', 'an HTTPS topic URL')):
            with self.subTest(team=team), self.assertRaisesRegex(EditError, message):
                teams.team_channel(self.root, team, value, runner=runner)
        with mock.patch('sys.stdin', io.StringIO(address + '\n')), redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
            self.assertEqual(teams.main_team_channel(['billing', '--root', str(self.root)], runner), 0)
            self.assertEqual(teams.main_team_channel(['billing', '--remove', '--root', str(self.root)], runner), 0)
        self.assertEqual(self.routes(), {})

    def test_the_address_is_read_from_standard_input(self):
        runner = self.notify_unit()
        with mock.patch('sys.stdin', io.StringIO('https://ntfy.sh/private-topic\n')), \
                redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            self.assertEqual(teams.main_channel_add(['payments-phone', '--root', str(self.root)], runner), 0)
            self.assertEqual(teams.main_edit(['payments', '--notify', 'payments-phone', '--root', str(self.root)]), 0)
            self.assertEqual(teams.main_channel_remove(['payments-phone', '--root', str(self.root)]), 2)
        self.assertNotIn('private-topic', out.getvalue() + err.getvalue())
        self.assertNotIn('private-topic', ' '.join(' '.join(call) for call in runner.calls))
        self.assertEqual(self.routes(), {'payments': ['payments-phone']})

    def test_an_app_naming_an_unregistered_team_is_found(self):
        path = self.root / 'apps/hello/staging/namespace.yaml'
        path.write_text(path.read_text().replace('team: demo', 'team: ghost'))
        self.assertEqual(teams.apps_of(self.root), {'ghost': ['hello'], 'demo': ['hello']})
        self.quiet(teams.add, 'ghost')  # registering it is one of the two ways out
        self.assertIn('ghost', self.registered())

    def test_commands_exit_2_on_refusal(self):
        err = io.StringIO()
        with redirect_stderr(err), redirect_stdout(io.StringIO()):
            self.assertEqual(teams.main_remove(['demo', '--root', str(self.root)]), 2)
            self.assertEqual(teams.main_add(['insights', '--description', 'Reporting', '--root', str(self.root)]), 0)
            self.assertEqual(teams.main_edit(['insights', '--rename', 'reports', '--root', str(self.root)]), 0)
        self.assertIn('[ERROR] team demo still owns hello', err.getvalue())
        self.assertIn('reports', self.registered())


if __name__ == '__main__':
    unittest.main()
