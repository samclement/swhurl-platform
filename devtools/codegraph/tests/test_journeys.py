"""Layer E: entry points are paired by distinctive shared reach, not by plumbing or a chain, and ties all count."""
import math
import unittest

from codegraph import documents, entries, journeys
from codegraph.errors import CodegraphError
from codegraph.journeys import Params
from tests.snapshots import node, snapshot


def sample(entry_points, calls, nodes=()):
    """Entry points ``py:pkg.cli:<name>``, found by entry strings; ``calls`` as (source, target) node IDs."""
    names = {item for pair in calls for item in pair} | {f'py:pkg.cli:{name}' for name in entry_points}
    given = {item.id: item for item in nodes}
    result = snapshot(['py:pkg.commands', *(given.get(name) or node(name) for name in sorted(names | set(given)))],
                      [(source, target, 'calls', [1]) for source, target in calls],
                      object_references=[('py:pkg.commands', line, f'pkg.cli:{name}')
                                         for line, name in enumerate(entry_points, 1)])
    return result, entries.entries(result)


def groups(result):
    return [[member.node.removeprefix('py:pkg.cli:') for member in candidate.members] for candidate in result.candidates]


# a and b share the rare r; every entry point reaches the plumbing p and q.
RARE = sample('abcd', [*((f'py:pkg.cli:{name}', 'py:pkg.run:p') for name in 'abcd'), ('py:pkg.run:p', 'py:pkg.run:q'),
                       ('py:pkg.cli:a', 'py:pkg.model:r'), ('py:pkg.cli:b', 'py:pkg.model:r')])


