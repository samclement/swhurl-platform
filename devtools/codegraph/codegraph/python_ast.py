"""Layer B for Python: one file's facts (``extract``), then references resolved across files (``link``).

Source is parsed with ``ast``, never imported or run. ``extract`` depends only on its arguments, so
its result can be cached by blob ID. It resolves names lexically within the file (local, enclosing
and module scopes, imports) and leaves cross-file references symbolic; ``link`` decides whether
each lands on a node in scope, outside scope (external) or nowhere it can tell (unresolved).

Deliberately conservative: a name bound in more than one way, or bound by assignment, is not
followed. A call it cannot place is counted by category, never guessed. A reference (a function or
class used as a value) is recorded only when it lands on a node; any other loaded name is a value.
"""
from __future__ import annotations

import ast
import builtins
import hashlib
import posixpath
import sys
from collections import Counter, defaultdict
from collections.abc import Iterable, Iterator
from dataclasses import dataclass

from codegraph.snapshot import Argument, Edge, Node, ObjectReference, Snapshot, Unparsed

EXTRACTOR = 'python-ast'
EXTRACTOR_VERSION = 2  # 2: references edges, object references, arguments, main blocks
BUILTINS = frozenset(dir(builtins))
MAX_REEXPORTS = 10  # re-export hops followed before a reference counts as missing

FUNCTIONS = (ast.FunctionDef, ast.AsyncFunctionDef)
SCOPES = (*FUNCTIONS, ast.ClassDef)


@dataclass(frozen=True)
class Ref:
    """A reference before linking.

    ``dotted``: an absolute dotted name (``swhurl.apps.ops.gather_status``, ``json.dumps``).
    ``local``: a definition in this file by qualified name (``Instance.named``).
    ``external``: outside any scope (a builtin). ``unresolved``: ``target`` is the category.
    Bindings also use ``value`` (bound by assignment, a parameter, a loop) and ``ambiguous``.
    """
    kind: str
    target: str = ''


@dataclass(frozen=True)
class Definition:
    qualname: str
    kind: str  # 'class' or 'function'
    start: int
    end: int
    signature: str
    body_hash: str
    redefinitions: int


@dataclass(frozen=True)
class ImportFact:
    target: str  # absolute dotted name: a module, or a module and the name taken from it
    line: int
    lazy: bool  # inside a function


@dataclass(frozen=True)
class Site:
    """A call (of ``ref``) or a reference (to ``ref``) in the body of ``caller``."""
    caller: str  # qualified name; '' for module-level code
    line: int
    ref: Ref


@dataclass(frozen=True)
class ObjectReferenceFact:
    caller: str
    line: int
    value: str  # 'module:qualname', as written


@dataclass(frozen=True)
class ArgumentFact:
    """A function or class (``ref``, if it lands on a node) passed directly as an argument of a call."""
    caller: str
    line: int
    callee: str  # the callee's absolute dotted name as this file binds it; '' when not rooted in a name it can place
    slot: str  # the argument's position ('0', '1', ...) or keyword
    ref: Ref
    literals: tuple[tuple[str, str], ...]  # (slot, value) of the call's string literal arguments


@dataclass(frozen=True)
class FileFacts:
    module: str
    path: str
    lines: int
    error: str | None
    definitions: tuple[Definition, ...] = ()
    imports: tuple[ImportFact, ...] = ()
    calls: tuple[Site, ...] = ()
    bindings: tuple[tuple[str, Ref], ...] = ()  # module-level names, for links from other files
    references: tuple[Site, ...] = ()
    object_references: tuple[ObjectReferenceFact, ...] = ()
    arguments: tuple[ArgumentFact, ...] = ()
    main_blocks: tuple[tuple[int, int], ...] = ()  # line ranges of `if __name__ == '__main__':` bodies


