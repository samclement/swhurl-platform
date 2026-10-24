"""Layer D: graph views choose edge kinds; closure follows them, through cycles, from known nodes only."""
import unittest

from codegraph.errors import CodegraphError
from codegraph.graph import closure, graph
from tests.snapshots import snapshot

SAMPLE = snapshot(['py:m', 'py:m:a', 'py:m:b', 'py:m:c', 'py:m:d'], [
    ('py:m:a', 'py:m:b', 'calls', [1]),
    ('py:m:b', 'py:m:a', 'calls', [2]),
    ('py:m:b', 'py:m:c', 'references', [3]),
    ('py:m', 'py:m:d', 'imports', [4]),
])


class GraphTests(unittest.TestCase):
    def test_a_view_has_every_node_and_only_the_chosen_kinds(self):
        view = graph(SAMPLE, ['calls'])
        self.assertEqual(len(view.nodes), 5)
        self.assertEqual(view.edges, {('py:m:a', 'py:m:b'), ('py:m:b', 'py:m:a')})

    def test_an_unknown_edge_kind_is_refused(self):
        with self.assertRaises(CodegraphError) as raised:
            graph(SAMPLE, ['calls', 'guesses'])
        self.assertEqual(raised.exception.code, 'usage')

    def test_closure_includes_the_starts_and_survives_cycles(self):
        self.assertEqual(closure(graph(SAMPLE, ['calls']), ['py:m:a']), {'py:m:a', 'py:m:b'})
        self.assertEqual(closure(graph(SAMPLE, ['calls', 'references']), ['py:m:a']), {'py:m:a', 'py:m:b', 'py:m:c'})
        self.assertEqual(closure(graph(SAMPLE, ['calls']), ['py:m:d']), {'py:m:d'})

    def test_closure_refuses_a_start_that_is_not_a_node(self):
        with self.assertRaises(CodegraphError) as raised:
            closure(graph(SAMPLE, ['calls']), ['py:m:nothing'])
        self.assertEqual(raised.exception.code, 'unknown-node')


if __name__ == '__main__':
    unittest.main()