class GroupingTests(unittest.TestCase):
    def test_entry_points_sharing_only_plumbing_are_not_grouped(self):
        source, found = sample('abc', [*((f'py:pkg.cli:{name}', 'py:pkg.run:p') for name in 'abc'),
                                       ('py:pkg.run:p', 'py:pkg.run:q')])
        result = journeys.journeys(source, found, Params(threshold=0.01))
        self.assertEqual(groups(result), [['a'], ['b'], ['c']])
        self.assertEqual({member.nearest for candidate in result.candidates for member in candidate.members}, {None},
                         'nothing shared that weighs anything: no nearest')

    def test_uniform_weighting_groups_them_by_the_plumbing(self):
        source, found = sample('abc', [*((f'py:pkg.cli:{name}', 'py:pkg.run:p') for name in 'abc'),
                                       ('py:pkg.run:p', 'py:pkg.run:q')])
        result = journeys.journeys(source, found, Params(threshold=0.5, weighting='uniform'))
        self.assertEqual(groups(result), [['a', 'b', 'c']], '{p, q} of {a, b, p, q}: 0.5 for every pair, tied')

    def test_entry_points_sharing_a_rare_function_are_grouped(self):
        # ln 2 shared over ln 4 + ln 4 + ln 2 (each entry point itself, r; p and q weigh 0): exactly 0.2.
        result = journeys.journeys(*RARE, Params(threshold=0.2))
        self.assertEqual(groups(result), [['a', 'b'], ['c'], ['d']])
        candidate = result.candidates[0]
        self.assertEqual([(link.a, link.b, link.similarity) for link in candidate.links],
                         [('py:pkg.cli:a', 'py:pkg.cli:b', 0.2)])
        self.assertEqual([(item.node, item.entry_points) for item in candidate.shared],
                         [('py:pkg.model:r', 2), ('py:pkg.run:p', 4), ('py:pkg.run:q', 4)], 'rarest first')
        self.assertEqual(groups(journeys.journeys(*RARE, Params(threshold=0.2001))), [['a'], ['b'], ['c'], ['d']])

    def test_a_third_entry_point_is_not_chained_in(self):
        # a and b share r and t; b and c share only s. c's nearest is b, but b's is a.
        source, found = sample('abcd', [('py:pkg.cli:a', 'py:pkg.m:r'), ('py:pkg.cli:b', 'py:pkg.m:r'),
                                        ('py:pkg.cli:a', 'py:pkg.m:t'), ('py:pkg.cli:b', 'py:pkg.m:t'),
                                        ('py:pkg.cli:b', 'py:pkg.m:s'), ('py:pkg.cli:c', 'py:pkg.m:s')])
        result = journeys.journeys(source, found, Params(threshold=0.01))
        nearest = {member.node: member.nearest for candidate in result.candidates for member in candidate.members}
        self.assertEqual(nearest['py:pkg.cli:c'].nodes, ('py:pkg.cli:b',))
        self.assertGreater(nearest['py:pkg.cli:c'].similarity, 0.01, 'c is similar enough to b, yet not linked')
        self.assertEqual(groups(result), [['a', 'b'], ['c'], ['d']])
        self.assertEqual([(link.a, link.b) for link in result.candidates[0].links], [('py:pkg.cli:a', 'py:pkg.cli:b')])

    def test_tied_nearest_entry_points_all_count(self):
        # a, b and c are equally similar through r (3 of 4 entry points reach it); d shares nothing.
        source, found = sample('dcba', [(f'py:pkg.cli:{name}', 'py:pkg.m:r') for name in 'abc'] + [('py:pkg.cli:d', 'py:pkg.m:z')])
        result = journeys.journeys(source, found, Params(threshold=0.05))
        nearest = {member.node: member.nearest and member.nearest.nodes
                   for candidate in result.candidates for member in candidate.members}
        self.assertEqual(nearest, {'py:pkg.cli:a': ('py:pkg.cli:b', 'py:pkg.cli:c'), 'py:pkg.cli:b': ('py:pkg.cli:a', 'py:pkg.cli:c'),
                                   'py:pkg.cli:c': ('py:pkg.cli:a', 'py:pkg.cli:b'), 'py:pkg.cli:d': None})
        self.assertEqual(groups(result), [['a', 'b', 'c'], ['d']])
        self.assertEqual(len(result.candidates[0].links), 3)

    def test_the_threshold_is_a_floor_and_equal_candidates_are_in_member_order(self):
        source, found = sample('dcba', [(f'py:pkg.cli:{name}', 'py:pkg.m:r') for name in 'abc'] + [('py:pkg.cli:d', 'py:pkg.m:z')])
        result = journeys.journeys(source, found, Params(threshold=0.99))
        self.assertEqual(groups(result), [['a'], ['b'], ['c'], ['d']])
        self.assertEqual(documents.dump(journeys.to_document(result)),
                         documents.dump(journeys.to_document(journeys.journeys(source, found, Params(threshold=0.99)))))

    def test_members_carry_their_reach_and_its_unknowns(self):
        source, found = sample('ab', [('py:pkg.cli:a', 'py:pkg.m:r'), ('py:pkg.cli:a', 'py:pkg.m:R')], nodes=[
            node('py:pkg.m:r', unresolved=[('dynamic', 2)]), node('py:pkg.m:R', kind='class', unresolved=[('missing', 1)])])
        member = journeys.journeys(source, found).candidates[0].members[0]
        self.assertEqual((member.node, member.values, member.reached, member.unresolved_calls),
                         ('py:pkg.cli:a', ('pkg.cli:a',), 2, (('dynamic', 2), ('missing', 1))),
                         'a class is reached and carries its unknowns, but only functions are compared')

    def test_one_or_no_entry_points(self):
        source, found = sample('a', [('py:pkg.cli:a', 'py:pkg.m:r')])
        (candidate,) = journeys.journeys(source, found).candidates
        self.assertEqual((candidate.members[0].nearest, [item.node for item in candidate.shared]),
                         (None, ['py:pkg.cli:a', 'py:pkg.m:r']))
        source, found = sample('', [('py:pkg.m:f', 'py:pkg.m:r')])
        self.assertEqual(journeys.journeys(source, found).candidates, ())