def module_name(path: str, import_root: str) -> str:
    """``tools/swhurl/apps/cli.py`` under ``tools`` is ``swhurl.apps.cli``; a package's ``__init__.py`` is the package."""
    relative = posixpath.relpath(path, import_root or '.')
    parts = relative[:-len('.py')].split('/')
    return '.'.join(parts[:-1] if parts[-1] == '__init__' else parts)


def extract(module: str, path: str, source: bytes) -> FileFacts:
    lines = len(source.splitlines())
    try:
        tree = ast.parse(source, filename=path)
    except (SyntaxError, ValueError) as error:
        where = f' at line {error.lineno}' if isinstance(error, SyntaxError) and error.lineno else ''
        return FileFacts(module, path, lines, f'{type(error).__name__}{where}')
    return _Extractor(module, path.endswith('__init__.py')).run(tree, path, lines)


class _Extractor:
    def __init__(self, module: str, is_package: bool):
        self.module = module
        self.package = module.split('.') if is_package else module.split('.')[:-1]
        self.definitions: dict[str, Definition] = {}
        self.imports: list[ImportFact] = []
        self.calls: list[Site] = []
        self.references: list[Site] = []
        self.object_references: list[ObjectReferenceFact] = []
        self.arguments: list[ArgumentFact] = []

    def run(self, tree: ast.Module, path: str, lines: int) -> FileFacts:
        module_scope = self._bindings(tree.body, '', ())
        self._scope(tree.body, '', '', [(False, module_scope)], lazy=False)
        return FileFacts(self.module, path, lines, None, tuple(sorted(self.definitions.values(), key=lambda d: d.qualname)),
                         tuple(sorted(set(self.imports), key=lambda i: (i.line, i.target, i.lazy))),
                         _sites(self.calls), tuple(sorted(module_scope.items())), _sites(self.references),
                         tuple(sorted(set(self.object_references), key=lambda o: (o.caller, o.line, o.value))),
                         tuple(sorted(set(self.arguments), key=lambda a: (a.caller, a.line, a.slot, a.callee, a.ref.target))),
                         tuple((node.body[0].lineno, node.body[-1].end_lineno or node.body[-1].lineno)
                               for node in tree.body if _is_main_guard(node)))

    def _scope(self, body: list[ast.stmt], caller: str, prefix: str, chain: list[tuple[bool, dict[str, Ref]]],
               lazy: bool) -> None:
        """Record the calls, imports and nested definitions of one scope's body.
        ``chain`` is (is a class scope, names) for every scope visible here, innermost first."""
        done: set[int] = set()  # IDs of name chains already handled as a callee or a reference, and annotations
        for node in _shallow(body):
            if id(node) in done:
                continue
            if isinstance(node, ast.Call):
                callee = self._callee(node.func, chain)
                self.calls.append(Site(caller, node.lineno, callee))
                done.update(id(part) for part in _chain(node.func))
                self._arguments(node, caller, callee, chain)
            elif isinstance(node, ast.Name | ast.Attribute) and isinstance(node.ctx, ast.Load):
                done.update(id(part) for part in _chain(node))
                ref = self._reference(node, chain)
                if ref is not None:
                    self.references.append(Site(caller, node.lineno, ref))
            elif isinstance(node, ast.AnnAssign):
                done.update(id(part) for part in ast.walk(node.annotation))  # a type, not a use of a value
            elif isinstance(node, ast.JoinedStr):
                done.update(id(part) for part in node.values)  # f-string fragments are not whole strings
            elif isinstance(node, ast.Constant) and _is_object_reference(node.value):
                self.object_references.append(ObjectReferenceFact(caller, node.lineno, node.value))
            elif isinstance(node, ast.Import | ast.ImportFrom):
                for target, _, _ in self._imported(node):
                    self.imports.append(ImportFact(target, node.lineno, lazy))
            elif isinstance(node, SCOPES):
                self._define(node, prefix, chain, lazy)

    def _define(self, node: ast.FunctionDef | ast.AsyncFunctionDef | ast.ClassDef, prefix: str,
                chain: list[tuple[bool, dict[str, Ref]]], lazy: bool) -> None:
        qualname = prefix + node.name
        earlier = self.definitions.get(qualname)
        self.definitions[qualname] = Definition(qualname, 'class' if isinstance(node, ast.ClassDef) else 'function',
                                                node.lineno, node.end_lineno or node.lineno, _signature(node),
                                                _body_hash(node.body), earlier.redefinitions + 1 if earlier else 0)
        if isinstance(node, ast.ClassDef):
            # A class body runs when its enclosing scope does, so its imports are as lazy as that scope's.
            own = self._bindings(node.body, qualname + '.', ())
            self._scope(node.body, qualname, qualname + '.', [(True, own), *chain], lazy)
        else:
            # Names bound in a class body are not visible inside its methods (or any inner function).
            own = self._bindings(node.body, qualname + '.', _parameters(node.args))
            outer = [scope for scope in chain if not scope[0]]
            self._scope(node.body, qualname, qualname + '.', [(False, own), *outer], lazy=True)

    def _bindings(self, body: list[ast.stmt], prefix: str, parameters: Iterable[str]) -> dict[str, Ref]:
        """The names one scope binds and what each refers to; a name bound in two different ways is ambiguous."""
        found: dict[str, set[Ref]] = defaultdict(set)
        declared_elsewhere: set[str] = set()
        for name in parameters:
            found[name].add(Ref('value'))
        for node in _shallow(body):
            if isinstance(node, SCOPES):
                found[node.name].add(Ref('local', prefix + node.name))
            elif isinstance(node, ast.Import | ast.ImportFrom):
                for _, name, bound in self._imported(node):
                    if name != '*':
                        found[name].add(Ref('dotted', bound) if bound else Ref('unresolved', 'missing'))
            elif isinstance(node, ast.Name) and not isinstance(node.ctx, ast.Load):
                found[node.id].add(Ref('value'))
            elif isinstance(node, ast.Lambda):
                for name in _parameters(node.args):
                    found[name].add(Ref('value'))
            elif isinstance(node, ast.Global | ast.Nonlocal):
                declared_elsewhere.update(node.names)
            for name in _pattern_names(node):
                found[name].add(Ref('value'))
        return {name: next(iter(refs)) if len(refs) == 1 else Ref('ambiguous')
                for name, refs in found.items() if name not in declared_elsewhere}

    def _imported(self, node: ast.Import | ast.ImportFrom) -> Iterator[tuple[str, str, str]]:
        """(what is imported, the name bound, what that name refers to) per alias, as absolute dotted names.
        They differ for ``import a.b``, which imports ``a.b`` but binds ``a`` to the package ``a``.
        Names are '' when a relative import climbs out of the tree."""
        if isinstance(node, ast.Import):
            for alias in node.names:
                if alias.asname:
                    yield alias.name, alias.asname, alias.name
                else:
                    root = alias.name.split('.')[0]
                    yield alias.name, root, root
            return
        base = self._absolute(node.level, node.module)
        for alias in node.names:
            if alias.name == '*':
                yield base, '*', ''
            else:
                target = f'{base}.{alias.name}' if base else ''
                yield target, alias.asname or alias.name, target

    def _absolute(self, level: int, module: str | None) -> str:
        if not level:
            return module or ''
        if level - 1 > len(self.package):
            return ''
        anchor = self.package[:len(self.package) - (level - 1)]
        return '.'.join([*anchor, *([module] if module else [])])

    def _callee(self, func: ast.expr, chain: list[tuple[bool, dict[str, Ref]]]) -> Ref:
        names = _dotted(func)
        if names is None:
            return Ref('unresolved', 'value-attribute' if isinstance(func, ast.Attribute) else 'dynamic')
        root, attributes = names[0], names[1:]
        ref = next((scope[root] for _, scope in chain if root in scope), None)
        if ref is None:
            ref = Ref('external') if root in BUILTINS else Ref('unresolved', 'missing')
        if ref.kind == 'value':
            return Ref('unresolved', 'value-attribute' if attributes else 'dynamic')
        if ref.kind == 'ambiguous':
            return Ref('unresolved', 'ambiguous')
        if ref.kind in ('dotted', 'local') and attributes:
            return Ref(ref.kind, '.'.join([ref.target, *attributes]))
        return ref

    def _reference(self, expr: ast.expr, chain: list[tuple[bool, dict[str, Ref]]]) -> Ref | None:
        """What a loaded name chain refers to if it could be a node in scope; None for values, builtins and the unknown."""
        if _dotted(expr) is None:
            return None
        ref = self._callee(expr, chain)
        return ref if ref.kind in ('dotted', 'local') else None

    def _arguments(self, call: ast.Call, caller: str, callee: Ref, chain: list[tuple[bool, dict[str, Ref]]]) -> None:
        """Record each argument of ``call`` that could be a function or class in scope, with the call's string literals.
        Positions after a ``*args`` are unknown, so they are not recorded."""
        slots: list[tuple[str, ast.expr]] = []
        for position, argument in enumerate(call.args):
            if isinstance(argument, ast.Starred):
                break
            slots.append((str(position), argument))
        slots.extend((keyword.arg, keyword.value) for keyword in call.keywords if keyword.arg is not None)
        literals = tuple(sorted((slot, value.value) for slot, value in slots
                                if isinstance(value, ast.Constant) and isinstance(value.value, str)))
        names = _dotted(call.func)
        dotted = {'dotted': callee.target, 'local': f'{self.module}.{callee.target}',
                  'external': '.'.join(['builtins', *(names or [])])}.get(callee.kind, '')
        for slot, value in slots:
            ref = self._reference(value, chain)
            if ref is not None:
                self.arguments.append(ArgumentFact(caller, call.lineno, dotted, slot, ref, literals))


