"""Prepare, plan and write what the platform derives from records (app-sync and every edit command).

Reads every instance's records (store.py), allocates ports, storage and identities during preparation
(never rendering), and plans ciphertext and rendered files before any write. Writes the records and
each file whose parsed content differs from the file on disk. A file that is the same objects in other bytes is
left alone, so an edit's diff shows what it changed.
"""
from __future__ import annotations

import dataclasses
from collections.abc import Callable
from pathlib import Path

import yaml

from swhurl import interface, yamlio
from swhurl.apps import channels, egress, events, nkeys, offers
from swhurl.apps.contract import RETAINED_STORAGE_CLASS, SECRET_FILE
from swhurl.apps.errors import EditError
from swhurl.apps.instance import ENVIRONMENTS, UNITS, Instance
from swhurl.apps.model import generated, relations, render, store
from swhurl.apps.model.draft import APP_SECRET, EVENTS_SECRET, Entry, Platform, RenderError, World
from swhurl.apps.model.records import APP_FILE, RECORD_FILES, App, RecordError, Release, Settings, Storage
from swhurl.apps.model.render import MANAGED_FILES, REGISTRY_FILE, RELOADER_FILE
from swhurl.apps.yaml_file import YamlFile, mapping
from swhurl.run import CommandError, Runner


def encrypt_secret(root: Path, path: Path, name: str, namespace: str, values: dict[str, str], runner: Runner) -> str:
    """Encrypt in memory using the destination's SOPS rule; neither disk nor errors receive plaintext."""
    for value in values.values():
        runner.add_secret(value)
    text = yamlio.dump({'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name, 'namespace': namespace},
                        'type': 'Opaque', 'stringData': values})
    relative = str(path.relative_to(root))
    try:
        encrypted = runner.run(['sops', '--encrypt', '--filename-override', relative, '--input-type', 'yaml',
                                '--output-type', 'yaml', '/dev/stdin'], input=text, cwd=root, secret_output=True).stdout
    except CommandError as error:
        raise EditError(f'could not SOPS-encrypt {relative}; nothing was changed: {error}') from None
    try:
        document = yamlio.load(encrypted)
        stored = document.get('stringData') if isinstance(document, dict) and 'sops' in document else None
    except yaml.YAMLError:  # parser details may contain plaintext from a failed encryption
        stored = None
    if not isinstance(stored, dict) or stored.keys() != values.keys() or any(
            not isinstance(value, str) or not value.startswith('ENC[') for value in stored.values()):
        raise EditError(f'sops did not encrypt {relative}; nothing was written')
    return encrypted


def identity(each: Instance, entry: Entry) -> bool:
    """Whether the instance has a complete NATS identity. One half alone is an error, never absence."""
    public = bool(entry.settings.allocations.events_key)
    if public != (EVENTS_SECRET in entry.encrypted or EVENTS_SECRET in entry.pending):
        raise EditError(f'{each}: inconsistent NATS identity: public allocation and encrypted seed must both exist or both be absent')
    return public


def allocate(root: Path, world: World, platform: Platform, sizes: dict[Instance, str], runner: Runner) -> World:
    """Choose missing allocations once, staging before production. Partial identities are never absence (``identity``).
    Every instance needs a NATS identity when NATS exists (its user carries its flag permissions), and wherever it uses events."""
    for each, entry in world.items():
        identity(each, entry)
    nats = events.has_nats(root)
    taken = {entry.settings.allocations.egress_port for entry in world.values()} - {None}
    found = {}
    for each in (each for env in ENVIRONMENTS for each in sorted(world, key=lambda i: i.app) if each.env == env):
        entry = world[each]
        allocations, pending = entry.settings.allocations, dict(entry.pending)
        if platform.egress and allocations.egress_port is None:
            port = egress.free_port(taken)
            taken.add(port)
            allocations = dataclasses.replace(allocations, egress_port=port)
        if entry.release.database and allocations.database is None:
            if not sizes.get(each):
                raise EditError(f'{each}: declares a database but has no database allocation; a new volume needs an explicit size')
            allocations = dataclasses.replace(allocations, database=Storage(sizes[each], RETAINED_STORAGE_CLASS))
        if not allocations.events_key and (nats or entry.release.publishes or entry.settings.grants.subscriptions):
            seed, public = nkeys.generate()
            pending[EVENTS_SECRET] = encrypt_secret(root, each.folder(root, platform.base) / events.SECRET_FILE,
                                                  events.secret_name(each.app), each.namespace, {interface.SEED_VARIABLE: seed}, runner)
            allocations = dataclasses.replace(allocations, events_key=public)
        found[each] = dataclasses.replace(entry, settings=dataclasses.replace(entry.settings, allocations=allocations), pending=pending)
    return found


