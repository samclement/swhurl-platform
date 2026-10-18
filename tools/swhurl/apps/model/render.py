"""Render: the records of every instance become each instance's files and the shared files.

Pure: no network, no clock, no checkout writes. The order of the capabilities below is the order
their variables, references and dependencies appear in."""
from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from swhurl.apps.instance import UNITS, Instance
from swhurl.apps.model import alerts, calls, egress, events, exposure, offers, secrets, sqlite, telemetry, workload
from swhurl.apps.model.draft import Platform, World


def instance(each: Instance, world: World, platform: Platform) -> dict[Path, list[dict]]:
    """One instance's files, by path from the root: its folder's files and its Flux unit."""
    entry = world[each]
    draft = workload.start(each, entry, platform)
    exposure.apply(draft, entry)
    workload.automation(draft, entry, platform)
    secrets.apply(draft, entry)
    calls.apply(draft, entry, world)
    events.apply(draft, entry, world)
    egress.apply(draft, entry, platform)
    sqlite.apply(draft, entry)
    telemetry.apply(draft, entry)
    offers.apply(draft, entry)
    alerts.apply(draft, entry)
    folder = platform.base / each.app / each.env
    found = {folder / name: documents for name, documents in workload.finish(draft, entry).items()}
    found[UNITS / f'{each.unit}.yaml'] = [draft.unit]
    return found


@dataclass(frozen=True)
class Shared:
    """What every instance contributes to: None where the root has no such service."""
    nats_users: list[dict] | None
    egress_proxy: list[dict] | None
    reloaded: frozenset[str]  # the namespaces Reloader watches for the apps
    units: frozenset[str]  # the app unit files registered in clusters/home/kustomization.yaml


def shared(world: World, platform: Platform) -> Shared:
    reloaded = frozenset(each.namespace for each, entry in world.items() if events.keyed(entry) or entry.release.secrets)
    return Shared(events.users(world, platform), egress.proxy(world, platform), reloaded,
                  frozenset(f'{each.unit}.yaml' for each in world))