def link(revision: str, scope: str, import_root: str, files: Iterable[FileFacts]) -> Snapshot:
    """Resolve every file's references against the others and build the snapshot."""
    return _Linker(revision, scope, import_root, files).snapshot()


class _Linker:
    def __init__(self, revision: str, scope: str, import_root: str, files: Iterable[FileFacts]):
        self.revision, self.scope, self.import_root = revision, scope, import_root
        self.files: dict[str, FileFacts] = {}
        self.unparsed: list[Unparsed] = []
        for facts in sorted(files, key=lambda f: f.path):
            if facts.module in self.files:
                self.unparsed.append(Unparsed(facts.path, f'module {facts.module} is already {self.files[facts.module].path}'))
            else:
                self.files[facts.module] = facts
                if facts.error is not None:
                    self.unparsed.append(Unparsed(facts.path, facts.error))
        self.definitions = {module: {d.qualname: d for d in facts.definitions} for module, facts in self.files.items()}
        self.bindings = {module: dict(facts.bindings) for module, facts in self.files.items()}

    def snapshot(self) -> Snapshot:
        nodes: list[Node] = []
        edges: dict[tuple[str, str, str], set[int]] = defaultdict(set)
        object_references: set[ObjectReference] = set()
        arguments: set[Argument] = set()
        for module, facts in self.files.items():
            external: Counter[str] = Counter()
            unresolved: dict[str, Counter[str]] = defaultdict(Counter)
            for call in facts.calls:
                kind, value = self._resolve(module, call.ref)
                if kind == 'node':
                    edges[(_node_id(module, call.caller), value, 'calls')].add(call.line)
                elif kind == 'external':
                    external[call.caller] += 1
                else:
                    unresolved[call.caller][value] += 1
            for reference in facts.references:
                kind, value = self._resolve(module, reference.ref)
                if kind == 'node':
                    edges[(_node_id(module, reference.caller), value, 'references')].add(reference.line)
            object_references.update(ObjectReference(_node_id(module, item.caller), item.line, item.value)
                                     for item in facts.object_references)
            for item in facts.arguments:
                kind, value = self._resolve(module, item.ref)
                if kind == 'node':
                    arguments.add(Argument(_node_id(module, item.caller), item.line, item.callee, item.slot, value,
                                           item.literals))
            for fact in facts.imports:
                target = self._module_of(fact.target)
                if target is not None and target != module:
                    edges[(_node_id(module, ''), _node_id(target, ''), 'lazy-imports' if fact.lazy else 'imports')].add(fact.line)
            nodes.append(Node(_node_id(module, ''), 'module', module, '', facts.path, 1, facts.lines,
                              external_calls=external[''], unresolved_calls=tuple(sorted(unresolved[''].items())),
                              main_blocks=facts.main_blocks))
            for d in facts.definitions:
                nodes.append(Node(_node_id(module, d.qualname), d.kind, module, d.qualname, facts.path, d.start, d.end,
                                  d.signature, d.body_hash, d.redefinitions, external[d.qualname],
                                  tuple(sorted(unresolved[d.qualname].items()))))
        return Snapshot(self.revision, self.scope, self.import_root, EXTRACTOR, EXTRACTOR_VERSION,
                        f'{sys.version_info.major}.{sys.version_info.minor}',
                        tuple(sorted(nodes, key=lambda node: node.id)),
                        tuple(Edge(source, target, kind, tuple(sorted(lines)))
                              for (source, target, kind), lines in sorted(edges.items())),
                        tuple(sorted(self.unparsed, key=lambda item: item.path)),
                        tuple(sorted(object_references)), tuple(sorted(arguments)))

    def _module_of(self, dotted: str) -> str | None:
        """The longest prefix of ``dotted`` that is a module in scope."""
        parts = dotted.split('.')
        return next(('.'.join(parts[:end]) for end in range(len(parts), 0, -1) if '.'.join(parts[:end]) in self.files), None)

    def _resolve(self, module: str, ref: Ref, hops: int = 0) -> tuple[str, str]:
        """('node', id), ('external', '') or ('unresolved', category)."""
        if ref.kind == 'external':
            return 'external', ''
        if ref.kind == 'unresolved':
            return 'unresolved', ref.target
        if ref.kind == 'local':
            return self._member(module, ref.target.split('.'), hops)
        target = self._module_of(ref.target)
        if target is None:
            return 'external', ''
        rest = ref.target.split('.')[len(target.split('.')):]
        return self._member(target, rest, hops) if rest else ('unresolved', 'missing')  # a module is not callable

    def _member(self, module: str, parts: list[str], hops: int) -> tuple[str, str]:
        if self.files[module].error is not None:
            return 'unresolved', 'unparsed'
        qualname = '.'.join(parts)
        if qualname in self.definitions[module]:
            return 'node', _node_id(module, qualname)
        if parts[0] in self.definitions[module]:
            return 'unresolved', 'missing'  # an attribute the definition does not define here (inherited, assigned)
        binding = self.bindings[module].get(parts[0])
        if binding is None or binding.kind in ('local', 'unresolved', 'external'):
            return 'unresolved', 'missing'
        if binding.kind == 'value':
            return 'unresolved', 'value-attribute' if len(parts) > 1 else 'dynamic'
        if binding.kind == 'ambiguous':
            return 'unresolved', 'ambiguous'
        if hops >= MAX_REEXPORTS:
            return 'unresolved', 'missing'
        return self._resolve(module, Ref('dotted', '.'.join([binding.target, *parts[1:]])), hops + 1)  # a re-export


