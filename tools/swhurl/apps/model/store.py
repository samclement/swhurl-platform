"""The records in the checkout: what every instance is rendered from (docs/plan.md section 24).

    apps/<app>/app.yaml              App: team and alerts, the same in every environment
    apps/<app>/<env>/release.yaml    Release: the image and what it needs and offers
    apps/<app>/<env>/instance.yaml   Settings: what the platform grants this environment, and allocations

Commands change these and render the rest (sync.py); nothing reads a grant back from a rendered file.
None of the three is listed in a kustomization.yaml, so Flux never applies them.
"""
from __future__ import annotations

from pathlib import Path

import yaml

from swhurl import yamlio
from swhurl.apps import egress, events
from swhurl.apps.calling import signing_keys
from swhurl.apps.contract import SECRET_FILE
from swhurl.apps.instance import Instance
from swhurl.apps.model.draft import APP_SECRET, EVENTS_SECRET, Entry, Platform, World
from swhurl.apps.model.records import (
    APP_FILE,
    RELEASE_FILE,
    SETTINGS_FILE,
    App,
    RecordError,
    Release,
    Settings,
)

HEADER = '# Written by the platform\'s commands (make app-*): docs/apps.md. Rendered files follow from it.\n'


def platform(root: Path, base: Path = Path('apps')) -> Platform:
    def key(path: Path) -> str:
        return (root / path).read_text().strip() if (root / path).is_file() else ''
    return Platform(signing_keys(root), (root / egress.PROXY_FILE).parent.is_dir(),
                    key(events.CONTROLLER_KEY_FILE), key(events.CONSOLE_KEY_FILE), base)


def _load(path: Path) -> object:
    return yamlio.load(path.read_text())


def recorded(folder: Path) -> list[Instance]:
    """The instances under ``folder`` (``<root>/apps``): each ``<app>/<env>`` with a HelmRelease or a Release
    record, in order. A merged first promotion has its records before main renders it."""
    paths = {path.parent for name in ('helmrelease.yaml', RELEASE_FILE) for path in folder.glob(f'*/*/{name}')}
    found = (Instance.named(path.parent.name, path.name) for path in sorted(paths))
    return [each for each in found if each]


def world(root: Path, base: Path = Path('apps')) -> tuple[World, list[str]]:
    """Every instance's records under ``root``, and why any could not be read."""
    found: World = {}
    problems: list[str] = []
    apps: dict[str, App | None] = {}
    for each in recorded(root / base):
        folder = each.folder(root, base)
        try:
            if each.app not in apps:
                apps[each.app] = App.parse(_load(root / base / each.app / APP_FILE), f'{each.app}/{APP_FILE}')
            app = apps[each.app]
            if app is None:
                continue
            release = Release.parse(_load(folder / RELEASE_FILE), f'{each}: {RELEASE_FILE}')
            settings = Settings.parse(_load(folder / SETTINGS_FILE), f'{each}: {SETTINGS_FILE}')
        except (OSError, RecordError, yaml.YAMLError) as error:
            apps.setdefault(each.app, None)
            problems.append(f'{each}: cannot read its records: {error}')
            continue
        encrypted = frozenset(name for name, file in ((EVENTS_SECRET, events.SECRET_FILE), (APP_SECRET, SECRET_FILE))
                              if (folder / file).is_file())
        found[each] = Entry(app, release, settings, encrypted)
    releases: dict[str, tuple[Instance, Release]] = {}
    for each, entry in found.items():
        reference, release = releases.setdefault(each.app, (each, entry.release))
        for field in ('kind', 'command'):
            if getattr(entry.release, field) != getattr(release, field):
                problems.append(f'{each}: release.yaml: {field} must match {reference}; '
                                'an app has the same workload kind and start command in every environment')
    return found, problems


def text(document: dict) -> str:
    return HEADER + yamlio.dump(document, wrap=False)


def documents(world: World, base: Path = Path('apps')) -> dict[Path, str]:
    """The record files of ``world``, by path from the root."""
    found: dict[Path, str] = {}
    for each, entry in world.items():
        folder = base / each.app / each.env
        found[base / each.app / APP_FILE] = text(entry.app.document())
        found[folder / RELEASE_FILE] = text(entry.release.document())
        found[folder / SETTINGS_FILE] = text(entry.settings.document())
    return found
