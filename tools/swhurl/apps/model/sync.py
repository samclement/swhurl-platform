"""Sync: what the platform derives, written from the records (make app-sync and every edit command).

Reads every instance's records (store.py), gives an instance without an egress port the lowest free
one (allocation is an operation's, never rendering's), renders, and writes the records and each file
whose parsed content differs from the file on disk. A file that is the same objects in other bytes is
left alone, so an edit's diff shows what it changed.
"""
from __future__ import annotations

import dataclasses
from collections.abc import Callable
from pathlib import Path

import yaml

from swhurl import yamlio
from swhurl.apps import channels, egress, events, offers
from swhurl.apps.contract import SECRET_FILE
from swhurl.apps.instance import ENVIRONMENTS, UNITS, Instance
from swhurl.apps.model import generated, render, store
from swhurl.apps.model.draft import Entry, Platform, RenderError, World
from swhurl.apps.model.records import APP_FILE, RECORD_FILES
from swhurl.apps.model.render import MANAGED_FILES, REGISTRY_FILE, RELOADER_FILE
from swhurl.apps.yaml_file import EditError, YamlFile, mapping


def allocate(world: World, platform: Platform) -> World:
    """Each instance without an egress port gets the lowest free one, staging's before production's."""
    if not platform.egress:
        return world
    taken = {entry.settings.allocations.egress_port for entry in world.values()} - {None}
    found = dict(world)
    for each in (each for env in ENVIRONMENTS for each in sorted(world, key=lambda i: i.app) if each.env == env):
        entry = found[each]
        if entry.settings.allocations.egress_port is None:
            port = egress.free_port(taken)
            taken.add(port)
            allocations = dataclasses.replace(entry.settings.allocations, egress_port=port)
            found[each] = dataclasses.replace(entry, settings=dataclasses.replace(entry.settings, allocations=allocations))
    return found


def _same(path: Path, text: str) -> bool:
    if not path.is_file():
        return False
    try:
        return [generated.normal(doc) for doc in yamlio.load_all(path.read_text())] == [generated.normal(doc) for doc in yamlio.load_all(text)]
    except Exception:  # unreadable YAML on disk: write the rendered file
        return False


@dataclasses.dataclass(frozen=True)
class Prepared:
    """A rendered change not yet written: the records and every file to write or delete."""
    world: World
    platform: Platform
    writes: dict[Path, str]
    deletions: list[Path]


def check_removal(each: Instance, world: World) -> None:
    """Explicit removals and merged orphan cleanup have the same relationship preconditions."""
    others = [(other.app, entry) for other, entry in world.items() if other.env == each.env and other.app != each.app]
    callers = sorted(name for name, entry in others if each.app in entry.settings.calls)
    if callers:
        raise EditError(f'{each} is called by {", ".join(callers)}: remove those calls first '
                        f'(make app-calls APP=<caller> ENV={each.env} ARGS="--remove {each.app}")')
    subscribers = sorted(name for name, entry in others if any(publisher == each.app for publisher, _ in entry.settings.subscribed))
    if subscribers:
        raise EditError(f'{each} publishes events that {", ".join(subscribers)} subscribe to: '
                        f'remove those subscriptions first (make app-events APP=<subscriber> ENV={each.env})')


def plan(root: Path, world: World, platform: Platform, added: frozenset[Instance] = frozenset(), *,
         register: bool = True, routes: dict | None = None) -> Prepared:
    """Complete writes and managed deletions, by absolute path. Plan everything before applying anything.
    Scratch roots keep existing units; creates always get a unit. With a registry, sync repairs missing units.
    ``register=False`` leaves the registry alone, not the new unit (app-new --no-register)."""
    units = {each: path for path in (root / UNITS).glob('app-*.yaml') if (each := Instance.of_unit(path.stem))}
    stale = {each for folder in (root / platform.base).glob('*/*')
             if (each := Instance.named(folder.parent.name, folder.name)) and folder.is_dir()
             and any((folder / name).is_file() for name in MANAGED_FILES)}
    removed = (stale | units.keys() | set(store.recorded(root / platform.base))) - world.keys()
    for each in sorted(removed, key=str):
        check_removal(each, world)
    world = allocate(world, platform)
    writes: dict[Path, str] = {}
    deletions: list[Path] = []

    def keep(path: Path, text: str) -> None:
        path = root / path
        if not path.is_file() or path.read_text() != text:
            writes[path] = text

    try:
        for each in world:
            rendered = render.instance(each, world, platform)
            folder = each.folder(root, platform.base)
            for path, documents in rendered.items():
                target = root / path
                if path.parent == UNITS and not target.is_file() and each not in added and not (root / REGISTRY_FILE).is_file():
                    continue
                text = render.text(documents)
                if not _same(target, text):
                    writes[target] = text
            deletions += [folder / name for name in sorted(MANAGED_FILES)
                          if folder / name not in {root / path for path in rendered} and (folder / name).is_file()]
        shared = render.shared(world, platform)
        for path, text in store.documents(world, platform.base).items():
            keep(path, text)
    except RenderError as error:
        raise EditError(str(error)) from None
    for path, text in ((events.USERS_FILE, shared.nats_users is not None and events.users_configmap(shared.nats_users)),
                       (egress.PROXY_FILE, shared.egress_proxy is not None and egress.proxy_text(shared.egress_proxy))):
        if text:
            keep(path, text)
    for path, wanted, key, managed in (
            (REGISTRY_FILE, shared.units, 'resources', lambda name: Instance.of_unit(str(name).removesuffix('.yaml'))),
            (RELOADER_FILE, shared.reloaded, 'namespaces', Instance.of_namespace)):
        if not (root / path).is_file() or (path == REGISTRY_FILE and not register):
            continue
        document = YamlFile(root / path)
        parent = document.data
        if path == RELOADER_FILE:
            for field in ('spec', 'values', 'reloader'):
                parent = mapping(parent, field)
        current = parent.get(key)
        if (not isinstance(current, list) or getattr(current.yaml_anchor(), 'value', None)
                or any(not isinstance(name, str) for name in current)):
            raise EditError(f'{path}: expected an ordinary {key} list')
        before = list(current)
        for index in reversed(range(len(current))):
            if managed(current[index]) and current[index] not in wanted:
                del current[index]
        current.extend(sorted(wanted - set(current)))
        if list(current) != before:
            keep(path, document.render())
    try:
        if (root / channels.NOTIFY_DIR).is_dir():
            keep(channels.ROUTES_FILE, channels.routes_configmap(channels.routes(root) if routes is None else routes))
    except (OSError, UnicodeError, yaml.YAMLError, AttributeError, TypeError) as error:
        # Parser details can include Secret contents: report the input boundary, never their text.
        raise EditError(f'cannot plan notification routes: invalid team/channel inputs ({type(error).__name__})') from None
    # A merged record-only removal leaves old outputs behind. Only platform-owned names are removed;
    # unrelated YAML, encrypted files, notes and data in the same directories are not ours.
    for each in sorted(removed, key=str):
        folder = each.folder(root, platform.base)
        names = MANAGED_FILES | RECORD_FILES | {events.SECRET_FILE, SECRET_FILE}
        deletions += [folder / name for name in sorted(names) if (folder / name).is_file()]
        source = folder / offers.OFFERS_DIR / offers.SOURCE_FILE
        if source.is_file():
            try:
                offered = offers.Source.parse(source.read_text())
            except offers.OfferError as error:
                raise EditError(f'{source}: {error}') from None
            deletions += [source, *(source.parent / name for name in (offered.api, offered.events, offered.flags)
                                    if name and '/' not in name and (source.parent / name).is_file())]
    deletions += [units[each] for each in sorted(removed, key=str) if each in units]
    deletions += [path for path in sorted((root / platform.base).glob(f'*/{APP_FILE}'))
                  if not any(each.app == path.parent.name for each in world)]
    return Prepared(world, platform, writes, list(dict.fromkeys(deletions)))