@dataclasses.dataclass(frozen=True)
class Prepared:
    """A rendered change not yet written: the records and every file to write or delete."""
    world: World
    platform: Platform
    writes: dict[Path, str]
    deletions: list[Path]


def check_removal(each: Instance, world: World) -> None:
    """Explicit removals and merged orphan cleanup have the same relationship preconditions."""
    try:
        others = [(other.app, relations.declarations(other, entry)) for other, entry in world.items()
                  if other.env == each.env and other.app != each.app]
    except RenderError as error:
        raise EditError(str(error)) from None
    callers = sorted(name for name, entry in others if any(r.role == 'calls' and r.target == each.app for r in entry))
    if callers:
        raise EditError(f'{each} is called by {", ".join(callers)}: remove those calls first '
                        f'(make app-calls APP=<caller> ENV={each.env} ARGS="--remove {each.app}")')
    subscribers = sorted(name for name, entry in others if any(r.role == 'subscribes' and r.target == each.app for r in entry))
    if subscribers:
        raise EditError(f'{each} publishes events that {", ".join(subscribers)} subscribe to: '
                        f'remove those subscriptions first (make app-events APP=<subscriber> ENV={each.env})')


def removals(root: Path, world: World, platform: Platform) -> frozenset[Instance]:
    """Check merged orphan removals before any allocation or encryption."""
    units = {each for path in (root / UNITS).glob('app-*.yaml') if (each := Instance.of_unit(path.stem))}
    stale = {each for folder in (root / platform.base).glob('*/*')
             if (each := Instance.named(folder.parent.name, folder.name)) and folder.is_dir()
             and any((folder / name).is_file() for name in MANAGED_FILES)}
    removed = (stale | units | set(store.recorded(root / platform.base))) - world.keys()
    for each in sorted(removed, key=str):
        check_removal(each, world)
    return frozenset(removed)


def plan(root: Path, world: World, platform: Platform, added: frozenset[Instance] = frozenset(), *,
         routes: dict | None = None, removed: frozenset[Instance] = frozenset()) -> Prepared:
    """Complete writes and managed deletions, by absolute path. Plan everything before applying anything.
    Scratch roots (no registry) keep existing units; creates always get a unit. With a registry, sync repairs missing units."""
    writes: dict[Path, str] = {}
    deletions: list[Path] = []

    def keep(path: Path, text: str) -> None:
        path = root / path
        if not generated.same(path, text):
            writes[path] = text

    try:
        for each in world:
            rendered = render.instance(each, world, platform)
            folder = each.folder(root, platform.base)
            for name, text in world[each].pending.items():
                keep(folder / {EVENTS_SECRET: events.SECRET_FILE, APP_SECRET: SECRET_FILE}[name], text)
            for path, documents in rendered.items():
                target = root / path
                if path.parent == UNITS and not target.is_file() and each not in added and not (root / REGISTRY_FILE).is_file():
                    continue
                keep(path, render.text(documents))
            deletions += [folder / name for name in sorted(MANAGED_FILES)
                          if folder / name not in {root / path for path in rendered} and (folder / name).is_file()]
        shared = render.shared(world, platform)
        for path, text in store.documents(world, platform.base).items():
            keep(path, text)
    except RenderError as error:
        raise EditError(str(error)) from None
    if shared.nats_users is not None:
        keep(events.USERS_FILE, events.users_configmap(shared.nats_users))
    for path, wanted, key, managed in (
            (REGISTRY_FILE, shared.units, 'resources', lambda name: Instance.of_unit(str(name).removesuffix('.yaml'))),
            (RELOADER_FILE, shared.reloaded, 'namespaces', Instance.of_namespace)):
        if not (root / path).is_file():
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
    deletions += [each.unit_file(root) for each in sorted(removed, key=str) if each.unit_file(root).is_file()]
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
    try:
        return world, store.platform(root, base)
    except RenderError as error:
        raise EditError(str(error)) from None


