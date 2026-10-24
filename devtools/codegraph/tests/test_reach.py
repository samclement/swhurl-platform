"""Layer E: reach follows calls and references through nested functions and methods; the spine is in source order."""
import unittest

from codegraph import reach
from codegraph.errors import CodegraphError
from tests.snapshots import node, snapshot

# main() passes its nested change() to run(); change() calls a method; the method references a helper.
SAMPLE = snapshot([
    'py:pkg.cli', 'py:pkg.cli:main', node('py:pkg.cli:main.change', unresolved=[('value-attribute', 2)], external=1),
    'py:pkg.cli:run', node('py:pkg.model:Store', kind='class'), node('py:pkg.model:Store.save', unresolved=[('dynamic', 1)]),
    'py:pkg.model:helper', 'py:pkg.model:unrelated', node('py:pkg.model:Error', kind='class'),
], [
    ('py:pkg.cli:main', 'py:pkg.cli:run', 'calls', [12]),
    ('py:pkg.cli:main', 'py:pkg.cli:main.change', 'references', [12]),
    ('py:pkg.cli:main', 'py:pkg.model:Error', 'references', [14]),
    ('py:pkg.cli:main', 'py:pkg.model:Store', 'calls', [3, 9]),
    ('py:pkg.cli:main.change', 'py:pkg.model:Store.save', 'calls', [6]),
    ('py:pkg.model:Store.save', 'py:pkg.model:helper', 'references', [20]),
    ('py:pkg.model:unrelated', 'py:pkg.cli:main', 'calls', [30]),
    ('py:pkg.cli', 'py:pkg.model:unrelated', 'imports', [1]),
])


class ReachTests(unittest.TestCase):
    def test_reach_goes_through_nested_functions_and_methods(self):
        result = reach.reach(SAMPLE, 'py:pkg.cli:main')
        self.assertEqual(result.reached, ('py:pkg.cli:main', 'py:pkg.cli:main.change', 'py:pkg.cli:run', 'py:pkg.model:Error',
                                          'py:pkg.model:Store', 'py:pkg.model:Store.save', 'py:pkg.model:helper'))

    def test_reach_carries_the_unknowns_it_rests_on(self):
        result = reach.reach(SAMPLE, 'py:pkg.cli:main')
        self.assertEqual((result.external_calls, result.unresolved_calls), (1, (('dynamic', 1), ('value-attribute', 2))))

    def test_the_spine_is_the_own_body_in_source_order(self):
        steps = [(step.line, step.kind, step.target) for step in reach.reach(SAMPLE, 'py:pkg.cli:main').spine]
        self.assertEqual(steps, [(3, 'calls', 'py:pkg.model:Store'), (9, 'calls', 'py:pkg.model:Store'),
                                 (12, 'references', 'py:pkg.cli:main.change'), (12, 'calls', 'py:pkg.cli:run'),
                                 (14, 'references', 'py:pkg.model:Error')])

    def test_a_node_that_reaches_nothing_reaches_itself(self):
        result = reach.reach(SAMPLE, 'py:pkg.model:helper')
        self.assertEqual((result.reached, result.spine, result.unresolved_calls), (('py:pkg.model:helper',), (), ()))

    def test_an_unknown_node_is_refused(self):
        with self.assertRaises(CodegraphError) as raised:
            reach.reach(SAMPLE, 'py:pkg.cli:nothing')
        self.assertEqual(raised.exception.code, 'unknown-node')

    def test_the_document_records_the_node_and_edge_kinds(self):
        document = reach.to_document(reach.reach(SAMPLE, 'py:pkg.cli:main'))
        self.assertEqual(document['params'], {'node': 'py:pkg.cli:main', 'edgeKinds': ['calls', 'references']})
        self.assertEqual(document['inputs'][0]['kind'], 'snapshot')
        self.assertEqual(document['spine'][0], {'line': 3, 'target': 'py:pkg.model:Store', 'kind': 'calls'})
        self.assertEqual(document['unresolvedCalls'], {'dynamic': 1, 'value-attribute': 2})


if __name__ == '__main__':
    unittest.main()
