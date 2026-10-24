"""Layer C: canonical bytes, digests, the envelope and the strict snapshot document."""
import json
import unittest

from codegraph import documents
from codegraph import snapshot as snapshots
from codegraph.errors import CodegraphError
from codegraph.snapshot import Argument, Edge, Node, ObjectReference, Snapshot, Unparsed

SAMPLE = Snapshot(
    'a' * 40, 'tools/pkg', 'tools', 'python-ast', 2, '3.12',
    (Node('py:pkg', 'module', 'pkg', '', 'tools/pkg/__init__.py', 1, 3, main_blocks=((3, 3),)),
     Node('py:pkg:f', 'function', 'pkg', 'f', 'tools/pkg/__init__.py', 2, 3, '()', 'sha256:00', 1, 2,
          (('dynamic', 1), ('value-attribute', 4)))),
    (Edge('py:pkg', 'py:pkg:f', 'calls', (3,)), Edge('py:pkg', 'py:pkg:f', 'references', (3,))),
    (Unparsed('tools/pkg/broken.py', 'SyntaxError at line 1'),),
    (ObjectReference('py:pkg', 2, 'pkg:f'),),
    (Argument('py:pkg', 3, 'builtins.sorted', 'key', 'py:pkg:f', (('0', 'x'),)),))


def failure(callable_, *args):
    with unittest.TestCase().assertRaises(CodegraphError) as raised:
        callable_(*args)
    return raised.exception.code


class DocumentTests(unittest.TestCase):
    def test_canonical_bytes(self):
        data = documents.dump({'version': 1, 'kind': 'x', 'b': 0.123456, 'a': ['é']})
        self.assertEqual(data, '{"a":["é"],"b":0.1235,"kind":"x","version":1}\n'.encode())
        self.assertEqual(documents.digest({'kind': 'x'}), documents.digest({'kind': 'x'}))
        self.assertTrue(documents.digest({'kind': 'x'}).startswith('sha256:'))

    def test_a_non_finite_number_is_refused(self):
        self.assertEqual(failure(documents.dump, {'x': float('nan')}), 'invalid-document')

    def test_load_refuses_malformed_envelopes(self):
        good = {'kind': 'snapshot', 'version': 1, 'inputs': [], 'params': {}}
        cases = {
            b'not json': 'invalid-document',
            b'[]': 'invalid-document',
            b'{"kind": "snapshot", "kind": "snapshot", "version": 1, "inputs": [], "params": {}}': 'invalid-document',
            b'{"kind": "snapshot", "version": NaN, "inputs": [], "params": {}}': 'invalid-document',
            json.dumps({**good, 'kind': 'journeys'}).encode(): 'invalid-document',
            json.dumps({**good, 'version': 2}).encode(): 'unsupported-version',
            json.dumps({**good, 'version': True}).encode(): 'unsupported-version',
            json.dumps({key: value for key, value in good.items() if key != 'params'}).encode(): 'invalid-document',
            json.dumps({**good, 'inputs': {}}).encode(): 'invalid-document',
        }
        for data, code in cases.items():
            with self.subTest(data=data):
                self.assertEqual(failure(documents.load, data, 'snapshot'), code)
        self.assertEqual(documents.load(json.dumps(good).encode(), 'snapshot'), good)


class SnapshotDocumentTests(unittest.TestCase):
    def test_round_trip(self):
        self.assertEqual(snapshots.load(snapshots.dump(SAMPLE)), SAMPLE)

    def test_optional_fields_are_omitted_when_empty(self):
        module, function = snapshots.to_document(SAMPLE)['nodes']
        self.assertNotIn('signature', module)
        self.assertNotIn('redefinitions', module)
        self.assertNotIn('mainBlocks', function)
        self.assertEqual(module['unresolvedCalls'], {})

    def test_a_document_from_extractor_version_1_still_loads(self):
        document = snapshots.to_document(SAMPLE)
        del document['objectReferences'], document['arguments'], document['nodes'][0]['mainBlocks']
        loaded = snapshots.load(documents.dump(document))
        self.assertEqual((loaded.object_references, loaded.arguments, loaded.nodes[0].main_blocks), ((), (), ()))

    def test_as_input_names_the_snapshot_by_digest(self):
        self.assertEqual(snapshots.as_input(SAMPLE).to_document(), {
            'kind': 'snapshot', 'revision': 'a' * 40, 'digest': documents.digest(snapshots.to_document(SAMPLE))})

    def test_load_refuses_a_malformed_snapshot(self):
        def broken(change):
            document = snapshots.to_document(SAMPLE)
            change(document)
            return documents.dump(document)

        cases = {
            'unknown field': lambda d: d['nodes'][0].update(extra=1),
            'missing field': lambda d: d['nodes'][0].pop('path'),
            'wrong type': lambda d: d['nodes'][0].update(lines='1-3'),
            'bool as int': lambda d: d['nodes'][1].update(externalCalls=True),
            'unknown node kind': lambda d: d['nodes'][0].update(kind='package'),
            'unknown category': lambda d: d['nodes'][1].update(unresolvedCalls={'guessed': 1}),
            'zero count': lambda d: d['nodes'][1].update(unresolvedCalls={'dynamic': 0}),
            'duplicate id': lambda d: d['nodes'].append(dict(d['nodes'][0])),
            'dangling edge': lambda d: d['edges'][0].update(target='py:nowhere'),
            'unknown edge kind': lambda d: d['edges'][0].update(kind='guesses'),
            'unsorted lines': lambda d: d['edges'][0].update(lines=[3, 2]),
            'duplicate edge': lambda d: d['edges'].append(dict(d['edges'][0])),
            'input documents': lambda d: d['inputs'].append({'kind': 'snapshot'}),
            'main block on a function': lambda d: d['nodes'][1].update(mainBlocks=[[1, 2]]),
            'empty main blocks': lambda d: d['nodes'][0].update(mainBlocks=[]),
            'reversed main block': lambda d: d['nodes'][0].update(mainBlocks=[[3, 2]]),
            'dangling object reference': lambda d: d['objectReferences'][0].update(source='py:nowhere'),
            'object reference line 0': lambda d: d['objectReferences'][0].update(line=0),
            'duplicate object reference': lambda d: d['objectReferences'].append(dict(d['objectReferences'][0])),
            'dangling argument': lambda d: d['arguments'][0].update(target='py:nowhere'),
            'non-string literal': lambda d: d['arguments'][0].update(literals={'0': 1}),
            'argument field missing': lambda d: d['arguments'][0].pop('slot'),
            'unknown param': lambda d: d['params'].update(extra='x'),
        }
        for name, change in cases.items():
            with self.subTest(name):
                self.assertEqual(failure(snapshots.load, broken(change)), 'invalid-document')


if __name__ == '__main__':
    unittest.main()