def sync(root: Path, base: Path = Path('apps'), *, runner: Runner | None = None) -> list[Path]:
    """Reconcile records, including merged creates and removals, and all shared files."""
    return apply(root, prepare(root, {}, base, runner=runner))


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
    database_size: str = ''


@dataclasses.dataclass(frozen=True)
class Update:
    update: Callable[[Entry], Entry]
    database_size: str = ''


@dataclasses.dataclass(frozen=True)
class Remove:
    pass


def _cleared(entry: Entry) -> Entry:
    allocations = dataclasses.replace(entry.settings.allocations, events_key='')
    return dataclasses.replace(entry, settings=dataclasses.replace(entry.settings, allocations=allocations),
                               encrypted=entry.encrypted - {EVENTS_SECRET},
                               pending={kind: text for kind, text in entry.pending.items() if kind != EVENTS_SECRET})


@dataclasses.dataclass(frozen=True)
class Rotate(Update):
    """Replace an existing complete NATS identity: an update that clears it, so allocation makes a new pair."""
    update: Callable[[Entry], Entry] = _cleared


def prepare(root: Path, operations: dict[Instance, Create | Update | Remove], base: Path = Path('apps'), *,
            routes: dict | None = None, runner: Runner | None = None) -> Prepared:
    """Validate all targets, transform their records, then plan every output before writing.
    Updates (rotations included) guard generated files against unrepresentable hand edits; a rotation also
    needs a complete identity, and something that still needs one. Removal needs only valid records:
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
        elif isinstance(operation, Rotate) and not identity(each, world[each]):
            raise EditError(f'{each}: rotation requires an existing complete NATS identity')
    guard(root, world, platform, [each for each, op in operations.items() if isinstance(op, Update)], base)
    for each, operation in operations.items():
        if isinstance(operation, Remove):
            del world[each]
        else:
            world[each] = operation.entry if isinstance(operation, Create) else operation.update(world[each])
    try:  # a command cannot build a record that would not parse: the records check their own grammar
        for each, operation in operations.items():
            if not isinstance(operation, Remove):
                entry = world[each]
                App.parse(entry.app.document()), Release.parse(entry.release.document()), Settings.parse(entry.settings.document())
    except RecordError as error:
        raise EditError(f'{each}: {error}') from None
    added = frozenset(each for each, op in operations.items() if isinstance(op, Create))
    removed = removals(root, world, platform)
    sizes = {each: op.database_size for each, op in operations.items() if not isinstance(op, Remove)}
    try:
        for each in world:
            relations.evaluate(each, world)
    except RenderError as error:
        raise EditError(str(error)) from None
    world = allocate(root, world, platform, sizes, runner or Runner())
    for each, operation in operations.items():
        if isinstance(operation, Rotate) and not world[each].settings.allocations.events_key:
            raise EditError(f'{each}: nothing needs a NATS identity (no NATS, no events), so there is none to rotate')
    return plan(root, world, platform, added, routes=routes, removed=removed)


def change(root: Path, targets: Instance | list[Instance], update: Callable[[Entry], Entry],
           base: Path = Path('apps'), *, runner: Runner | None = None) -> list[Path]:
    """``prepare``, then write what changed; return the files changed."""
    targets = [targets] if isinstance(targets, Instance) else targets
    return apply(root, prepare(root, {each: Update(update) for each in targets}, base, runner=runner))
