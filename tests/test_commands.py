"""Each command is stated once (swhurl/commands.py): the Makefile, make help, usage and docs/commands.md follow it."""
import importlib
import io
import re
import subprocess
import unittest
from contextlib import redirect_stderr, redirect_stdout

from swhurl import ROOT, __main__, commands
from swhurl.commands import COMMANDS, TARGETS, Command, Var
from swhurl.run import CommandError


def database():
    """What make itself reads from the Makefile: ``{name: words}`` for its simple variables and .PHONY."""
    out = subprocess.run(['make', '-pnq', 'help'], cwd=ROOT, capture_output=True, text=True).stdout
    return {name: set(words.split()) for name, words in re.findall(r'^(\.PHONY|COMMANDS)(?: :=|:) (.+)$', out, re.M)}


def doc_rows():
    """``[(first cell, last cell)]`` of every table row in docs/commands.md."""
    rows = []
    for line in (ROOT / 'docs/commands.md').read_text().splitlines():
        cells = [cell.strip() for cell in re.split(r'(?<!\\)\|', line.strip().strip('|'))]
        if line.startswith('| `'):
            rows.append((cells, line))
    return rows


def run_make(target, env):
    out, err = io.StringIO(), io.StringIO()
    executed = []
    with redirect_stdout(out), redirect_stderr(err):
        code = commands.make([target], env, execute=lambda program, argv: executed.append((program, argv)))
    return code, out.getvalue(), err.getvalue(), executed


class SpecTests(unittest.TestCase):
    def test_the_makefile_has_exactly_the_targets_stated(self):
        make = database()
        self.assertEqual(make['.PHONY'], set(TARGETS))
        self.assertEqual(make['COMMANDS'], {name for name, command in TARGETS.items() if command.run},
                         'COMMANDS in the Makefile is every target with a function; the others have a recipe')

    def test_every_function_exists(self):
        for name, command in COMMANDS.items():
            if command.run:
                with self.subTest(command=name):
                    module, function = command.run.split(':')
                    self.assertTrue(callable(getattr(importlib.import_module(module), function)))

    def test_every_target_says_what_it_touches(self):
        for name, command in TARGETS.items():
            with self.subTest(target=name):
                self.assertIn(command.touches, commands.TOUCHES)
                self.assertIn(command.dry_run, ('', commands.PLAN, commands.READS))

    def test_the_verb_says_what_a_command_touches(self):
        for name, command in TARGETS.items():
            for verb, touches in commands.VERBS.items():
                if name.startswith(verb):
                    with self.subTest(target=name):
                        self.assertEqual(command.touches, touches)

    def test_help_lists_every_target_with_its_variables(self):
        text = commands.help_text()
        for name, command in TARGETS.items():
            with self.subTest(target=name):
                self.assertRegex(text, rf'(?m)^  {re.escape(name)}{" †" if command.dry_run else ""} +{re.escape(command.touches)} ')
                for var in command.variables:
                    self.assertIn(var.signature(), text)

    def test_make_help_is_that_list(self):
        result = subprocess.run(['make', '--no-print-directory', 'help'], cwd=ROOT, capture_output=True, text=True)
        self.assertEqual((result.returncode, result.stdout.strip()), (0, commands.help_text()))


class DocTests(unittest.TestCase):
    """docs/commands.md says more about a target than its help line, but not something else."""

    def test_every_row_matches_its_command(self):
        seen = set()
        for cells, line in doc_rows():
            names = [span.split()[0] for span in re.findall(r'`([^`]+)`', cells[0]) if span.split()[0] in TARGETS]
            self.assertTrue(names, f'no target in: {line[:80]}')
            self.assertEqual(len(cells), 3, f'a row is target, does, touches: {line[:80]}')
            for name in names:
                command = TARGETS[name]
                with self.subTest(target=name):
                    self.assertEqual(set(re.findall(r'\b([A-Z][A-Z_]*)=', cells[0])), {var.name for var in command.variables},
                                     'the variables in the first cell')
                    for var in command.variables:
                        self.assertEqual(f'[{var.name}=' in cells[0], not var.required, f'{var.name} is optional exactly when in []')
                    self.assertEqual('†' in cells[0], bool(command.dry_run), 'the † mark')
                    touches = max((label for label in commands.TOUCHES if cells[2].startswith(label)), key=len, default='')
                    self.assertEqual(touches, command.touches, 'the start of Touches')
            seen.update(names)
        self.assertEqual(seen, set(TARGETS))


