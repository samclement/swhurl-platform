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
from swhurl.apps import channels, contract, teams
from swhurl.apps.errors import EditError
from swhurl.apps.model import write
from swhurl.run import FakeRunner, Result

SAMPLE = ROOT / 'tests/fixtures/instance'  # one v1 app, hello, in staging and prod; v1's renderer reads it


class TeamTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        for rel in ('apps', 'clusters'):
            shutil.copytree(SAMPLE / rel, self.root / rel)
        (self.root / 'apps/teams.yaml').write_text(
            '# Teams that own apps.\nteams:\n  demo:\n    description: The platform itself\n  payments: {}\n')
        # v2's records are where a team is stated: hello belongs to demo.
        self.record('hello/app.json', {'schemaVersion': 1, 'application': 'hello', 'team': 'demo'})

    def record(self, rel, document):
        path = self.root / 'v2-records' / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(document, indent=2))

    def record_of(self, rel='hello/app.json'):
        return json.loads((self.root / 'v2-records' / rel).read_text())

    def quiet(self, fn, *args, **kwargs):
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()):
            fn(self.root, *args, **kwargs)
        return out.getvalue()

    def registered(self):
        return contract.teams(self.root)

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

    def test_a_rename_is_refused_while_a_record_names_the_team(self):
        self.quiet(teams.remove, 'payments')
        self.quiet(teams.add, 'core')
        self.quiet(teams.remove, 'core')  # round trip of an empty team
        (self.root / 'apps/teams.yaml').write_text('teams:\n  demo: {}\n  legacy: {description: Old}\n  payments: {}\n')
        before = (self.root / 'apps/teams.yaml').read_text()
        self.record('hello/app.json', {'schemaVersion': 1, 'application': 'hello', 'team': 'legacy'})
        with self.assertRaisesRegex(EditError, 'team legacy is named by hello in v2-records and a rename moves nothing: '
                                               'move them first with swhurl-v2 edit --set team=orders'):
            teams.change(self.root, 'legacy', rename='orders')
        self.assertEqual((self.root / 'apps/teams.yaml').read_text(), before, 'a refused rename changes nothing')
        self.record('hello/app.json', {'schemaVersion': 1, 'application': 'hello', 'team': 'demo'})
        out = self.quiet(teams.change, 'legacy', rename='orders')
        self.assertIn('renamed to orders', out)
        self.assertEqual(list(self.registered()), ['demo', 'orders', 'payments'], 'it keeps its place and description')
        self.assertEqual(self.registered()['orders'], 'Old')
        self.assertEqual(self.record_of()['team'], 'demo', 'a rename relabels nothing: the record is v2\'s to write')
        with self.assertRaisesRegex(EditError, 'already exists; a rename cannot merge two teams: move the apps with '
                                               'swhurl-v2 edit --set team=payments'):
            teams.change(self.root, 'orders', rename='payments')
        self.quiet(teams.change, 'payments', rename='core')  # no team is special: one nothing names can be renamed
        self.assertEqual(list(self.registered()), ['demo', 'orders', 'core'])

    def test_a_team_a_record_or_a_grant_names_is_not_removed(self):
        self.quiet(teams.add, 'legacy')
        before = (self.root / 'apps/teams.yaml').read_text()
        with self.assertRaisesRegex(EditError, 'team demo is named by hello in v2-records: move them first with '
                                               'swhurl-v2 edit --set team=<another team>, then remove it'):
            teams.remove(self.root, 'demo')
        self.assertEqual((self.root / 'apps/teams.yaml').read_text(), before, 'a refused removal changes nothing')
        self.record('hello/app.json', {'schemaVersion': 1, 'application': 'hello', 'team': 'legacy'})
        self.set_allows(['team:demo', 'team:payments'])
        with self.assertRaisesRegex(EditError, 'team:demo is allowed to call hello/staging: remove those grants first'):
            teams.remove(self.root, 'demo')
        self.set_allows(['team:payments'])
        self.quiet(teams.remove, 'demo')  # no team is special: one nothing names can go
        self.assertEqual(list(self.registered()), ['payments', 'legacy'])

    def test_a_record_that_cannot_be_read_refuses_rather_than_passing(self):
        (self.root / 'v2-records/hello/app.json').write_text('{')
        with self.assertRaisesRegex(EditError, 'cannot be read'):
            teams.remove(self.root, 'demo')
        self.assertIn('demo', self.registered())

    def set_allows(self, names, env='staging'):
        self.record(f'hello/{env}/instance.json', {'schemaVersion': 1, 'application': 'hello', 'environment': env,
                                                   'allows': names})

    def notify_unit(self):
        directory = self.root / channels.NOTIFY_DIR
        directory.mkdir(parents=True)
        (directory / 'kustomization.yaml').write_text('# Channels.\nresources:\n  - namespace.yaml\n  - routes.yaml\n')
        write.sync(self.root)
        (self.root / channels.UNIT_FILE).write_text(
            '# The notify unit.\nspec:\n  path: ./platform/notify\n  prune: true\n')

        def encrypt(args, text):
            document = yaml.safe_load(text)
            document['stringData'] = {key: 'ENC[...]' for key in document['stringData']}
            return Result(args, 0, yaml.safe_dump(document) + 'sops:\n  version: 3\n')
        return FakeRunner().on('sops', '--encrypt', handler=encrypt)

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

    def snapshot(self):
        return {path.relative_to(self.root): path.read_bytes() for path in self.root.rglob('*') if path.is_file()}

    def test_team_and_channel_planning_failures_leave_every_input_and_output_untouched(self):
        runner = self.notify_unit()
        record = self.root / 'apps/hello/staging/release.yaml'
        record.write_text('invalid: [')
        for action in (lambda: teams.change(self.root, 'demo', rename='core'),
                       lambda: teams.remove(self.root, 'demo'),
                       lambda: teams.team_channel(self.root, 'demo', 'https://ntfy.sh/private', runner=runner)):
            before = self.snapshot()
            with self.assertRaises(EditError):
                action()
            self.assertEqual(self.snapshot(), before)
        self.assertEqual(runner.calls, [], 'planning refused before encryption')

    def test_replacing_a_channel_address_keeps_the_registry_bytes(self):
        runner = self.notify_unit()
        self.quiet(teams.team_channel, 'demo', 'https://ntfy.sh/first', runner=runner)
        registry = self.root / 'apps/teams.yaml'
        registry.write_text('teams:\n  demo: {notify: [demo]} # keep this style\n  payments: {}\n')
        before = registry.read_bytes()
        self.quiet(teams.team_channel, 'demo', 'https://ntfy.sh/second', runner=runner)
        self.assertEqual(registry.read_bytes(), before)

    def test_team_channel_removal_plans_both_registry_and_channel_before_writing(self):
        runner = self.notify_unit()
        self.quiet(teams.team_channel, 'demo', 'https://ntfy.sh/private', runner=runner)
        (self.root / channels.NOTIFY_DIR / 'kustomization.yaml').write_text('resources: wrong\n')
        before = self.snapshot()
        with self.assertRaisesRegex(EditError, 'expected a resources list'):
            teams.team_channel(self.root, 'demo', None)
        self.assertEqual(self.snapshot(), before, 'the team must not lose its channel on a refused second step')

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
        write.sync(self.root)
        self.assertEqual(channels.problems(self.root), ['notify-channels: team payments notifies 2 channels; a team has one '
                                                        '(make team-edit NAME=payments ARGS="--notify <channel>")'])
        registry.write_text(one)
        write.sync(self.root)
        with self.assertRaisesRegex(EditError, 'channel payments-phone is notified by payments'):
            teams.channel_remove(self.root, 'payments-phone')
        self.set_allows(['team:payments'])
        with self.assertRaisesRegex(EditError, 'team:payments is allowed to call hello/staging, so a rename would leave '
                                               'that grant naming a team that is gone'):
            teams.change(self.root, 'payments', rename='billing')
        self.set_allows([])
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
        self.set_allows([])
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
        self.record('hello/app.json', {'schemaVersion': 1, 'application': 'hello', 'team': 'ghost'})
        self.assertNotIn('ghost', self.registered())
        self.quiet(teams.add, 'ghost')  # registering it is one of the two ways out
        self.assertIn('ghost', self.registered())
        with self.assertRaisesRegex(EditError, 'team ghost is named by hello'):
            teams.remove(self.root, 'ghost')

    def test_commands_exit_2_on_refusal(self):
        err = io.StringIO()
        with redirect_stderr(err), redirect_stdout(io.StringIO()):
            self.assertEqual(teams.main_remove(['demo', '--root', str(self.root)]), 2)
            self.assertEqual(teams.main_add(['insights', '--description', 'Reporting', '--root', str(self.root)]), 0)
            self.assertEqual(teams.main_edit(['insights', '--rename', 'reports', '--root', str(self.root)]), 0)
        self.assertIn('[ERROR] team demo is named by hello', err.getvalue())
        self.assertIn('reports', self.registered())


if __name__ == '__main__':
    unittest.main()
