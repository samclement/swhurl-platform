"""Each target is stated once, in the Makefile's ``##`` lines: its targets, make help and docs/commands.md follow them."""
import re
import subprocess
import unittest
from dataclasses import dataclass
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]


# What a target touches (the Touches column of docs/commands.md).
TOUCHES = ('Local', 'Git', 'Cluster (read)', 'Cluster', 'Cluster (throwaway)', 'GitHub', 'Host')
# What DRY_RUN=true does: print the plan and touch nothing (test_command_safety runs these), or read without changing.
DRY_RUNS = ('-', 'plan', 'reads')
# The verb rule: a name starting with one of these touches exactly this.
VERBS = {'check': 'Local', 'verify-': 'Cluster (read)'}


@dataclass(frozen=True)
class Target:
    name: str
    touches: str
    dry_run: str
    help: str
    variables: str  # as help shows them: NAME=<shows>, optional ones in []

    def names(self) -> dict[str, bool]:
        """``{variable: required}``."""
        return {name: not optional for optional, name in re.findall(r'(\[?)([A-Z][A-Z_]*)=', self.variables)}


def targets() -> dict[str, Target]:
    """The Makefile's ``## <target> | <touches> | <dry run> | <help>[ | <variables>]`` lines, in order."""
    found = {}
    for line in (ROOT / 'Makefile').read_text().splitlines():
        if line.startswith('## '):
            fields = line[3:].split(' | ')
            if len(fields) not in (4, 5):
                raise AssertionError(f'a target line has four or five fields: {line[:80]}')
            found[fields[0]] = Target(*fields, *([''] if len(fields) == 4 else []))
    return found


def plans() -> list[str]:
    """Every target whose DRY_RUN=true only prints a plan and that needs no variable."""
    return [name for name, target in targets().items()
            if target.dry_run == 'plan' and not any(target.names().values())]


def phony() -> set[str]:
    """What make itself reads from the Makefile as .PHONY."""
    out = subprocess.run(['make', '-pnq', 'help'], cwd=ROOT, capture_output=True, text=True).stdout
    return {word for words in re.findall(r'^\.PHONY: (.+)$', out, re.M) for word in words.split()}


def doc_rows():
    """``[(cells, line)]`` of every table row in docs/commands.md."""
    rows = []
    for line in (ROOT / 'docs/commands.md').read_text().splitlines():
        cells = [cell.strip() for cell in re.split(r'(?<!\\)\|', line.strip().strip('|'))]
        if line.startswith('| `'):
            rows.append((cells, line))
    return rows