def _node_id(module: str, qualname: str) -> str:
    return f'py:{module}:{qualname}' if qualname else f'py:{module}'


def _shallow(body: list[ast.stmt]) -> Iterator[ast.AST]:
    """Every node of one scope's body in source order, without entering nested function or class bodies.
    A nested definition is yielded itself, with the parts its enclosing scope evaluates (decorators,
    defaults, bases)."""
    stack: list[ast.AST] = list(reversed(body))
    while stack:
        node = stack.pop()
        yield node
        if isinstance(node, FUNCTIONS):
            children = [*node.decorator_list, *node.args.defaults, *(d for d in node.args.kw_defaults if d is not None)]
        elif isinstance(node, ast.ClassDef):
            children = [*node.decorator_list, *node.bases, *(keyword.value for keyword in node.keywords)]
        else:
            children = list(ast.iter_child_nodes(node))
        stack.extend(reversed(children))


def _sites(sites: list[Site]) -> tuple[Site, ...]:
    return tuple(sorted(sites, key=lambda site: (site.caller, site.line, site.ref.kind, site.ref.target)))


def _chain(expr: ast.expr) -> Iterator[ast.expr]:
    """``a.b.c`` and its parts ``a.b`` and ``a``; stops at anything that is not an attribute or a name."""
    while isinstance(expr, ast.Attribute):
        yield expr
        expr = expr.value
    if isinstance(expr, ast.Name):
        yield expr


