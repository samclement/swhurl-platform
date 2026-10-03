"""make check-templates: every combination of a stack's questions, rendered, must pass app-new and the policy; offline."""
import io
import unittest
from pathlib import Path
from unittest import mock

from test_app_repo import COPIER_YML

from swhurl.apps import repo, templates_check
from swhurl.apps.contract import STACK_REVISIONS
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

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
        code = templates_check.check(runner, Report(out), {'typescript': 'samclement/t'})
    return code, out.getvalue()


class CheckTemplatesTests(unittest.TestCase):
    def test_every_combination_becomes_a_staging_and_prod_instance(self):
        runner = template_runner(lambda a: WORKER if a['kind'] == 'worker' else WEB)
        code, out = run(runner)
        self.assertEqual(code, 0, out)
        for combo in ('kind=web database=none', 'kind=web database=sqlite', 'kind=worker database=none',
                      'kind=worker database=sqlite'):
            self.assertIn(f'[OK] typescript {combo}: staging and prod pass the app policy', out)
        renders = [c for c in runner.calls if c[:2] == ('copier', 'copy')]
        self.assertEqual(len(renders), 4)
        self.assertTrue(all(c[c.index('--vcs-ref') + 1] == STACK_REVISIONS['typescript'] for c in renders))
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
        self.assertIn('production policy refused: no-root: runs as root', out)

    def test_combinations_cover_every_choice(self):
        questions = repo.parse_questions(COPIER_YML)
        self.assertEqual(len(templates_check.combinations(questions)), 4)
        self.assertEqual(templates_check.combinations([]), [{}])
