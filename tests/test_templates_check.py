"""make check-templates: each app type, rendered from its stack's template, must produce a label v2's creation
rules accept; offline."""
import io
import unittest
from pathlib import Path
from unittest import mock

from test_app_repo import COPIER_YML as ALL_QUESTIONS

from swhurl.apps import image_declaration, repo, templates_check
from swhurl.apps.contract import (
    STACK_REVISIONS,
    AppType,
)
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

COPIER_YML = ALL_QUESTIONS

WEB = 'version: 1\nkind: web\nport: 8080\nhealthPath: /healthz\nautoDeploy: true\n'
WORKER = 'version: 1\nkind: worker\nautoDeploy: true\n'

LABEL = 'platform.swhurl.com/declaration={"schemaVersion":2,"runtime":{"kind":"web","uid":65532},' \
        '"telemetry":true,"memoryMiB":128}'
REFUSAL = ("Traceback (most recent call last):\n  raise ValueError(f\"Validation error for question '{self.var_name}'\")\n"
           "ValueError: Validation error for question 'frontend': kind=worker database=sqlite is not an app type\n")
# Two types, as APP_TYPES would have them: the check renders each, and OUTSIDE_TYPES once.
TYPES = {'api': AppType('API', '', ('typescript',), {'kind': 'web', 'database': 'sqlite', 'api': 'openapi'}, 'private'),
         'worker': AppType('Worker', '', ('typescript', 'kotlin'), {'kind': 'worker', 'database': 'none', 'api': 'none'},
                           'private')}


def template_runner(manifest, declaration=None):
    """A FakeRunner whose git clone writes COPIER_YML, whose copier writes ``manifest(answers)``, and whose
    swhurl-v2 declaration answers ``declaration`` when given."""
    runner = FakeRunner()
    # Registered first so a test replacing the last rule still replaces the copier.
    runner.on('swhurl-v2', 'declaration', handler=declaration or (lambda args, _input: Result(args, 0, '', '')))

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

    runner.on('git', 'clone', handler=clone).on('git', 'fetch').on('git', 'checkout').on('copier', 'copy', handler=copier)
    return runner


def run(runner, label=None):
    """check() with the producer's label stubbed: these tests are about the check's shape, not the producer's
    (tests/test_image_declaration.py is)."""
    out = io.StringIO()
    with mock.patch.object(repo, 'copier_command', return_value=['copier']), \
            mock.patch.object(templates_check, 'binary', return_value='swhurl-v2'), \
            mock.patch.object(templates_check, 'declaration_label', side_effect=label or (lambda _rendered: LABEL)):
        code = templates_check.check(runner, Report(out), {'typescript': 'samclement/t'}, types=TYPES)
    return code, out.getvalue()


class CheckTemplatesTests(unittest.TestCase):
    def test_each_type_produces_a_label_the_creation_rules_accept(self):
        runner = template_runner(lambda a: WORKER if a['kind'] == 'worker' else WEB)
        code, out = run(runner)
        self.assertEqual(code, 0, out)
        self.assertIn('[OK] typescript api=openapi database=sqlite kind=web (api): the label applies to a new instance', out)
        self.assertIn('[OK] typescript api=none database=none kind=worker (worker): the label applies to a new instance', out)
        renders = [c for c in runner.calls if c[:2] == ('copier', 'copy')]
        self.assertEqual(len(renders), 3, 'two types and the answers no type has')
        self.assertTrue(all('--vcs-ref' not in c for c in renders), 'renders the pinned checkout as a plain folder')
        self.assertIn(('git', 'fetch', '--quiet', '--depth', '1', 'origin', STACK_REVISIONS['typescript']), runner.calls)
        self.assertEqual([c for c in runner.calls if c[:2] == ('git', 'clone')][0][-2],
                         'https://github.com/samclement/t.git', 'one clone, rendered locally')
        declarations = [c for c in runner.calls if c[:2] == ('swhurl-v2', 'declaration')]
        self.assertEqual(len(declarations), 2, 'one per type: the outside answers never reach the producer')
        self.assertIn('--team', declarations[0])
        self.assertNotIn('--out', declarations[0], 'the check writes nothing: the report is stdout only')

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

    def test_a_producer_or_creation_refusal_fails(self):
        def refuses(_rendered):
            raise image_declaration.LabelError('swhurl.yaml: secrets names a reserved variable')
        code, out = run(template_runner(lambda a: WEB), label=refuses)
        self.assertEqual(code, 1)
        self.assertIn('the image-declaration producer refused the app: swhurl.yaml', out)
        refusing = template_runner(lambda a: WEB, declaration=lambda args, _input: Result(args, 2, '', ''))
        code, out = run(refusing)
        self.assertEqual(code, 1)
        self.assertIn('swhurl-v2 declaration refused the label', out)
        gap = template_runner(lambda a: WEB, declaration=lambda args, _input: Result(
            args, 2, '', 'unsupported: runtime.kind: only web and worker releases are supported\n'))
        code, out = run(gap)
        self.assertEqual(code, 0, 'a kind v2 does not model yet is a reported gap, not a template failure')
        self.assertIn('[WARN]', out)
        self.assertIn('v2 does not model it yet', out)
        broken = template_runner(lambda a: WEB)
        broken._rules[-1] = (('copier', 'copy'), lambda args, _input: Result(args, 1, '', 'no such template'))
        code, out = run(broken)
        self.assertEqual(code, 1, 'any other copier failure fails')
        self.assertIn('the template failed otherwise', out)

    def test_a_missing_manifest_fails(self):
        code, out = run(template_runner(lambda a: None))
        self.assertEqual(code, 1)
        self.assertIn('rendered no swhurl.yaml', out)