def _is_object_reference(value: object) -> bool:
    """A whole string in the ``module:qualname`` form packaging entry points use (``pkg.cli:main``)."""
    if not isinstance(value, str) or value.count(':') != 1:
        return False
    return all(part.isidentifier() for side in value.split(':') for part in side.split('.'))


def _is_main_guard(node: ast.stmt) -> bool:
    """``if __name__ == '__main__':``, either way round."""
    if not (isinstance(node, ast.If) and isinstance(node.test, ast.Compare) and len(node.test.ops) == 1
            and isinstance(node.test.ops[0], ast.Eq)):
        return False
    sides = [node.test.left, node.test.comparators[0]]
    return any(isinstance(side, ast.Name) and side.id == '__name__' for side in sides) and \
        any(isinstance(side, ast.Constant) and side.value == '__main__' for side in sides)


def _dotted(func: ast.expr) -> list[str] | None:
    """``a.b.c`` as ['a', 'b', 'c']; None for anything not rooted in a plain name."""
    attributes = []
    while isinstance(func, ast.Attribute):
        attributes.append(func.attr)
        func = func.value
    return [func.id, *reversed(attributes)] if isinstance(func, ast.Name) else None


def _parameters(arguments: ast.arguments) -> list[str]:
    every = [*arguments.posonlyargs, *arguments.args, *arguments.kwonlyargs,
             *(arg for arg in (arguments.vararg, arguments.kwarg) if arg is not None)]
    return [arg.arg for arg in every]