def apply(root: Path, prepared: Prepared) -> list[Path]:
    """Write an already validated plan; no reading, parsing or rendering after writes begin."""
    for path, text in prepared.writes.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
    for path in prepared.deletions:
        path.unlink()
    for folder in sorted({parent for path in prepared.deletions for parent in path.parents
                          if root / prepared.platform.base in parent.parents}, reverse=True):
        if folder.is_dir() and not any(folder.iterdir()):
            folder.rmdir()
    return [*prepared.writes, *prepared.deletions]


def read(root: Path, base: Path = Path('apps')) -> tuple[World, Platform]:
    world, problems = store.world(root, base)
    if problems:
        raise EditError('; '.join(problems))
    return world, store.platform(root, base)


def sync(root: Path, base: Path = Path('apps')) -> list[Path]:
    """Reconcile records, including merged creates and removals, and all shared files."""
    world, platform = read(root, base)
    return apply(root, plan(root, world, platform))


def guard(root: Path, world: World, platform: Platform, targets: list[Instance], base: Path = Path('apps')) -> None:
    """Refuse instances whose files are not what their records render: a hand edit the model cannot
    express would be lost by writing them."""
    held = [line for each in targets for line in generated.instance_problems(root, each, world, platform, base)]
    if held:
        shown = '; '.join(held[:5]) + (f'; and {len(held) - 5} more' if len(held) > 5 else '')
        raise EditError(f'the files differ from what the platform renders, so a command cannot change them '
                        f'(make check-apps lists every difference): {shown}')


@dataclasses.dataclass(frozen=True)
class Create:
    entry: Entry


@dataclasses.dataclass(frozen=True)
class Update:
    update: Callable[[Entry], Entry]


@dataclasses.dataclass(frozen=True)
class Remove:
    pass


def prepare(root: Path, operations: dict[Instance, Create | Update | Remove], base: Path = Path('apps'), *,
            register: bool = True, routes: dict | None = None) -> Prepared:
    """Validate all targets, transform their records, then plan every output before writing.
    Updates guard generated files against unrepresentable hand edits. Removal needs only valid records:
    its managed outputs may already be stale, and unrelated files are retained."""
    world, platform = read(root, base)
    for each, operation in operations.items():
        if isinstance(operation, Create) and each in world:
            raise EditError(f'{each} already exists')
        if not isinstance(operation, Create) and each not in world:
            raise EditError(f'no instance {each}')
    for each, operation in operations.items():
        if isinstance(operation, Remove):
            check_removal(each, world)
    guard(root, world, platform, [each for each, op in operations.items() if isinstance(op, Update)], base)
    for each, operation in operations.items():
        if isinstance(operation, Remove):
            del world[each]
        else:
            world[each] = operation.entry if isinstance(operation, Create) else operation.update(world[each])
    added = frozenset(each for each, op in operations.items() if isinstance(op, Create))
    return plan(root, world, platform, added, register=register, routes=routes)


def change(root: Path, targets: Instance | list[Instance], update: Callable[[Entry], Entry],
           base: Path = Path('apps')) -> list[Path]:
    """``prepare``, then write what changed; return the files changed."""
    targets = [targets] if isinstance(targets, Instance) else targets
    return apply(root, prepare(root, {each: Update(update) for each in targets}, base))
