"""Layer B: what each kind of Python reference resolves to, and what is counted instead of guessed."""
import textwrap
import unittest

from codegraph import python_ast


def snapshot(files, scope='pkg', order=sorted):
    root = scope.rpartition('/')[0]
    facts = [python_ast.extract(python_ast.module_name(path, root), path, textwrap.dedent(source).encode())
             for path, source in files.items()]
    return python_ast.link('0' * 40, scope, root, order(facts, key=lambda f: f.path))


def edges(result, kind='calls'):
    return {(edge.source, edge.target) for edge in result.edges if edge.kind == kind}


def calls_from(result, node_id):
    return {target for source, target in edges(result) if source == node_id}


def node(result, node_id):
    return next(item for item in result.nodes if item.id == node_id)


B = '''
def f():
    pass

VALUE = 1

class K:
    def build(self):
        pass
'''


class ResolutionTests(unittest.TestCase):
    def test_module_and_from_imports(self):
        result = snapshot({'pkg/__init__.py': '', 'pkg/b.py': B, 'pkg/a.py': '''
            import pkg.b
            import pkg.b as bb
            from pkg import b
            from pkg.b import f, K

            def use():
                pkg.b.f()
                bb.f()
                b.f()
                f()
                K.build()
                K()
            '''})
        self.assertEqual(calls_from(result, 'py:pkg.a:use'), {'py:pkg.b:f', 'py:pkg.b:K.build', 'py:pkg.b:K'})
        self.assertEqual(node(result, 'py:pkg.a:use').unresolved_calls, ())

    def test_relative_imports_from_a_module_and_from_a_package(self):
        result = snapshot({'pkg/__init__.py': 'from .b import f\n', 'pkg/b.py': B, 'pkg/sub/__init__.py': '',
                           'pkg/sub/d.py': 'def g():\n    pass\n', 'pkg/sub/c.py': '''
            from ..b import f
            from . import d
            from .d import g

            def use():
                f()
                d.g()
                g()
            '''})
        self.assertEqual(calls_from(result, 'py:pkg.sub.c:use'), {'py:pkg.b:f', 'py:pkg.sub.d:g'})
        self.assertIn(('py:pkg', 'py:pkg.b'), edges(result, 'imports'))

    def test_a_re_export_is_followed_to_the_definition(self):
        result = snapshot({'pkg/__init__.py': 'from .b import f\n', 'pkg/b.py': B,
                           'pkg/c.py': 'from pkg import f\n\ndef use():\n    f()\n'})
        self.assertEqual(calls_from(result, 'py:pkg.c:use'), {'py:pkg.b:f'})

    def test_nested_definitions_and_enclosing_scopes(self):
        result = snapshot({'pkg/a.py': '''
            def helper():
                pass

            def outer():
                def inner():
                    helper()
                    sibling()

                def sibling():
                    pass

                inner()
            '''})
        self.assertEqual(calls_from(result, 'py:pkg.a:outer'), {'py:pkg.a:outer.inner'})
        self.assertEqual(calls_from(result, 'py:pkg.a:outer.inner'), {'py:pkg.a:helper', 'py:pkg.a:outer.sibling'})

    def test_class_body_names_are_not_visible_in_methods(self):
        result = snapshot({'pkg/a.py': '''
            def f():
                pass

            def make():
                pass

            class C:
                f = make()

                def m(self):
                    f()
            '''})
        self.assertEqual(calls_from(result, 'py:pkg.a:C.m'), {'py:pkg.a:f'})
        self.assertEqual(calls_from(result, 'py:pkg.a:C'), {'py:pkg.a:make'}, 'a class body call belongs to the class')

    def test_a_lazy_import_resolves_and_gives_a_lazy_edge(self):
        result = snapshot({'pkg/b.py': B, 'pkg/c.py': 'def h():\n    pass\n', 'pkg/a.py': '''
            import pkg.c

            def g():
                from pkg.b import f
                f()
            '''})
        self.assertEqual(calls_from(result, 'py:pkg.a:g'), {'py:pkg.b:f'})
        self.assertEqual(edges(result, 'lazy-imports'), {('py:pkg.a', 'py:pkg.b')})
        self.assertEqual(edges(result, 'imports'), {('py:pkg.a', 'py:pkg.c')})

    def test_a_global_declaration_reaches_the_module_binding(self):
        result = snapshot({'pkg/b.py': B, 'pkg/a.py': '''
            from pkg.b import f

            def g():
                global f
                f()
            '''})
        self.assertEqual(calls_from(result, 'py:pkg.a:g'), {'py:pkg.b:f'})

    def test_decorators_and_defaults_belong_to_the_enclosing_scope(self):
        result = snapshot({'pkg/a.py': '''
            def deco():
                return lambda function: function

            def default():
                pass

            @deco()
            def decorated(x=default()):
                pass
            '''})
        self.assertEqual(calls_from(result, 'py:pkg.a'), {'py:pkg.a:deco', 'py:pkg.a:default'})
        self.assertEqual(calls_from(result, 'py:pkg.a:decorated'), set())

    def test_a_redefinition_merges_into_one_node(self):
        result = snapshot({'pkg/a.py': '''
            def helper():
                pass

            class P:
                @property
                def v(self):
                    return 1

                @v.setter
                def v(self, value):
                    helper()
            '''})
        self.assertEqual(node(result, 'py:pkg.a:P.v').redefinitions, 1)
        self.assertEqual(calls_from(result, 'py:pkg.a:P.v'), {'py:pkg.a:helper'})


