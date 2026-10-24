"""Layer D: graph views choose edge kinds; closure follows them, through cycles, from known nodes only."""
import unittest
from itertools import pairwise

from codegraph.errors import CodegraphError
from codegraph.graph import Graph, closure, components, graph, rank
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


# a -> b -> c -> a is a cycle; c -> d -> e; f stands alone; g -> a.
CYCLE = Graph(frozenset('abcdefg'), frozenset({('a', 'b'), ('b', 'c'), ('c', 'a'), ('c', 'd'), ('d', 'e'), ('g', 'a')}))


class ComponentTests(unittest.TestCase):
    def test_each_node_is_in_one_component_and_cycles_are_found(self):
        self.assertEqual(components(CYCLE), (frozenset('abc'), frozenset('d'), frozenset('e'), frozenset('f'), frozenset('g')))

    def test_a_self_edge_and_an_empty_graph(self):
        self.assertEqual(components(Graph(frozenset('a'), frozenset({('a', 'a')}))), (frozenset('a'),))
        self.assertEqual(components(Graph(frozenset(), frozenset())), ())

    def test_a_long_chain_does_not_recurse(self):
        nodes = [f'n{index:05}' for index in range(5000)]
        chain = Graph(frozenset(nodes), frozenset(pairwise(nodes)))
        self.assertEqual(len(components(chain)), 5000)
        self.assertEqual(rank(chain)[nodes[0]], 4999)


class RankTests(unittest.TestCase):
    def test_depth_counts_up_from_what_depends_on_nothing(self):
        self.assertEqual(rank(CYCLE), {'a': 2, 'b': 2, 'c': 2, 'd': 1, 'e': 0, 'f': 0, 'g': 3})

    def test_the_deepest_path_decides(self):
        # a -> b -> c and a -> c: a is two above c, not one.
        self.assertEqual(rank(Graph(frozenset('abc'), frozenset({('a', 'b'), ('b', 'c'), ('a', 'c')}))), {'a': 2, 'b': 1, 'c': 0})


if __name__ == '__main__':
    unittest.main()
