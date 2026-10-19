"""yamlio: the same objects and errors as yaml.safe_load, with text parsed once."""
import io
import unittest

import yaml

from swhurl import yamlio

TEXT = 'a: 1\nlist: [x, y]\nwhen: 2026-10-17\nflag: yes\n'


class YamlioTests(unittest.TestCase):
    def test_same_objects_as_safe_load(self):
        for text in (TEXT, '', '---\n', 'just text\n', '- 1\n- 2\n'):
            with self.subTest(text=text):
                self.assertEqual(yamlio.load(text), yaml.safe_load(text))
                self.assertEqual(list(yamlio.load_all(text)), list(yaml.safe_load_all(text)))
        self.assertEqual(list(yamlio.load_all('a: 1\n---\nb: 2\n')), [{'a': 1}, {'b': 2}])
        self.assertEqual(yamlio.load(io.StringIO(TEXT)), yaml.safe_load(TEXT))

    def test_every_caller_gets_its_own_copy(self):
        first = yamlio.load(TEXT)
        first['a'] = 2
        first['list'].append('z')
        self.assertEqual(yamlio.load(TEXT), yaml.safe_load(TEXT))
        docs = list(yamlio.load_all(TEXT))
        docs[0]['list'].clear()
        self.assertEqual(list(yamlio.load_all(TEXT)), [yaml.safe_load(TEXT)])

    def test_errors_as_safe_load(self):
        for text in ('a: [1\n', 'a: 1\n---\nb: 2\n'):  # invalid; two documents where one is expected
            with self.subTest(text=text):
                self.assertRaises(yaml.YAMLError, yaml.safe_load, text)
                self.assertRaises(yaml.YAMLError, yamlio.load, text)
        self.assertRaises(yaml.YAMLError, list, yamlio.load_all('a: [1\n'))
        docs = yamlio.load_all('a: 1\n---\nb: [1\n')  # raises only when read, after the good document
        self.assertEqual(next(docs), {'a': 1})
        self.assertRaises(yaml.YAMLError, next, docs)

    def test_dump_writes_what_the_pure_python_dumper_writes(self):
        long = 'x ' * 200
        docs = [{'b': 1, 'a': [long, 'yes', '', 'multi\nline\n', 'naïve', None, 1.5, '2026-10-17']}, {'list': []}]
        for wrap, width in ((True, 80), (False, float('inf'))):
            with self.subTest(wrap=wrap):
                expected = yaml.safe_dump_all(docs, sort_keys=False, default_flow_style=False, width=width)
                self.assertEqual(yamlio.dump_all(docs, wrap=wrap), expected)
                self.assertEqual(yamlio.dump(docs[0], wrap=wrap),
                                 yaml.safe_dump(docs[0], sort_keys=False, default_flow_style=False, width=width))
        self.assertEqual(len(yamlio.dump({'a': long}, wrap=False).splitlines()), 1)


if __name__ == '__main__':
    unittest.main()
