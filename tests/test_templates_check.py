"""make check-templates: every combination of a stack's questions, rendered, must pass app-new and the policy; offline."""
import io
import unittest
from pathlib import Path
from unittest import mock

from test_app_repo import COPIER_YML as ALL_QUESTIONS

from swhurl.apps import repo, templates_check
from swhurl.apps.contract import (
    STACK_REVISIONS,
)
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

COPIER_YML = ALL_QUESTIONS.split("api:\n")[0]  # kind and database: four combinations are enough here

WEB = 'version: 1\nkind: web\nport: 8080\nhealthPath: /healthz\nautoDeploy: true\n'
WORKER = 'version: 1\nkind: worker\nautoDeploy: true\n'


def template_runner(manifest):
    """A FakeRunner whose git clone writes COPIER_YML and whose copier writes ``manifest(answers)``."""
    runner = FakeRunner()

    def clone(args, _input):
        Path(args[-1]).mkdir(parents=True)
        (Path(args[-1]) / 'copier.yml').write_text(COPIER_YML)
        return Result(args, 0, '', '')

    def copier(args, _input):
        answers = dict(a.split('=', 1) for a in args if '=' in a and not a.startswith('app_name'))
        dest = Path(args[-1])
        dest.mkdir(parents=True)
        text = manifest(answers)
        if text is not None:
            (dest / 'swhurl.yaml').write_text(text)
        return Result(args, 0, '', '')

    return runner.on('git', 'clone', handler=clone).on('git', 'fetch').on('git', 'checkout').on('copier', 'copy', handler=copier)


def run(runner, problems=lambda path: []):
    out = io.StringIO()
    with mock.patch.object(repo, 'copier_command', return_value=['copier']), \
            mock.patch.object(templates_check.policy, 'evaluate', side_effect=lambda path, _runner: problems(path)):
        code = templates_check.check(runner, Report(out), {'typescript': 'samclement/t'}, types={})
    return code, out.getvalue()


class CheckTemplatesTests(unittest.TestCase):
    def test_a_combination_the_template_refuses_is_skipped_not_failed(self):
        refusal = ("Traceback (most recent call last):\n  raise ValueError(f\"Validation error for question '{self.var_name}'\")\n"
                   "ValueError: Validation error for question 'database': a worker cannot keep a database\n")
        runner = template_runner(lambda a: WORKER if a['kind'] == 'worker' else WEB)

        def copier(args, _input):
            if 'kind=worker' in args and 'database=sqlite' in args:
                return Result(args, 1, '', refusal)
            Path(args[-1]).mkdir(parents=True)
            (Path(args[-1]) / 'swhurl.yaml').write_text(WORKER if 'kind=worker' in args else WEB)
            return Result(args, 0, '', '')
        runner._rules[-1] = (('copier', 'copy'), copier)
        code, out = run(runner)
        self.assertEqual(code, 0, out)
        self.assertIn('[INFO] typescript kind=worker database=sqlite: the template refuses it (a worker cannot keep a database)', out)
        broken = template_runner(lambda a: WEB)
        broken._rules[-1] = (('copier', 'copy'), lambda args, _input: Result(args, 1, '', 'no such template'))
        code, out = run(broken)
        self.assertEqual(code, 1, 'any other copier failure still fails')

    def test_every_combination_becomes_a_staging_and_prod_instance(self):
        runner = template_runner(lambda a: (WORKER if a['kind'] == 'worker' else WEB) + f"# {a['database']}\n")
        code, out = run(runner)
        self.assertEqual(code, 0, out)
        for combo in ('kind=web database=none', 'kind=web database=sqlite', 'kind=worker database=none',
                      'kind=worker database=sqlite'):
            self.assertIn(f'[OK] typescript {combo}: staging and prod pass the app policy', out)

    def test_combinations_rendering_the_same_manifest_are_checked_once(self):
        checked = []
        runner = template_runner(lambda a: WORKER if a['kind'] == 'worker' else WEB)
        code, out = run(runner, problems=lambda path: checked.append(path.name) or [])
        self.assertEqual(code, 0, out)
        self.assertIn('[OK] typescript kind=web database=none (and 1 rendering the same swhurl.yaml): staging and prod pass', out)
        self.assertIn('[INFO] typescript: 4 combinations, 2 distinct swhurl.yaml checked', out)
        self.assertEqual(sorted(checked), ['prod', 'prod', 'staging', 'staging'], 'two manifests, each in both environments')
        renders = [c for c in runner.calls if c[:2] == ('copier', 'copy')]
        renders = [c for c in runner.calls if c[:2] == ('copier', 'copy')]
        self.assertEqual(len(renders), 4)
        self.assertTrue(all('--vcs-ref' not in c for c in renders), 'renders the pinned checkout as a plain folder')
        self.assertIn(('git', 'fetch', '--quiet', '--depth', '1', 'origin', STACK_REVISIONS['typescript']), runner.calls)
        self.assertEqual([c for c in runner.calls if c[:2] == ('git', 'clone')][0][-2],
                         'https://github.com/samclement/t.git', 'one clone, rendered locally four times')

    def test_a_refused_manifest_a_missing_one_or_a_policy_problem_fails(self):
        cases = (
            (lambda a: WEB + 'secrets: [OTEL_X]\n', 'reserved for the platform: OTEL_X'),
            (lambda a: None, 'rendered no swhurl.yaml'),
        )
        for manifest, expected in cases:
            with self.subTest(expected):
                code, out = run(template_runner(manifest))
                self.assertEqual(code, 1)
                self.assertIn(expected, out)
        code, out = run(template_runner(lambda a: WEB), problems=lambda path: ['no-root: runs as root'])
        self.assertEqual(code, 1)
        self.assertIn('prod: no-root: runs as root', out)

    def test_combinations_cover_every_choice(self):
        questions = repo.parse_questions(COPIER_YML)
        self.assertEqual(len(templates_check.combinations(questions)), 4)
        self.assertEqual(templates_check.combinations([]), [{}])