def _pattern_names(node: ast.AST) -> list[str]:
    """Names bound by an except clause or a match pattern, which ast stores as plain strings."""
    if isinstance(node, ast.ExceptHandler | ast.MatchAs | ast.MatchStar):
        return [node.name] if node.name else []
    if isinstance(node, ast.MatchMapping):
        return [node.rest] if node.rest else []
    return []


def _signature(node: ast.FunctionDef | ast.AsyncFunctionDef | ast.ClassDef) -> str:
    if isinstance(node, ast.ClassDef):
        return '(' + ', '.join(ast.unparse(item) for item in [*node.bases, *node.keywords]) + ')'
    returns = f' -> {ast.unparse(node.returns)}' if node.returns else ''
    return ('async ' if isinstance(node, ast.AsyncFunctionDef) else '') + f'({ast.unparse(node.args)}){returns}'


def _body_hash(body: list[ast.stmt]) -> str:
    """SHA-256 of the body as ``ast.unparse`` prints it, without a leading docstring: formatting and
    comments do not change it; any change to the code, including nested definitions, does."""
    if body and isinstance(body[0], ast.Expr) and isinstance(body[0].value, ast.Constant) \
            and isinstance(body[0].value.value, str):
        body = body[1:]
    return 'sha256:' + hashlib.sha256(ast.unparse(ast.Module(body=body, type_ignores=[])).encode()).hexdigest()