class MakeTests(unittest.TestCase):
    def test_a_missing_variable_is_refused_with_the_usage_line(self):
        for target, env, usage in (
                ('app-scale', {'APP': 'web', 'ENV': 'prod'}, 'Usage: make app-scale APP=<app> ENV=<env> ARGS="--replicas N'),
                ('reconcile', {'UNIT': ''}, 'Usage: make reconcile UNIT=<name>'),
                ('team-remove', {}, 'Usage: make team-remove NAME=<team> [ARGS="--move-apps-to OTHER"]')):
            with self.subTest(target=target):
                code, out, err, executed = run_make(target, env)
                self.assertEqual((code, out, executed), (2, '', []))
                self.assertIn(usage, err)

    def test_dry_run_is_refused_where_the_command_has_no_dry_run(self):
        code, out, err, executed = run_make('app-remove', {'APP': 'web', 'ENV': 'staging', 'DRY_RUN': 'true'})
        self.assertEqual((code, out, executed), (2, '', []))
        self.assertIn('make app-remove has no dry run', err)
        code, _, _, executed = run_make('notifications-check', {'DRY_RUN': 'false'})
        self.assertEqual(len(executed), 1, 'DRY_RUN=false runs it')

    def test_a_variable_the_command_reads_itself_is_left_to_it(self):
        self.assertEqual(TARGETS['restore-sqlite'].missing({'APP': 'notes', 'ENV': 'staging'}), [])
        self.assertEqual(TARGETS['restore-sqlite'].arguments({'APP': 'notes', 'ENV': 'staging', 'CONFIRM': 'notes/staging'}),
                         ['notes', 'staging'])

    def test_variables_become_arguments(self):
        for target, env, expected in (
                ('app-scale', {'APP': 'web', 'ENV': 'prod', 'ARGS': '--replicas 2 --cpu 100m'}, ['web', 'prod', '--replicas', '2', '--cpu', '100m']),
                ('team-edit', {'NAME': 'a', 'ARGS': '--notify ""'}, ['a', '--notify', '']),
                ('team-add', {'NAME': 'a', 'DESCRIPTION': 'what it owns'}, ['a', '--description', 'what it owns']),
                ('team-add', {'NAME': 'a'}, ['a']),
                ('app-offers', {'ARGS': '--all staging'}, ['--all', 'staging']),
                ('verify-logs', {}, ['--minutes', '15']),
                ('verify-logs', {'MINUTES': '60'}, ['--minutes', '60']),
                ('notifications-check', {}, ['--dry-run']),
                ('console-dev', {'PORT': '9000'}, ['--dev', '--port', '9000'])):
            with self.subTest(target=target, env=env):
                self.assertEqual(TARGETS[target].arguments(env), expected)

    def test_a_command_that_needs_the_locked_dependencies_runs_under_uv(self):
        code, _, _, executed = run_make('notifications-check', {})
        self.assertEqual(executed, [('uv', ['uv', 'run', '--frozen', 'python', '-m', 'swhurl', 'notifications-check', '--dry-run'])])

    def test_a_shared_function_gets_its_prefix_from_make_and_from_the_command_line(self):
        seen = []
        command = Command('x', 'help', run=f'{__name__}:record', prefix=('status',), takes=(Var('APP', '<app>'),))
        record.calls = seen
        self.assertEqual(commands.call(command, command.arguments({'APP': 'web'})), 0)
        self.assertEqual(seen, [['status', 'web']])

    def test_only_a_target_with_a_function_runs_through_make(self):
        for target in ('flux-reconcile', 'console-merge', 'nothing'):
            with self.subTest(target=target):
                code, _, err, _ = run_make(target, {})
                self.assertEqual(code, 2)
                self.assertIn('usage: python3 -m swhurl make <target>', err)

    def test_an_unhandled_failed_command_is_one_line(self):
        err = io.StringIO()
        with redirect_stderr(err):
            code = commands.call(Command('x', 'help', run=f'{__name__}:fail'), [])
        self.assertEqual((code, err.getvalue()), (1, '[ERROR] kubectl get x exited 1\n'))

    def test_the_entry_point_lists_and_refuses(self):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            self.assertEqual(__main__.main(['--help']), 0)
            self.assertEqual(__main__.main(['flux-reconcile']), 2, 'a recipe in the Makefile is not a swhurl command')
        self.assertIn('  app-new', out.getvalue())
        self.assertNotIn('  flux-reconcile ', out.getvalue())
        self.assertIn('unknown command: flux-reconcile', err.getvalue())


def record(argv):
    record.calls.append(argv)
    return 0


def fail(argv):
    raise CommandError(['kubectl', 'get', 'x'], 'kubectl get x exited 1', 1)


if __name__ == '__main__':
    unittest.main()
