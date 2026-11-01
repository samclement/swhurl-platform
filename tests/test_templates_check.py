"""make check-templates: each app type, rendered from its stack's template, must pass app-new and the policy; offline."""
import io
import unittest
from pathlib import Path
from unittest import mock

from test_app_repo import COPIER_YML as ALL_QUESTIONS

from swhurl.apps import repo, templates_check
from swhurl.apps.contract import (
    STACK_REVISIONS,
    AppType,
)
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

COPIER_YML = ALL_QUESTIONS

WEB = 'version: 1\nkind: web\nport: 8080\nhealthPath: /healthz\nautoDeploy: true\n'
WORKER = 'version: 1\nkind: worker\nautoDeploy: true\n'


REFUSAL = ("Traceback (most recent call last):\n  raise ValueError(f\"Validation error for question '{self.var_name}'\")\n"
           "ValueError: Validation error for question 'frontend': kind=worker database=sqlite is not an app type\n")
# Two types, as APP_TYPES would have them: the check renders each, and OUTSIDE_TYPES once.
TYPES = {'api': AppType('API', '', ('typescript',), {'kind': 'web', 'database': 'sqlite', 'api': 'openapi'}, 'private'),
         'worker': AppType('Worker', '', ('typescript', 'kotlin'), {'kind': 'worker', 'database': 'none', 'api': 'none'},
                           'private')}


def template_runner(manifest):
    """A FakeRunner whose git clone writes COPIER_YML and whose copier writes ``manifest(answers)``."""
    runner = FakeRunner()

    def clone(args, _input):
        Path(args[-1]).mkdir(parents=True)
        (Path(args[-1]) / 'copier.yml').write_text(COPIER_YML)
        return Result(args, 0, '', '')

    def copier(args, _input):
        answers = dict(a.split('=', 1) for a in args if '=' in a and not a.startswith('app_name'))
        if answers == templates_check.OUTSIDE_TYPES:
            return Result(args, 1, '', REFUSAL)
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
        code = templates_check.check(runner, Report(out), {'typescript': 'samclement/t'}, types=TYPES)
    return code, out.getvalue()


class CheckTemplatesTests(unittest.TestCase):
    def test_each_type_becomes_a_staging_instance(self):
        checked = []
        runner = template_runner(lambda a: WORKER if a['kind'] == 'worker' else WEB)
        code, out = run(runner, problems=lambda path: checked.append(path.name) or [])
        self.assertEqual(code, 0, out)
        self.assertIn('[OK] typescript api=openapi database=sqlite kind=web (api): staging passes the app policy', out)
        self.assertIn('[OK] typescript api=none database=none kind=worker (worker): staging passes the app policy', out)
        self.assertEqual(sorted(checked), ['staging', 'staging'], 'each type in staging; production is v2\'s create path')
        renders = [c for c in runner.calls if c[:2] == ('copier', 'copy')]
        self.assertEqual(len(renders), 3, 'two types and the answers no type has')
        self.assertTrue(all('--vcs-ref' not in c for c in renders), 'renders the pinned checkout as a plain folder')
        self.assertIn(('git', 'fetch', '--quiet', '--depth', '1', 'origin', STACK_REVISIONS['typescript']), runner.calls)
        self.assertEqual([c for c in runner.calls if c[:2] == ('git', 'clone')][0][-2],
                         'https://github.com/samclement/t.git', 'one clone, rendered locally')

    def test_the_template_must_refuse_answers_no_type_has(self):
        code, out = run(template_runner(lambda a: WORKER if a['kind'] == 'worker' else WEB))
        self.assertIn('[OK] typescript database=sqlite kind=worker: the template refuses it (kind=worker database=sqlite is not '
                      'an app type)', out)
        accepting = template_runner(lambda a: WORKER if a['kind'] == 'worker' else WEB)

        def copier(args, _input):
            Path(args[-1]).mkdir(parents=True)
            (Path(args[-1]) / 'swhurl.yaml').write_text(WORKER if 'kind=worker' in args else WEB)
            return Result(args, 0, '', '')
        accepting._rules[-1] = (('copier', 'copy'), copier)
        code, out = run(accepting)
        self.assertEqual(code, 1)
        self.assertIn('database=sqlite kind=worker: no app type has these answers, but the template accepts them', out)

    def test_a_type_the_template_refuses_or_no_longer_offers_fails(self):
        refusing = template_runner(lambda a: WEB)
        refusing._rules[-1] = (('copier', 'copy'), lambda args, _input: Result(args, 1, '', REFUSAL))
        code, out = run(refusing)
        self.assertEqual(code, 1)
        self.assertIn('(api): the template refuses it', out)
        with mock.patch.object(templates_check, 'type_answers', return_value={'kind': 'cron'}):
            code, out = run(template_runner(lambda a: WEB))
        self.assertEqual(code, 1)
        self.assertIn('typescript: the API type (api) no longer fits the template: kind must be one of web, worker, job', out)

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
        self.assertIn('staging: no-root: runs as root', out)
        broken = template_runner(lambda a: WEB)
        broken._rules[-1] = (('copier', 'copy'), lambda args, _input: Result(args, 1, '', 'no such template'))
        code, out = run(broken)
        self.assertEqual(code, 1, 'any other copier failure fails')
        self.assertIn('the template failed otherwise', out)