class CountedTests(unittest.TestCase):
    """What is not a node in scope is counted on the caller, by category, never turned into an edge."""

    def test_external_calls(self):
        result = snapshot({'pkg/a.py': 'import json\n\ndef g():\n    json.dumps(1)\n    len([])\n    str.join("", [])\n'})
        self.assertEqual(node(result, 'py:pkg.a:g').external_calls, 3)
        self.assertEqual(calls_from(result, 'py:pkg.a:g'), set())
        self.assertEqual(edges(result, 'imports'), set(), 'an import outside the scope is not an edge')

    def test_each_unresolved_category(self):
        result = snapshot({'pkg/b.py': B, 'pkg/broken.py': 'def f(:\n', 'pkg/a.py': '''
            from pkg.b import f, nothing, VALUE
            from pkg.broken import g

            try:
                from pkg.b import f as impl
            except ImportError:
                def impl():
                    pass

            def cats(param):
                param.go()
                "x".upper()
                VALUE.x()
                param()
                (lambda: 1)()
                VALUE()
                undefined_name()
                nothing()
                impl()
                g()

            def shadow(other):
                f = other
                f()
            '''})
        self.assertEqual(dict(node(result, 'py:pkg.a:cats').unresolved_calls),
                         {'value-attribute': 3, 'dynamic': 3, 'missing': 2, 'ambiguous': 1, 'unparsed': 1})
        self.assertEqual(dict(node(result, 'py:pkg.a:shadow').unresolved_calls), {'dynamic': 1},
                         'a local assignment shadows the import')
        self.assertEqual(calls_from(result, 'py:pkg.a:cats'), set())


class ReferenceTests(unittest.TestCase):
    """A function or class used as a value is a ``references`` edge; a call or a value is not."""

    def test_functions_and_classes_used_as_values(self):
        result = snapshot({'pkg/b.py': B, 'pkg/a.py': '''
            import pkg.b
            from pkg.b import f, K

            def deco(function):
                return function

            @deco
            def decorated():
                pass

            class Sub(K):
                pass

            def main():
                def change():
                    pass

                run(change, True)
                table = {'go': f, 'build': K.build}
                handler = pkg.b.f
                try:
                    pass
                except K:
                    pass
                return Sub

            def run(work, flag):
                work()
            '''})
        self.assertEqual(edges(result, 'references'), {
            ('py:pkg.a:main', 'py:pkg.a:main.change'), ('py:pkg.a:main', 'py:pkg.b:f'), ('py:pkg.a:main', 'py:pkg.b:K.build'),
            ('py:pkg.a:main', 'py:pkg.b:K'), ('py:pkg.a:main', 'py:pkg.a:Sub'),
            ('py:pkg.a', 'py:pkg.a:deco'), ('py:pkg.a', 'py:pkg.b:K'),
        })
        self.assertEqual(calls_from(result, 'py:pkg.a:main'), {'py:pkg.a:run'}, 'a reference is not a call')
        self.assertEqual(dict(node(result, 'py:pkg.a:run').unresolved_calls), {'dynamic': 1})

    def test_calls_values_modules_builtins_and_annotations_are_not_references(self):
        result = snapshot({'pkg/b.py': B, 'pkg/a.py': '''
            import json
            import pkg.b
            from pkg.b import f, K, VALUE

            def g(param: K) -> K:
                local: K = K()
                f()
                pkg.b.f()
                x = VALUE
                y = param
                z = len
                w = json.dumps
                m = pkg.b
                f = 1
            '''})
        self.assertEqual(edges(result, 'references'), set())
        self.assertEqual(calls_from(result, 'py:pkg.a:g'), {'py:pkg.b:K', 'py:pkg.b:f'})

    def test_a_reference_lands_on_the_definition_through_a_re_export(self):
        result = snapshot({'pkg/__init__.py': 'from .b import f\n', 'pkg/b.py': B,
                           'pkg/c.py': 'import pkg\n\ndef use():\n    return pkg.f\n'})
        self.assertEqual(edges(result, 'references'), {('py:pkg.c:use', 'py:pkg.b:f')})

    def test_arguments_record_the_callee_slot_and_string_literals(self):
        result = snapshot({'pkg/b.py': B, 'pkg/a.py': '''
            from starlette.routing import Route
            from pkg.b import f, K

            def page():
                pass

            def routes(other):
                return [Route('/page', page, methods=['POST']), Route(path='/kw', endpoint=f), sorted([], key=f),
                        other.add('/x', page), Route('/wrapped', wrap(page)), Route(*other, page), K(f)]

            def wrap(function):
                return function
            '''})
        self.assertEqual({(a.line, a.callee, a.slot, a.target, a.literals) for a in result.arguments}, {
            (9, 'starlette.routing.Route', '1', 'py:pkg.a:page', (('0', '/page'),)),
            (9, 'starlette.routing.Route', 'endpoint', 'py:pkg.b:f', (('path', '/kw'),)),
            (9, 'builtins.sorted', 'key', 'py:pkg.b:f', ()),
            (10, '', '1', 'py:pkg.a:page', (('0', '/x'),)),
            (10, 'pkg.a.wrap', '0', 'py:pkg.a:page', ()),
            (10, 'pkg.b.K', '0', 'py:pkg.b:f', ()),
        })
        self.assertEqual({a.source for a in result.arguments}, {'py:pkg.a:routes'})

    def test_object_reference_strings(self):
        result = snapshot({'pkg/a.py': '''
            COMMANDS = ['pkg.cli:main', 'cli:Store.save', 'r:gz']
            NOT = ['a:b:c', 'a b:c', 'pkg.cli', 'x:', f'pkg.{1}:main', f'cli:main{1}', 'http://x']

            def f():
                """Run cli:main."""
                return 'x.y:z'
            '''})
        self.assertEqual({(o.source, o.line, o.value) for o in result.object_references}, {
            ('py:pkg.a', 2, 'pkg.cli:main'), ('py:pkg.a', 2, 'cli:Store.save'), ('py:pkg.a', 2, 'r:gz'),
            ('py:pkg.a:f', 7, 'x.y:z')})

    def test_main_blocks_are_top_level_name_guards(self):
        result = snapshot({'pkg/a.py': '''
            def main():
                if __name__ == '__main__':
                    pass

            if __name__ == '__main__':
                main()
                main()

            if '__main__' == __name__:
                main()
            elif __name__ == 'other':
                pass
            '''})
        self.assertEqual(node(result, 'py:pkg.a').main_blocks, ((7, 8), (11, 11)))
        self.assertEqual(node(result, 'py:pkg.a:main').main_blocks, ())


