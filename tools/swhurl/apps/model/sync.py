"""Sync: what the platform derives, written from the records (make app-sync and every edit command).

Reads every instance's records (from its manifests until the cutover), gives an instance without an
egress port the lowest free one (allocation is an operation's, never rendering's), renders, and writes
each file whose parsed content differs from the file on disk. A file that is the same objects in other
bytes is left alone, so an edit's diff shows what it changed.
"""
from __future__ import annotations

import dataclasses
from pathlib import Path

from swhurl import yamlio
from swhurl.apps import egress, events
from swhurl.apps.instance import ENVIRONMENTS, UNITS
from swhurl.apps.model import legacy, render
from swhurl.apps.model.draft import Platform, RenderError, World
from swhurl.apps.model.shadow import normal
from swhurl.apps.yaml_file import EditError

RELOADER_FILE = Path('platform/reloader/helmrelease.yaml')


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
        return [normal(doc) for doc in yamlio.load_all(path.read_text())] == [normal(doc) for doc in yamlio.load_all(text)]
    except Exception:  # unreadable YAML on disk: write the rendered file
        return False


def plan(root: Path, world: World, platform: Platform) -> tuple[dict[Path, str], list[Path]]:
    """``(files to write, files to delete)``, by absolute path."""
    writes: dict[Path, str] = {}
    deletions: list[Path] = []
    try:
        for each in world:
            rendered = render.instance(each, world, platform)
            folder = root / platform.base / each.app / each.env
            for path, documents in rendered.items():
                target = root / path
                if path.parts[:len(UNITS.parts)] == UNITS.parts and not target.is_file():
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


def sync(root: Path, base: Path = Path('apps')) -> list[Path]:
    """Write what the platform derives for every instance under ``root``; return the files changed."""
    from swhurl.apps.new import watch_namespace

    world, problems = legacy.world(root, base)
    if problems:
        raise EditError('; '.join(problems))
    platform = legacy.platform(root, base)
    world = allocate(world, platform)
    writes, deletions = plan(root, world, platform)
    for path, text in writes.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
    for path in deletions:
        path.unlink()
    changed = [*writes, *deletions]
    reloader = root / RELOADER_FILE
    if reloader.is_file():
        before = reloader.read_text()
        for namespace in sorted(render.shared(world, platform).reloaded):
            watch_namespace(root, namespace)
        if reloader.read_text() != before:
            changed.append(reloader)
    return changed

