"""bin/agent-cli: the offline parts an agent relies on (doc sections, make output, targets, transcripts, misuse)."""
import importlib.machinery
import importlib.util
import io
import json
import unittest
from contextlib import redirect_stdout

from swhurl import ROOT

loader = importlib.machinery.SourceFileLoader('agent_cli', str(ROOT / 'bin/agent-cli'))
spec = importlib.util.spec_from_loader('agent_cli', loader)
cli = importlib.util.module_from_spec(spec)
loader.exec_module(cli)


def run(*argv):
    out = io.StringIO()
    with redirect_stdout(out):
        code = cli.main(list(argv))
    return code, out.getvalue()


class Docs(unittest.TestCase):
    TEXT = '# Title\n\n## One\ntext\n```bash\n# not a heading\n```\n### One.a\nmore\n## Two\nend\n'

    def test_headings_skip_fenced_blocks(self):
        self.assertEqual([(h['line'], h['level'], h['title']) for h in cli.headings(self.TEXT)],
                         [(1, 1, 'Title'), (3, 2, 'One'), (8, 3, 'One.a'), (10, 2, 'Two')])

    def test_a_section_ends_before_the_next_heading_at_its_level(self):
        heads = cli.headings(self.TEXT)
        self.assertEqual(cli.section(self.TEXT, heads, heads[1]), (3, 9))
        self.assertEqual(cli.section(self.TEXT, heads, heads[3]), (10, 11))

    def test_doc_prints_the_matching_section_of_a_real_document(self):
        code, out = run('doc', 'agents', '^Commands$')
        self.assertEqual(code, 0)
        self.assertRegex(out.splitlines()[0], r'^AGENTS\.md:\d+-\d+$')
        self.assertIn('make flux-reconcile', out)

    def test_an_unknown_document_is_misuse(self):
        with self.assertRaises(SystemExit) as raised, redirect_stdout(io.StringIO()):
            cli.resolve_doc('no-such-document')
        self.assertEqual(raised.exception.code, 2)


class Make(unittest.TestCase):
    def test_summarize_separates_problems_from_warnings(self):
        out = ('[OK] repo\n[WARN] stale\n[BAD] apps: broken\ntests/x.py:3: DeprecationWarning: old\n'
               'kernel/a.go:12:3: undefined: x\nRan 9 tests in 1s\nFAILED (failures=1)\n')
        s = cli.summarize(out)
        self.assertEqual(s['problems'], ['[BAD] apps: broken', 'kernel/a.go:12:3: undefined: x', 'FAILED (failures=1)'])
        self.assertEqual(s['warnings'], ['[WARN] stale', 'tests/x.py:3: DeprecationWarning: old'])
        self.assertEqual(s['summary'], ['Ran 9 tests in 1s', 'FAILED (failures=1)'])


class Targets(unittest.TestCase):
    def test_a_makefile_recipe_is_shown_and_json_matches(self):
        code, out = run('targets', 'check-v2', '--json')
        data = json.loads(out)
        self.assertEqual((code, data['run']), (0, 'Makefile recipe'))
        self.assertTrue(any('go vet' in line for line in data['recipe']))

    def test_an_unknown_target_suggests_close_names(self):
        code, out = run('targets', 'chek')
        self.assertEqual(code, 1)
        self.assertIn('check', out)


class Sessions(unittest.TestCase):
    def test_reads_pi_and_claude_code_message_shapes(self):
        pi = {'type': 'message', 'message': {'role': 'user', 'content': [{'type': 'text', 'text': 'what next?'}]}}
        claude = {'message': {'role': 'assistant', 'content': 'done'}}
        tool = {'message': {'role': 'toolResult', 'content': [{'type': 'text', 'text': 'x'}]}}
        self.assertEqual(cli.session_text(pi), ('user', 'what next?'))
        self.assertEqual(cli.session_text(claude), ('assistant', 'done'))
        self.assertEqual(cli.session_text(tool), ('', ''))


if __name__ == '__main__':
    unittest.main()
