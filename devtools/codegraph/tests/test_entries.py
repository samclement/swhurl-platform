"""Layer E: each entry rule finds its entry points, and reports what it cannot place instead of choosing."""
import unittest

from codegraph import documents, entries
from codegraph import snapshot as snapshots
from tests.snapshots import node, snapshot

FUNCTIONS = ['py:pkg.apps.cli:main_promote', 'py:pkg.cli:main', 'py:pkg.apps.cli:main', 'py:pkg.web:routes',
             'py:pkg.web:routes.page', node('py:pkg.web:Endpoint', kind='class')]


def found(result):
    return {entry.node: [(item.rule, item.line, item.value) for item in entry.evidence] for entry in result.entries}


class EntryStringTests(unittest.TestCase):
    def result(self, *values):
        return entries.entries(snapshot(['py:pkg.commands', *FUNCTIONS],
                                        object_references=[('py:pkg.commands', 10 + index, value)
                                                           for index, value in enumerate(values)]))

    def test_a_full_name_and_a_unique_suffix_resolve(self):
        result = self.result('pkg.apps.cli:main_promote', 'apps.cli:main', 'cli:main_promote')
        self.assertEqual(found(result), {
            'py:pkg.apps.cli:main_promote': [('entry-string', 10, 'pkg.apps.cli:main_promote'),
                                             ('entry-string', 12, 'cli:main_promote')],
            'py:pkg.apps.cli:main': [('entry-string', 11, 'apps.cli:main')],
        })
        self.assertEqual((result.ambiguous, result.unresolved), ((), ()))

    def test_a_suffix_matching_two_functions_is_ambiguous_not_chosen(self):
        result = self.result('cli:main')
        self.assertEqual(found(result), {})
        self.assertEqual([(match.evidence.value, match.candidates) for match in result.ambiguous],
                         [('cli:main', ('py:pkg.apps.cli:main', 'py:pkg.cli:main'))])

    def test_a_string_naming_no_function_is_unresolved(self):
        # A partial module name is not a suffix match, and a class is not a function.
        result = self.result('r:gz', 'pps.cli:main_promote', 'web:Endpoint')
        self.assertEqual(found(result), {})
        self.assertEqual([match.evidence.value for match in result.unresolved], ['r:gz', 'pps.cli:main_promote', 'web:Endpoint'])
        self.assertEqual(result.unresolved[0].evidence.path, 'pkg/commands.py')


class MainBlockTests(unittest.TestCase):
    def test_a_function_called_inside_the_guard_is_an_entry_point(self):
        result = entries.entries(snapshot(
            [node('py:pkg.cli', main_blocks=[(20, 22)]), *FUNCTIONS],
            [('py:pkg.cli', 'py:pkg.cli:main', 'calls', [21]),
             ('py:pkg.cli', 'py:pkg.apps.cli:main', 'calls', [5]),  # module-level code outside the guard
             ('py:pkg.cli', 'py:pkg.web:Endpoint', 'calls', [22]),  # a class is not an entry function
             ('py:pkg.cli', 'py:pkg.apps.cli:main_promote', 'references', [22])]))
        self.assertEqual(found(result), {'py:pkg.cli:main': [('main-block', 21, None)]})


class StarletteRouteTests(unittest.TestCase):
    def test_a_route_with_a_function_handler_is_an_entry_point(self):
        route = 'starlette.routing.Route'
        result = entries.entries(snapshot(['py:pkg.web', *FUNCTIONS], arguments=[
            ('py:pkg.web:routes', 5, route, '1', 'py:pkg.web:routes.page', (('0', '/page'),)),
            ('py:pkg.web:routes', 6, route, 'endpoint', 'py:pkg.web:routes', (('path', '/kw'),)),
            ('py:pkg.web:routes', 7, route, '1', 'py:pkg.web:Endpoint', (('0', '/class'),)),
            ('py:pkg.web:routes', 8, route, '2', 'py:pkg.cli:main', (('0', '/x'),)),
            ('py:pkg.web:routes', 9, 'other.Route', '1', 'py:pkg.cli:main', (('0', '/y'),)),
        ]))
        self.assertEqual(found(result), {'py:pkg.web:routes.page': [('starlette-route', 5, '/page')],
                                         'py:pkg.web:routes': [('starlette-route', 6, '/kw')]})


class DocumentTests(unittest.TestCase):
    def test_the_document_names_its_input_and_rules(self):
        source = snapshot(['py:pkg.commands', *FUNCTIONS],
                          object_references=[('py:pkg.commands', 3, 'cli:main'), ('py:pkg.commands', 4, 'x:y'),
                                             ('py:pkg.commands', 5, 'pkg.cli:main')])
        document = entries.to_document(entries.entries(source))
        self.assertEqual(document['inputs'], [{'kind': 'snapshot', 'revision': 'c' * 40,
                                               'digest': documents.digest(snapshots.to_document(source))}])
        self.assertEqual(document['params'], {'rules': ['entry-string', 'main-block', 'starlette-route']})
        self.assertEqual(document['entries'], [{'node': 'py:pkg.cli:main', 'evidence': [
            {'rule': 'entry-string', 'path': 'pkg/commands.py', 'line': 5, 'value': 'pkg.cli:main'}]}])
        self.assertEqual(document['ambiguous'][0]['candidates'], ['py:pkg.apps.cli:main', 'py:pkg.cli:main'])
        self.assertEqual(document['unresolved'], [{'rule': 'entry-string', 'path': 'pkg/commands.py', 'line': 4, 'value': 'x:y'}])
        self.assertEqual(documents.dump(document), documents.dump(entries.to_document(entries.entries(source))))

    def test_no_entry_points_is_an_empty_success(self):
        document = entries.to_document(entries.entries(snapshot(['py:pkg.a', 'py:pkg.a:f'])))
        self.assertEqual((document['entries'], document['ambiguous'], document['unresolved']), ([], [], []))


if __name__ == '__main__':
    unittest.main()
