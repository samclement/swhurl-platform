"""bin/agent-cli: the offline parts an agent relies on (doc sections, make output, targets, transcripts, misuse)."""
import importlib.machinery
import importlib.util
import io
import json
import unittest
from contextlib import redirect_stdout
from unittest import mock

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


class Clock:
    """time.time and time.sleep for a test: sleeping advances the clock instead of waiting."""
    def __init__(self):
        self.now = 1000.0

    def time(self):
        return self.now

    def sleep(self, seconds):
        self.now += seconds


class CI(unittest.TestCase):
    SHA = 'a' * 40

    def ci(self, runs, pushed, *argv):
        clock = Clock()
        polls = []

        def runs_for(sha):
            polls.append(clock.now)
            return runs(clock.now - 1000)

        with mock.patch.object(cli, 'sh', return_value=mock.Mock(stdout=self.SHA + '\n')), \
                mock.patch.object(cli, 'runs_for', runs_for), mock.patch.object(cli, 'pushed', return_value=pushed), \
                mock.patch.object(cli.time, 'time', clock.time), mock.patch.object(cli.time, 'sleep', clock.sleep):
            code, out = run('ci', *argv)
        return code, out, polls, clock.now - 1000

    def test_an_unpushed_commit_stops_at_once_even_with_wait(self):
        code, out, polls, waited = self.ci(lambda t: [], False, '--wait')
        self.assertEqual((code, len(polls), waited), (1, 1, 0))
        self.assertIn('not pushed', out)

    def test_wait_gives_up_when_no_run_starts_for_a_pushed_commit(self):
        code, out, _, waited = self.ci(lambda t: [], True, '--wait')
        self.assertEqual(code, 1)
        self.assertIn(f'none started within {cli.APPEAR}s', out)
        self.assertLess(waited, cli.APPEAR + 30)

    def test_wait_follows_a_run_that_starts_late_until_it_completes(self):
        def runs(t):
            if t < 30:
                return []
            done = t >= 300
            return [{'databaseId': 1, 'workflowName': 'Validate', 'status': 'completed' if done else 'in_progress',
                     'conclusion': 'success' if done else '', 'event': 'push', 'url': 'u'}]
        code, out, _, waited = self.ci(runs, True, '--wait')
        self.assertEqual((code, out.splitlines()[0]), (0, 'aaaaaaaa runs 1 failed 0 pending 0'))
        self.assertGreaterEqual(waited, 300)

    def test_a_network_failure_is_retried_then_reported(self):
        clock, calls = Clock(), []

        def sh_json(*args, **kw):
            calls.append(clock.now)
            raise cli.ToolFailed('gh run list --commit: exit 1: error connecting to api.github.com')
        with mock.patch.object(cli, 'sh_json', sh_json), mock.patch.object(cli.time, 'time', clock.time), \
                mock.patch.object(cli.time, 'sleep', clock.sleep), self.assertRaises(cli.ToolFailed):
            cli.runs_for(self.SHA)
        self.assertGreater(len(calls), 1)
        self.assertLessEqual(calls[-1] - calls[0], cli.RETRY + 10)

    def test_a_request_error_is_not_retried(self):
        calls = []

        def sh_json(*args, **kw):
            calls.append(1)
            raise cli.ToolFailed('gh run list --commit: exit 1: authentication required')
        with mock.patch.object(cli, 'sh_json', sh_json), self.assertRaises(cli.ToolFailed):
            cli.runs_for(self.SHA)
        self.assertEqual(len(calls), 1)


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
