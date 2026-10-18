"""Sync: what the platform derives, written from the records (make app-sync and every edit command).

Reads every instance's records (from its manifests until the cutover), gives an instance without an
egress port the lowest free one (allocation is an operation's, never rendering's), renders, and writes
each file whose parsed content differs from the file on disk. A file that is the same objects in other
bytes is left alone, so an edit's diff shows what it changed.
"""
from __future__ import annotations

import dataclasses
from collections.abc import Callable
from pathlib import Path

from swhurl import yamlio
from swhurl.apps import egress, events
from swhurl.apps.instance import ENVIRONMENTS, UNITS, Instance
from swhurl.apps.model import legacy, render, shadow
from swhurl.apps.model.draft import Entry, Platform, RenderError, World
from swhurl.apps.yaml_file import EditError

RELOADER_FILE = Path('platform/reloader/helmrelease.yaml')
REGISTRY_FILE = UNITS / 'kustomization.yaml'


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
        return [shadow.normal(doc) for doc in yamlio.load_all(path.read_text())] == [shadow.normal(doc) for doc in yamlio.load_all(text)]
    except Exception:  # unreadable YAML on disk: write the rendered file
        return False


def plan(root: Path, world: World, platform: Platform, added: frozenset[Instance] = frozenset()) -> tuple[dict[Path, str], list[Path]]:
    """``(files to write, files to delete)``, by absolute path. An ``added`` instance gets its unit file."""
    writes: dict[Path, str] = {}
    deletions: list[Path] = []
    try:
        for each in world:
            rendered = render.instance(each, world, platform)
            folder = root / platform.base / each.app / each.env
            for path, documents in rendered.items():
                target = root / path
                if path.parts[:len(UNITS.parts)] == UNITS.parts and not target.is_file() and each not in added:
                    continue  # a tree without its units (tests) has none to keep
                text = render.text(documents)
                if not _same(target, text):
                    writes[target] = text
            kept = {root / path for path in rendered}
            deletions += [path for path in sorted(folder.glob('*.yaml'))
                          if path not in kept and not path.name.endswith('.sops.yaml')]
        shared = render.shared(world, platform)
    except RenderError as error:
        raise EditError(str(error)) from None
    for path, text in ((events.USERS_FILE, shared.nats_users is not None and events.users_configmap(shared.nats_users)),
                       (egress.PROXY_FILE, shared.egress_proxy is not None and egress.proxy_text(shared.egress_proxy))):
        if text and not ((root / path).is_file() and (root / path).read_text() == text):
            writes[root / path] = text
    return writes, deletions


@dataclasses.dataclass(frozen=True)
class Prepared:
    """A rendered change not yet written: the records and every file to write or delete."""
    world: World
    platform: Platform
    writes: dict[Path, str]
    deletions: list[Path]
    added: frozenset[Instance] = frozenset()  # new instances: their units are registered


def render_all(root: Path, world: World, platform: Platform, added: frozenset[Instance] = frozenset()) -> Prepared:
    world = allocate(world, platform)
    return Prepared(world, platform, *plan(root, world, platform, added), added)


def apply(root: Path, prepared: Prepared, *, register: bool = True) -> list[Path]:
    """Write a prepared change, registering any new instance's unit unless not ``register``; return the files changed."""
    from swhurl.apps.new import watch_namespace

    for path, text in prepared.writes.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
    for path in prepared.deletions:
        path.unlink()
    changed = [*prepared.writes, *prepared.deletions]
    if register and (root / REGISTRY_FILE).is_file():
        from swhurl.apps.new import GenerationError
        from swhurl.apps.new import register as add_unit
        for each in sorted(prepared.added, key=str):
            before = (root / REGISTRY_FILE).read_text()
            try:
                add_unit(root, f'{each.unit}.yaml')
            except GenerationError as error:
                raise EditError(str(error)) from None
            if (root / REGISTRY_FILE).read_text() != before:
                changed.append(root / REGISTRY_FILE)
    reloader = root / RELOADER_FILE
    if reloader.is_file():
        before = reloader.read_text()
        for namespace in sorted(render.shared(prepared.world, prepared.platform).reloaded):
            watch_namespace(root, namespace)
        if reloader.read_text() != before:
            changed.append(reloader)
    return changed


def write(root: Path, world: World, platform: Platform) -> list[Path]:
    """Render ``world`` and write what changed, after every file has rendered; return the files changed."""
    return apply(root, render_all(root, world, platform))


def read(root: Path, base: Path = Path('apps')) -> tuple[World, Platform]:
    world, problems = legacy.world(root, base)
    if problems:
        raise EditError('; '.join(problems))
    return world, legacy.platform(root, base)


def sync(root: Path, base: Path = Path('apps')) -> list[Path]:
    """Write what the platform derives for every instance under ``root``; return the files changed."""
    return write(root, *read(root, base))


def guard(root: Path, world: World, platform: Platform, targets: list[Instance], base: Path = Path('apps')) -> None:
    """Refuse instances whose files are not what their records render: a hand edit the model cannot
    express would be lost by writing them."""
    held = [line for each in targets for line in shadow.instance_problems(root, each, world, platform, base)]
    if held:
        shown = '; '.join(held[:5]) + (f'; and {len(held) - 5} more' if len(held) > 5 else '')
        raise EditError(f'the files differ from what the platform renders, so a command cannot change them '
                        f'(make check-apps lists every difference): {shown}')


def prepare(root: Path, targets: Instance | list[Instance], update: Callable[[Entry | None], Entry],
            base: Path = Path('apps'), *, adding: bool = False) -> Prepared:
    """One operation on some instances' records, rendered but not written: ``update`` applied to each
    (given None for an instance being ``adding``). Refused when a target exists and its files are not
    what its records render (guard), or when any instance fails to render."""
    world, platform = read(root, base)
    targets = [targets] if isinstance(targets, Instance) else targets
    for each in targets:
        if adding and each in world:
            raise EditError(f'{each} already exists')
        if not adding and each not in world:
            raise EditError(f'no instance {each}')
    guard(root, world, platform, [each for each in targets if each in world], base)
    for each in targets:
        world[each] = update(world.get(each))
    return render_all(root, world, platform, frozenset(targets) if adding else frozenset())


def change(root: Path, targets: Instance | list[Instance], update: Callable[[Entry], Entry],
           base: Path = Path('apps')) -> list[Path]:
    """``prepare``, then write what changed; return the files changed."""
    return apply(root, prepare(root, targets, update, base))
