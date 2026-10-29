"""Render: the records of every instance become each instance's files and the shared files.

Pure: no network, no clock, no checkout writes. The order of the capabilities below is the order
their variables, references and dependencies appear in."""
from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass
from pathlib import Path

from swhurl import interface, yamlio
from swhurl.apps.contract import BACKUP_FILE, IMAGE_AUTOMATION_FILE, NETWORK_POLICY_FILE, QUOTA_FILE
from swhurl.apps.instance import UNITS, Instance
from swhurl.apps.model import (
    alerts,
    backup,
    calls,
    egress,
    events,
    exposure,
    offers,
    secrets,
    sqlite,
    telemetry,
    workload,
)
from swhurl.apps.model.draft import APP_SECRET, Platform, World

REGISTRY_FILE = UNITS / 'kustomization.yaml'
RELOADER_FILE = Path('platform/reloader/helmrelease.yaml')
MANAGED_FILES = frozenset({'namespace.yaml', 'helmrelease.yaml', 'kustomization.yaml', QUOTA_FILE,
                           NETWORK_POLICY_FILE, IMAGE_AUTOMATION_FILE, BACKUP_FILE, 'events.yaml'})



def fingerprint(files: dict[Path, list[dict]]) -> str:
    """Hash paths and rendered objects before adding the hash itself; YAML formatting is immaterial."""
    payload = {path.as_posix(): documents for path, documents in files.items()}
    return hashlib.sha256(json.dumps(payload, sort_keys=True, separators=(',', ':')).encode()).hexdigest()


def text(documents: list[dict]) -> str:
    """A file's bytes: the documents in the order rendered, long values on one line."""
    return yamlio.dump_all(documents, wrap=False)


def instance(each: Instance, world: World, platform: Platform) -> dict[Path, list[dict]]:
    """One instance's files, by path from the root: its folder's files and its Flux unit."""
    entry = world[each]
    draft = workload.start(each, entry, platform)
    exposure.apply(draft, entry)
    workload.automation(draft, entry)
    secrets.apply(draft, entry)
    offers.apply(draft, entry)
    telemetry.apply(draft, entry)
    sqlite.apply(draft, entry)
    calls.apply(draft, entry, world)
    events.apply(draft, entry, world)
    calls.admit(draft)
    egress.apply(draft, entry, platform)
    alerts.apply(draft, entry)
    backup.apply(draft, entry, platform)
    folder = platform.base / each.app / each.env
    found = {folder / name: documents for name, documents in workload.finish(draft, entry).items()}
    found[UNITS / f'{each.unit}.yaml'] = [draft.unit]
    draft.annotate(draft.release, interface.RENDER_ANNOTATION, fingerprint(found))
    return found


@dataclass(frozen=True)
class Shared:
    """What every instance contributes to: the namespaces Reloader watches, and the app units registered.

    The NATS user list is not here: v2 renders it from the snapshot (platform/nats/users.json, v2/check-rendered.sh).
    """
    reloaded: frozenset[str]  # the namespaces Reloader watches for the apps
    units: frozenset[str]  # the app unit files registered in clusters/home/kustomization.yaml


def shared(world: World, platform: Platform) -> Shared:
    reloaded = frozenset(each.namespace for each, entry in world.items()
                         if events.keyed(entry) or entry.release.secrets or (APP_SECRET in entry.encrypted or APP_SECRET in entry.pending))
    return Shared(reloaded, frozenset(f'{each.unit}.yaml' for each in world))