class ContractTests(unittest.TestCase):
    def test_the_document_names_both_inputs_and_every_parameter(self):
        source, found = RARE
        document = journeys.to_document(journeys.journeys(source, found, Params(threshold=0.2)))
        self.assertEqual(document['inputs'], [found.snapshot.to_document(), entries.as_input(found).to_document()])
        self.assertEqual(document['params'], {'edgeKinds': ['calls', 'references'], 'nodeKind': 'function',
                                              'weighting': 'inverse', 'similarity': 'weighted-jaccard',
                                              'grouping': 'mutual-nearest', 'threshold': 0.2})
        first = document['candidates'][0]
        self.assertEqual(first['members'][0], {'node': 'py:pkg.cli:a', 'values': ['pkg.cli:a'], 'reached': 4,
                                               'unresolvedCalls': {},
                                               'nearest': {'nodes': ['py:pkg.cli:b'], 'similarity': 0.2}})
        self.assertEqual(first['shared'][0], {'node': 'py:pkg.model:r', 'entryPoints': 2})
        self.assertNotIn('nearest', document['candidates'][1]['members'][0], 'c shares nothing that weighs anything')
        self.assertEqual(round(math.log(4 / 2) / (2 * math.log(4) + math.log(2)), 4), first['links'][0]['similarity'])

    def test_entries_from_another_snapshot_are_refused(self):
        other, _ = sample('ab', [])
        with self.assertRaises(CodegraphError) as raised:
            journeys.journeys(other, RARE[1])
        self.assertEqual(raised.exception.code, 'invalid-document')

    def test_parameters_out_of_range_are_refused(self):
        for params in (Params(threshold=0), Params(threshold=1.5), Params(weighting='squared')):
            with self.subTest(params), self.assertRaises(CodegraphError) as raised:
                journeys.journeys(*RARE, params)
            self.assertEqual(raised.exception.code, 'usage')



class LoadTests(unittest.TestCase):
    def document(self):
        return journeys.to_document(journeys.journeys(*RARE, Params(threshold=0.2)))

    def test_round_trip(self):
        result = journeys.journeys(*RARE, Params(threshold=0.2))
        self.assertEqual(journeys.load(documents.dump(journeys.to_document(result))), result)

    def test_load_refuses_malformed_journeys(self):
        def broken(change):
            document = self.document()
            change(document)
            return documents.dump(document)

        cases = {
            'unknown field': lambda d: d['candidates'][0].update(extra=1),
            'missing member field': lambda d: d['candidates'][0]['members'][0].pop('reached'),
            'reached 0': lambda d: d['candidates'][0]['members'][0].update(reached=0),
            'similarity above 1': lambda d: d['candidates'][0]['links'][0].update(similarity=1.5),
            'similarity as bool': lambda d: d['candidates'][0]['links'][0].update(similarity=True),
            'link to a non-member': lambda d: d['candidates'][0]['links'][0].update(b='py:pkg.cli:z'),
            'link reversed': lambda d: d['candidates'][0]['links'][0].update(a='py:pkg.cli:b', b='py:pkg.cli:a'),
            'group without links': lambda d: d['candidates'][0].update(links=[]),
            'nearest at 0': lambda d: d['candidates'][0]['members'][0]['nearest'].update(similarity=0),
            'shared by fewer than the members': lambda d: d['candidates'][0]['shared'][0].update(entryPoints=1),
            'entry point in two candidates': lambda d: d['candidates'].append(d['candidates'][0]),
            'unknown category': lambda d: d['candidates'][0]['members'][0].update(unresolvedCalls={'guessed': 1}),
            'another grouping': lambda d: d['params'].update(grouping='connected'),
            'threshold 0': lambda d: d['params'].update(threshold=0),
            'one input': lambda d: d['inputs'].pop(),
            'inputs swapped': lambda d: d['inputs'].reverse(),
        }
        for name, change in cases.items():
            with self.subTest(name), self.assertRaises(CodegraphError) as raised:
                journeys.load(broken(change))
            self.assertEqual(raised.exception.code, 'invalid-document')

    def test_a_whole_number_threshold_loads(self):
        document = self.document()
        document['params']['threshold'] = 1
        self.assertEqual(journeys.load(documents.dump(document)).params.threshold, 1.0)


if __name__ == '__main__':
    unittest.main()
