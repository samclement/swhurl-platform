"""make help is generated from the Makefile, and docs/commands.md covers every target."""
import io
import re
import unittest
from contextlib import redirect_stdout

from swhurl import ROOT, makehelp


class MakeHelpTests(unittest.TestCase):
    def test_parses_targets_and_help(self):
        text = 'a: ## first\nb c: dep ## second\nX := 1 ## not a target\n.PHONY: a\nd:\n\techo ## not help\n'
        self.assertEqual(makehelp.targets(text), [('a', 'first'), ('b c', 'second')])

    def test_every_real_target_has_help(self):
        makefile = (ROOT / 'Makefile').read_text()
        declared = {t for line in makefile.splitlines() if line.startswith('.PHONY:')
                    for t in line.split(':', 1)[1].split()}
        documented = {t for names, _ in makehelp.targets(makefile) for t in names.split()}
        self.assertEqual(declared - documented, set(), 'targets without a ## help line')

    def test_commands_doc_lists_every_target(self):
        doc = (ROOT / 'docs/commands.md').read_text()
        mentioned = set(re.findall(r'`([a-z][a-z0-9-]*)', doc))
        for names, _ in makehelp.targets((ROOT / 'Makefile').read_text()):
            for target in names.split():
                with self.subTest(target=target):
                    self.assertIn(target, mentioned, f'{target} is missing from docs/commands.md')

    def test_help_prints_every_target(self):
        out = io.StringIO()
        with redirect_stdout(out):
            self.assertEqual(makehelp.main([]), 0)
        for names, _ in makehelp.targets((ROOT / 'Makefile').read_text()):
            self.assertIn(names, out.getvalue())


if __name__ == '__main__':
    unittest.main()