class SpecTests(unittest.TestCase):
    def test_the_makefile_has_exactly_the_targets_stated(self):
        self.assertEqual(phony(), set(targets()))

    def test_every_target_says_what_it_touches_and_what_its_dry_run_does(self):
        for name, target in targets().items():
            with self.subTest(target=name):
                self.assertIn(target.touches, TOUCHES)
                self.assertIn(target.dry_run, DRY_RUNS)
                self.assertTrue(target.help)

    def test_the_verb_says_what_a_target_touches(self):
        for name, target in targets().items():
            for verb, touches in VERBS.items():
                if name.startswith(verb):
                    with self.subTest(target=name):
                        self.assertEqual(target.touches, touches)

    def test_the_v1_instance_writer_commands_stay_deleted(self):
        removed = ('app-scale', 'app-expose', 'app-calls', 'app-allows', 'app-events', 'app-team', 'app-remove',
                   'app-deploy', 'app-promote', 'app-sync', 'app-check', 'app-offers', 'app-egress', 'app-alerts',
                   'app-schedule', 'app-secret', 'platform-signing-keys', 'bot-deploy', 'image-declaration',
                   'notify-serve', 'clickstack-notify-webhook', 'clickstack-ntfy-webhook')
        for name in removed:
            with self.subTest(command=name):
                self.assertNotIn(name, targets())
                self.assertNotIn(name, phony())

    def test_the_notify_service_is_go(self):
        manifest = yaml.safe_load((ROOT / 'platform/notify/helmrelease.yaml').read_text())
        main = manifest['spec']['values']['controllers']['main']['containers']['main']
        self.assertEqual(main['command'], ['swhurl-v2', 'notify-serve'])

    def test_make_help_lists_every_target_with_its_variables(self):
        result = subprocess.run(['make', '--no-print-directory', 'help'], cwd=ROOT, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        for name, target in targets().items():
            with self.subTest(target=name):
                mark = '' if target.dry_run == '-' else ' †'
                self.assertRegex(result.stdout, rf'(?m)^  {re.escape(name + mark)} +{re.escape(target.touches)} +'
                                                rf'{re.escape(target.help)}$')
                if target.variables:
                    self.assertRegex(result.stdout, rf'(?m)^ +{re.escape(target.variables)}$')

    def test_make_help_pads_by_characters_and_lists_the_sections_in_order(self):
        lines = subprocess.run(['make', '--no-print-directory', 'help'], cwd=ROOT, capture_output=True,
                               text=True).stdout.splitlines()
        sections = re.findall(r'(?m)^##@ (.+)$', (ROOT / 'Makefile').read_text())
        self.assertEqual([line[:-1] for line in lines[1:] if line.endswith(':') and not line.startswith(' ')], sections)
        columns = {line.index(touches) for line in lines for touches in TOUCHES if f'  {touches}  ' in line}
        self.assertEqual(len(columns), 1, 'the Touches column starts at one character position')

    def test_more_than_a_handful_of_targets_only_plan(self):
        self.assertGreater(len(plans()), 11)


class DocTests(unittest.TestCase):
    """docs/commands.md says more about a target than its help line, but not something else."""

    def test_every_row_matches_its_target(self):
        stated = targets()
        seen = set()
        for cells, line in doc_rows():
            names = [span.split()[0] for span in re.findall(r'`([^`]+)`', cells[0]) if span.split()[0] in stated]
            self.assertTrue(names, f'no target in: {line[:80]}')
            self.assertEqual(len(cells), 3, f'a row is target, does, touches: {line[:80]}')
            for name in names:
                target = stated[name]
                with self.subTest(target=name):
                    self.assertEqual(set(re.findall(r'\b([A-Z][A-Z_]*)=', cells[0])), set(target.names()),
                                     'the variables in the first cell')
                    for var, required in target.names().items():
                        self.assertEqual(f'[{var}=' in cells[0], not required, f'{var} is optional exactly when in []')
                    self.assertEqual('†' in cells[0], target.dry_run != '-', 'the † mark')
                    touches = max((label for label in TOUCHES if cells[2].startswith(label)), key=len, default='')
                    self.assertEqual(touches, target.touches, 'the start of Touches')
            seen.update(names)
        self.assertEqual(seen, set(stated))


class MakeTests(unittest.TestCase):
    def test_a_missing_variable_is_refused_with_the_usage_line(self):
        # destroy-data is a recipe over swhurl-v2, which refuses a missing TARGET before building anything.
        result = subprocess.run(['make', '--no-print-directory', 'destroy-data', 'TARGET=', 'CONFIRM='], cwd=ROOT,
                                capture_output=True, text=True)
        self.assertEqual((result.returncode, result.stdout), (2, ''))
        self.assertIn('Usage: make destroy-data TARGET=pvc/<ns>/<name>|pv/<name> CONFIRM=<TARGET>', result.stderr)

    def test_dry_run_is_refused_where_the_target_has_no_dry_run(self):
        result = subprocess.run(['make', '--no-print-directory', 'check-secrets', 'DRY_RUN=true'], cwd=ROOT,
                                capture_output=True, text=True)
        self.assertEqual((result.returncode, result.stdout), (2, ''))
        self.assertIn('make check-secrets has no dry run', result.stderr)

    def test_the_console_is_v2s_and_the_makefile_runs_it(self):
        recipe = re.search(r'^console-dev:\n((?:\t.*\n)+)', (ROOT / 'Makefile').read_text(), re.M)
        self.assertIsNotNone(recipe, 'the Makefile states console-dev')
        self.assertIn('swhurl-v2" console --dev', recipe.group(1))


if __name__ == '__main__':
    unittest.main()
