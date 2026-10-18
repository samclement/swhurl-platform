"""Edit accessors for an instance's files, and make app-sync.

What follows from an instance's declarations (identity, calls, events, egress, isolation) is rendered
by the app model (apps/model, docs/plan.md section 24): ``sync_env`` reads every instance's records,
renders them and writes each file whose content changed, before any is written if a render fails.

The strict accessors at the top read a HelmRelease for editing in place (comments kept, for
app-promote) and refuse shapes they do not understand; reading only is helmrelease.py.
"""
from __future__ import annotations

from pathlib import Path

from swhurl.apps import channels, events, helmrelease
from swhurl.apps.calling import (
    declared_calls,
)
from swhurl.apps.instance import (
    ENVIRONMENTS,
    INSTANCE_ROOTS,
    NAME_RE,
    instance_folder,
    instances,
)
from swhurl.apps.yaml_file import EditError, YamlFile, mapping


def instance_dir(root: Path, app: str, env: str) -> Path:
    if not NAME_RE.match(app) or env not in ENVIRONMENTS:
        raise EditError(f'no instance {app}/{env}: APP must be a DNS label and ENV one of {", ".join(ENVIRONMENTS)}')
    path = instance_folder(root, app, env)
    if not (path / 'helmrelease.yaml').is_file():
        raise EditError(f'no instance {app}/{env} (missing {path.relative_to(root)}/helmrelease.yaml)')
    return path

def load_release(instance: Path) -> dict:
    return YamlFile(instance / 'helmrelease.yaml').data

def values(release: dict) -> dict:
    return mapping(mapping(release, 'spec'), 'values')

def controller(release: dict) -> dict:
    return mapping(mapping(values(release), 'controllers'), 'main')

def container(release: dict) -> dict:
    return mapping(mapping(controller(release), 'containers'), 'main')

def instances_in(root: Path, env: str) -> list[str]:
    return sorted(each.app for each in instances(root / INSTANCE_ROOTS[0]) if each.env == env)

def sync_env(root: Path, env: str | None = None) -> list[Path]:
    """Rewrite what the platform derives, for every instance (``env`` is no longer needed: rendering
    reads every environment); return the files changed. The app model renders it (apps/model/sync.py)."""
    from swhurl.apps.model import sync as model_sync

    return model_sync.sync(root)

def sync(root: Path) -> Path | None:
    changed = sync_env(root)
    routes = channels.routes_file(root)
    if routes and (not routes[0].is_file() or routes[0].read_text() != routes[1]):
        routes[0].write_text(routes[1])
        changed.append(routes[0])
    for path in changed:
        print(f'[OK] rewrote {path.relative_to(root)}')
    if not changed:
        print('[OK] every instance already has what the platform derives')
    return None

def subscribers_of(root: Path, app: str, env: str) -> list[str]:
    """The other instances of ``env`` that subscribe to an event of ``app``."""
    found = []
    for other in instances_in(root, env):
        release = YamlFile(instance_folder(root, other, env) / 'helmrelease.yaml').data
        if other != app and any(publisher == app for publisher, _ in events.subscribed(helmrelease.variables(release))):
            found.append(other)
    return found

def callers_of(root: Path, app: str, env: str) -> list[str]:
    """The other instances of ``env`` that declare a call to ``app``."""
    found = []
    for other in instances_in(root, env):
        release = YamlFile(instance_folder(root, other, env) / 'helmrelease.yaml').data
        if other != app and app in declared_calls(helmrelease.variables(release)):
            found.append(other)
    return found
