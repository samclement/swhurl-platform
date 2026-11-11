"""The v1 records model (tools/swhurl/apps/model) only shrinks.

It reads no v2 record and leaves with its remaining importers (docs/v2-status.md#remaining-python), so until then
nothing new may import it and it may not grow. Both pins are exact: a change that drops an importer or a
line lowers the pin in the same commit, so the room cannot be used again.
"""
import ast
import unittest

from swhurl import ROOT

MODEL = ROOT / 'tools/swhurl/apps/model'
LINES = 1541
IMPORTERS = {
    'tests/full_settings.py',
    'tests/test_app_relations.py',
    'tests/test_interface.py',
    'tools/swhurl/apps/connections.py',
    'tools/swhurl/apps/declaration.py',
    'tools/swhurl/apps/instance.py',
    'tools/swhurl/apps/offers.py',
    'tools/swhurl/apps/promotion.py',
    'tools/swhurl/apps/release.py',
    'tools/swhurl/livetests/team_scenario.py',
}


def imports_model(tree: ast.AST, package: str = '') -> bool:
    """Whether the module imports the model; package is its own package, for relative imports."""
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            names = [alias.name for alias in node.names]
        elif isinstance(node, ast.ImportFrom):
            parts = package.split('.')
            base = parts[:len(parts) - node.level + 1] if node.level else []
            module = '.'.join([*base, *([node.module] if node.module else [])])
            names = [module, *(f'{module}.{alias.name}' for alias in node.names)]
        else:
            continue
        if any(name == 'swhurl.apps.model' or name.startswith('swhurl.apps.model.') for name in names):
            return True
    return False


def package(path) -> str:
    """The dotted package a file under tools/ belongs to; only tools/swhurl is a package."""
    parts = path.relative_to(ROOT / 'tools').parent.parts if path.is_relative_to(ROOT / 'tools') else ()
    return '.'.join(parts)


def importers() -> set[str]:
    files = [path for tree in ('tools', 'tests', 'images') for path in (ROOT / tree).rglob('*.py')]
    return {str(path.relative_to(ROOT)) for path in files
            if not path.is_relative_to(MODEL)
            and imports_model(ast.parse(path.read_text(), str(path)), package(path))}


class V1ModelBoundTests(unittest.TestCase):
    def test_no_new_importers(self):
        found = importers()
        self.assertFalse(found - IMPORTERS, 'new code imports the v1 model; use v2 or a module outside it')
        self.assertFalse(IMPORTERS - found, 'these no longer import the v1 model; remove them from IMPORTERS')

    def test_does_not_grow(self):
        lines = sum(len(path.read_text().splitlines()) for path in MODEL.glob('*.py'))
        self.assertLessEqual(lines, LINES, 'the v1 model grew; it only shrinks')
        self.assertGreaterEqual(lines, LINES, f'the v1 model shrank; lower LINES to {lines}')

    def test_detects_each_import_form(self):
        for source in ('import swhurl.apps.model', 'from swhurl.apps.model import records',
                       'from swhurl.apps.model.draft import Draft', 'from swhurl.apps import model'):
            self.assertTrue(imports_model(ast.parse(source)), source)
        for source in ('from .model import records', 'from . import model', 'from ..apps.model import draft'):
            self.assertTrue(imports_model(ast.parse(source), 'swhurl.apps'), source)
        for source in ('from swhurl.apps import models', 'from swhurl.apps.modelling import x',
                       '# from swhurl.apps.model import records'):
            self.assertFalse(imports_model(ast.parse(source)), source)


if __name__ == '__main__':
    unittest.main()
