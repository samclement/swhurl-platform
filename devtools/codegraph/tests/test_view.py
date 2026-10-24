"""Layer F: the page embeds exactly its documents' data, cannot be broken out of by that data and loads nothing."""
import json
import re
import unittest

from codegraph import entries, journeys, view
from codegraph.errors import CodegraphError
from tests.snapshots import snapshot

# a and b are routes sharing the rare pkg.model:r; pkg.web imports pkg.model, which imports pkg.base lazily.
ROUTE = 'starlette.routing.Route'
SOURCE = snapshot(
    ['py:pkg.web', 'py:pkg.model', 'py:pkg.base', 'py:pkg.web:a', 'py:pkg.web:b', 'py:pkg.web:c', 'py:pkg.model:r',
     'py:pkg.base:p'],
    [('py:pkg.web:a', 'py:pkg.model:r', 'calls', [3]), ('py:pkg.web:b', 'py:pkg.model:r', 'calls', [5]),
     ('py:pkg.web:c', 'py:pkg.base:p', 'calls', [7]), ('py:pkg.web', 'py:pkg.model', 'imports', [1]),
     ('py:pkg.model', 'py:pkg.base', 'lazy-imports', [2])],
    arguments=[('py:pkg.web', 10, ROUTE, '1', 'py:pkg.web:a', (('0', '/a</script><b>'),)),
               ('py:pkg.web', 11, ROUTE, '1', 'py:pkg.web:b', (('0', '/b'),)),
               ('py:pkg.web', 12, ROUTE, '1', 'py:pkg.web:c', (('0', '/c'),))])
FOUND = entries.entries(SOURCE)
GROUPED = journeys.journeys(SOURCE, FOUND, journeys.Params(threshold=0.1))  # a~b: ln 1.5 / (2 ln 3 + ln 1.5)


def embedded(page: bytes):
    (data,) = re.findall(rb'<script type="application/json" id="model">(.*?)</script>', page, re.S)
    return json.loads(data)


class ViewTests(unittest.TestCase):
    def test_the_page_embeds_the_model_and_is_deterministic(self):
        page = view.render(SOURCE, FOUND, GROUPED)
        self.assertEqual(page, view.render(SOURCE, FOUND, GROUPED))
        model = embedded(page)
        self.assertEqual(model, json.loads(json.dumps(view.model(SOURCE, FOUND, GROUPED))))
        names = [name for name, _, _ in model['modules']]
        self.assertEqual(names, ['pkg.base', 'pkg.model', 'pkg.web'])
        self.assertEqual({name: depth for name, _, depth in model['modules']}, {'pkg.base': 0, 'pkg.model': 0, 'pkg.web': 1},
                         'lazy imports are drawn but do not place a module')
        self.assertIn([1, 0, 'lazy-imports'], model['imports'])
        (pair, single) = model['candidates']
        self.assertEqual([model['nodes'][model['entries'][m['entry']]['node']][1] for m in pair['members']], ['a', 'b'])
        self.assertEqual(len(single['members']), 1)

    def test_data_cannot_close_its_script_element(self):
        page = view.render(SOURCE, FOUND, GROUPED)
        self.assertNotIn(b'</script><b>', page)
        self.assertEqual(page.count(b'</script>'), 2, 'the model and the code, nothing else')
        self.assertIn('/a</script><b>', [value for entry in embedded(page)['entries'] for value in entry['values']])

    def test_the_page_loads_nothing(self):
        page = view.render(SOURCE, FOUND, GROUPED).decode()
        self.assertIn("default-src 'none'", page)
        self.assertEqual(re.findall(r'https?://[^\s"\'`]+', page), ['http://www.w3.org/2000/svg'], 'a namespace, not a fetch')
        self.assertNotRegex(page, r'\b(src|href)=')

    def test_documents_from_other_inputs_are_refused(self):
        other = snapshot(['py:pkg.web', 'py:pkg.web:a'])
        cases = (
            (other, FOUND, GROUPED),
            (SOURCE, entries.entries(snapshot(['py:pkg.web', 'py:pkg.web:a'])), GROUPED),
            (SOURCE, entries.entries(SOURCE), journeys.journeys(other, entries.entries(other))),
        )
        for case in cases:
            with self.subTest(case), self.assertRaises(CodegraphError) as raised:
                view.render(*case)
            self.assertEqual(raised.exception.code, 'invalid-document')


if __name__ == '__main__':
    unittest.main()