class StampTests(unittest.TestCase):
    """A full pass is remembered with a hash of what it read; any change to that renders again."""

    def test_a_pass_is_reused_until_a_module_a_read_file_or_a_tool_changes(self):
        import tempfile
        with tempfile.TemporaryDirectory() as tmp:
            root, stamp = Path(tmp) / 'repo', Path(tmp) / 'cache/check-templates.json'
            (root / 'tools').mkdir(parents=True)
            (root / 'tools/mod.py').write_text('A = 1\n')
            (root / 'apps').mkdir()
            (root / 'apps/teams.yaml').write_text('teams: {}\n')
            runner = FakeRunner().on('helm', stdout='v3.19.0').on('kubectl', stdout='v1.34')
            with mock.patch.object(templates_check, 'ROOT', root), \
                    mock.patch.object(templates_check, 'loaded_modules', return_value=['tools/mod.py']):
                self.assertEqual(templates_check.unchanged(runner, stamp), '', 'no stamp: render')
                templates_check.remember(runner, templates_check.dt.datetime(2026, 10, 18, 19, tzinfo=templates_check.dt.UTC), stamp)
                self.assertEqual(templates_check.unchanged(runner, stamp), '2026-10-18 19:00 UTC')
                (root / 'apps/teams.yaml').write_text('teams: {a: {}}\n')
                self.assertEqual(templates_check.unchanged(runner, stamp), '', 'a file it reads changed')
                templates_check.remember(runner, templates_check.dt.datetime(2026, 10, 18, 19, tzinfo=templates_check.dt.UTC), stamp)
                (root / 'tools/mod.py').write_text('A = 2\n')
                self.assertEqual(templates_check.unchanged(runner, stamp), '', 'a module it loaded changed')
                templates_check.remember(runner, templates_check.dt.datetime(2026, 10, 18, 19, tzinfo=templates_check.dt.UTC), stamp)
                newer = FakeRunner().on('helm', stdout='v3.20.0').on('kubectl', stdout='v1.34')
                self.assertEqual(templates_check.unchanged(newer, stamp), '', 'Helm changed')
                stamp.write_text('not json')
                self.assertEqual(templates_check.unchanged(runner, stamp), '', 'an unreadable stamp renders')

    def test_only_a_real_full_run_uses_the_stamp(self):
        with mock.patch.dict(templates_check.os.environ, {'FORCE': 'false'}), \
                mock.patch.object(templates_check, 'unchanged', return_value='earlier') as asked, \
                mock.patch.object(templates_check, 'check', return_value=0), \
                mock.patch.object(templates_check, 'remember') as kept:
            out = io.StringIO()
            self.assertEqual(templates_check.main([], runner=None, report=Report(out)), 0)
            self.assertIn('nothing the template check reads has changed since it passed at earlier', out.getvalue())
            templates_check.main(['--force'], report=Report(io.StringIO()))
            self.assertEqual(asked.call_count, 1, '--force does not ask')
            self.assertEqual(kept.call_count, 1, 'a forced pass is remembered')
            templates_check.main(['--stack', 'kotlin'], report=Report(io.StringIO()))
            templates_check.main([], runner=FakeRunner(), report=Report(io.StringIO()))
            self.assertEqual((asked.call_count, kept.call_count), (1, 1), 'one stack, or a test runner, never uses it')

    def test_the_check_reads_only_the_listed_repository_files(self):
        """READS was measured on a real run; a check that starts reading another file must list it."""
        import sys

        from swhurl import ROOT
        opened, watching = set(), [True]

        def hook(event, args):  # an audit hook cannot be removed: it stops recording when the test ends
            if watching[0] and event == 'open' and isinstance(args[0], str) and args[0].startswith(str(ROOT)):
                opened.add(str(Path(args[0]).relative_to(ROOT)))
        sys.addaudithook(hook)
        try:
            code, out = run(template_runner(lambda a: WEB))
        finally:
            watching[0] = False
        self.assertEqual(code, 0, out)
        read = {name for name in opened if not name.endswith(('.py', '.pyc')) and '__pycache__' not in name}
        self.assertTrue(read, 'the hook saw the check read the repository')
        self.assertLessEqual(read, set(templates_check.READS))
