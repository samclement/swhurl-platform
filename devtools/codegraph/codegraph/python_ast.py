"""Layer B for Python: one file's facts (``extract``), then references resolved across files (``link``).

Source is parsed with ``ast``, never imported or run. ``extract`` depends only on its arguments, so
its result can be cached by blob ID. It resolves names lexically within the file (local, enclosing
and module scopes, imports) and leaves cross-file references symbolic; ``link`` decides whether
each lands on a node in scope, outside scope (external) or nowhere it can tell (unresolved).

Deliberately conservative: a name bound in more than one way, or bound by assignment, is not
followed. A call it cannot place is counted by category, never guessed.
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

from codegraph.snapshot import Edge, Node, Snapshot, Unparsed

EXTRACTOR = 'python-ast'
EXTRACTOR_VERSION = 1
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
class CallSite:
    caller: str  # qualified name; '' for module-level code
    line: int
    ref: Ref


@dataclass(frozen=True)
class FileFacts:
    module: str
    path: str
    lines: int
    error: str | None
    definitions: tuple[Definition, ...] = ()
    imports: tuple[ImportFact, ...] = ()
    calls: tuple[CallSite, ...] = ()
    bindings: tuple[tuple[str, Ref], ...] = ()  # module-level names, for links from other files


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
        self.calls: list[CallSite] = []

    def run(self, tree: ast.Module, path: str, lines: int) -> FileFacts:
        module_scope = self._bindings(tree.body, '', ())
        self._scope(tree.body, '', '', [(False, module_scope)], lazy=False)
        return FileFacts(self.module, path, lines, None, tuple(sorted(self.definitions.values(), key=lambda d: d.qualname)),
                         tuple(sorted(set(self.imports), key=lambda i: (i.line, i.target, i.lazy))),
                         tuple(sorted(self.calls, key=lambda c: (c.caller, c.line, c.ref.kind, c.ref.target))),
                         tuple(sorted(module_scope.items())))

    def _scope(self, body: list[ast.stmt], caller: str, prefix: str, chain: list[tuple[bool, dict[str, Ref]]],
               lazy: bool) -> None:
        """Record the calls, imports and nested definitions of one scope's body.
        ``chain`` is (is a class scope, names) for every scope visible here, innermost first."""
        for node in _shallow(body):
            if isinstance(node, ast.Call):
                self.calls.append(CallSite(caller, node.lineno, self._callee(node.func, chain)))
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
            for fact in facts.imports:
                target = self._module_of(fact.target)
                if target is not None and target != module:
                    edges[(_node_id(module, ''), _node_id(target, ''), 'lazy-imports' if fact.lazy else 'imports')].add(fact.line)
            nodes.append(Node(_node_id(module, ''), 'module', module, '', facts.path, 1, facts.lines,
                              external_calls=external[''], unresolved_calls=tuple(sorted(unresolved[''].items()))))
            for d in facts.definitions:
                nodes.append(Node(_node_id(module, d.qualname), d.kind, module, d.qualname, facts.path, d.start, d.end,
                                  d.signature, d.body_hash, d.redefinitions, external[d.qualname],
                                  tuple(sorted(unresolved[d.qualname].items()))))
        return Snapshot(self.revision, self.scope, self.import_root, EXTRACTOR, EXTRACTOR_VERSION,
                        f'{sys.version_info.major}.{sys.version_info.minor}',
                        tuple(sorted(nodes, key=lambda node: node.id)),
                        tuple(Edge(source, target, kind, tuple(sorted(lines)))
                              for (source, target, kind), lines in sorted(edges.items())),
                        tuple(sorted(self.unparsed, key=lambda item: item.path)))

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