class SnapshotTests(unittest.TestCase):
    def test_nodes_carry_kind_location_signature_and_module(self):
        result = snapshot({'pkg/__init__.py': '', 'pkg/a.py': '''
            class Base:
                pass

            class K(Base, metaclass=type):
                async def run(self, x: int, *, y=2) -> str:
                    return ""
            '''})
        self.assertEqual(node(result, 'py:pkg').path, 'pkg/__init__.py')
        method = node(result, 'py:pkg.a:K.run')
        self.assertEqual((method.kind, method.module, method.qualname, method.start, method.end),
                         ('function', 'pkg.a', 'K.run', 6, 7))
        self.assertEqual(method.signature, 'async (self, x: int, *, y=2) -> str')
        self.assertEqual(node(result, 'py:pkg.a:K').signature, '(Base, metaclass=type)')
        self.assertEqual(node(result, 'py:pkg.a').kind, 'module')

    def test_the_body_hash_ignores_docstrings_comments_and_formatting(self):
        def body_hash(source):
            return node(snapshot({'pkg/a.py': source}), 'py:pkg.a:f').body_hash

        base = body_hash('def f():\n    return 1 + 2\n')
        self.assertEqual(body_hash('def f():\n    """Explained."""\n    # a comment\n    return (1 +\n            2)\n'), base)
        self.assertNotEqual(body_hash('def f():\n    return 1 + 3\n'), base)
        self.assertEqual(body_hash('def f(x):\n    return 1 + 2\n'), base, 'the signature is not part of the body')

    def test_an_unparsable_file_is_recorded_and_keeps_its_module_node(self):
        result = snapshot({'pkg/a.py': 'x = 1\n', 'pkg/broken.py': '\n\ndef f(:\n'})
        self.assertEqual([(item.path, item.error) for item in result.unparsed], [('pkg/broken.py', 'SyntaxError at line 3')])
        self.assertEqual(node(result, 'py:pkg.broken').kind, 'module')

    def test_two_files_for_one_module_keep_the_first_and_record_the_second(self):
        result = snapshot({'pkg/x.py': '', 'pkg/x/__init__.py': ''})
        self.assertEqual(node(result, 'py:pkg.x').path, 'pkg/x.py')
        self.assertEqual([item.path for item in result.unparsed], ['pkg/x/__init__.py'])

    def test_file_order_does_not_change_the_snapshot(self):
        files = {'pkg/__init__.py': 'from .b import f\n', 'pkg/b.py': B, 'pkg/c.py': 'from pkg import f\nf()\n'}
        self.assertEqual(snapshot(files), snapshot(files, order=lambda items, key: sorted(items, key=key, reverse=True)))

    def test_module_names_come_from_the_import_root(self):
        self.assertEqual(python_ast.module_name('tools/swhurl/apps/cli.py', 'tools'), 'swhurl.apps.cli')
        self.assertEqual(python_ast.module_name('tools/swhurl/__init__.py', 'tools'), 'swhurl')
        self.assertEqual(python_ast.module_name('pkg/a.py', ''), 'pkg.a')


if __name__ == '__main__':
    unittest.main()
