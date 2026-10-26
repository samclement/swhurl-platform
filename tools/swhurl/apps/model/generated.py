"""Generated means generated (docs/plan.md section 24): render every instance's records and compare the
result with what is committed, byte for byte (``same``, which make app-sync's writer also uses). Where they
differ, the parsed documents show where.

A difference is a hand edit, or a record changed without rendering: make check-apps fails on it, and
make app-sync writes what the records render.
"""
from __future__ import annotations

from pathlib import Path

import yaml

from swhurl import yamlio
from swhurl.apps import events
from swhurl.apps.contract import teams
from swhurl.apps.instance import UNITS, Instance
from swhurl.apps.model import render, store
from swhurl.apps.model.draft import EVENTS_SECRET, Platform, RenderError, World
from swhurl.apps.model.records import RECORD_FILES, TEAM_GRANT
from swhurl.apps.model.render import REGISTRY_FILE, RELOADER_FILE

FIXTURES = Path('tests/fixtures/apps')
"""The generated fixtures (tests/fixtures/apps.sh) are a root of their own."""
SHOWN = 120


def same(path: Path, text: str) -> bool:
    """The one test that a file already is what the records render: its bytes equal ``text``."""
    try:
        return path.read_text() == text
    except (OSError, UnicodeError):
        return False


def _short(value: object) -> str:
    text = repr(value)
    return text if len(text) <= SHOWN else text[:SHOWN] + '...'


def differences(committed: object, rendered: object, where: str = '') -> list[str]:
    """Where two parsed documents differ, by path."""
    if isinstance(committed, dict) and isinstance(rendered, dict):
        found = []
        for key in [*committed, *(k for k in rendered if k not in committed)]:
            path = f'{where}.{key}' if where else str(key)
            if key not in rendered:
                found.append(f'{path}: committed {_short(committed[key])}, not rendered')
            elif key not in committed:
                found.append(f'{path}: rendered {_short(rendered[key])}, not committed')
            else:
                found += differences(committed[key], rendered[key], path)
        return found
    if isinstance(committed, list) and isinstance(rendered, list) and len(committed) == len(rendered):
        return [line for index, (a, b) in enumerate(zip(committed, rendered, strict=True))
                for line in differences(a, b, f'{where}[{index}]')]
    return [] if committed == rendered else [f'{where or "document"}: committed {_short(committed)}, rendered {_short(rendered)}']


def _documents(text: str) -> list:
    return [doc for doc in yamlio.load_all(text) if doc is not None]


def file_problems(name: Path, path: Path, text: str) -> list[str]:
    """Why the committed ``path`` is not ``text``, by document path; [] when ``same``."""
    if same(path, text):
        return []
    if not path.is_file():
        return [f'{name}: rendered, not committed']
    try:
        committed = _documents(path.read_text())
    except (OSError, UnicodeError, yaml.YAMLError) as error:
        return [f'{name}: cannot read committed YAML: {error}']
    return [f'{name}: {line}' for line in differences(committed, _documents(text))] or \
        [f'{name}: formatted differently from the render; make app-sync rewrites it']


def _committed(root: Path, folder: Path, unit: Path) -> dict[Path, Path]:
    found = {path.relative_to(root): path for path in sorted(folder.rglob('*.yaml'))
             if not path.name.endswith('.sops.yaml') and path.name not in RECORD_FILES
             and 'offers' not in path.relative_to(folder).parts}
    if unit.is_file():
        found[unit.relative_to(root)] = unit
    return found


def instance_problems(root: Path, each: Instance, world: World, platform: Platform, base: Path = Path('apps')) -> list[str]:
    """Where one instance's committed files differ from what its records render."""
    try:
        rendered = render.instance(each, world, platform)
    except RenderError as error:
        return [f'{each}: cannot render: {error}']
    found = []
    committed = _committed(root, each.folder(root, base), root / UNITS / f'{each.unit}.yaml')
    for path in sorted(set(rendered) | set(committed)):
        if path not in committed:
            found.append(f'{path}: rendered, not committed')
        elif path not in rendered:
            found.append(f'{path}: committed, not rendered')
        else:
            found += file_problems(path, committed[path], render.text(rendered[path]))
    return found


def nats_problems(root: Path, world: World, users: list[dict] | None) -> list[str]:
    """The NATS shared output and required identities; also used by the live verifier."""
    found = []
    if users is not None:
        found += [f'{each}: has no NATS key; make app-sync creates it' for each, entry in world.items()
                  if not entry.settings.allocations.events_key or EVENTS_SECRET not in entry.encrypted]
        found += file_problems(events.USERS_FILE, root / events.USERS_FILE, events.users_configmap(users))
    return found


def problems(root: Path, base: Path = Path('apps'), *, only: frozenset[Instance] | None = None) -> list[str]:
    """Validate the records and their rendered files. ``only`` selects instance files, not shared files;
    the whole world's records are still read and validated because they supply each other's dependencies."""
    world, found = store.world(root, base)
    try:
        platform = store.platform(root, base)
    except RenderError as error:
        return [*found, f'platform inputs: {error}']
    registered = teams(root)
    found += [f'{each}: instance.yaml: allows {name}, which is not registered in apps/teams.yaml (rule team)'
              for each, entry in sorted(world.items(), key=lambda item: str(item[0])) for name in entry.settings.grants.allows
              if name.startswith(TEAM_GRANT) and name.removeprefix(TEAM_GRANT) not in registered
              and (only is None or each in only)]
    for each in world:
        if only is None or each in only:
            found += instance_problems(root, each, world, platform, base)
    if only is not None:
        return found
    try:
        shared = render.shared(world, platform)
    except RenderError as error:
        return [*found, f'shared files: cannot render: {error}']
    found += nats_problems(root, world, shared.nats_users)
    if (root / RELOADER_FILE).is_file():
        try:
            watched = yamlio.load((root / RELOADER_FILE).read_text())['spec']['values']['reloader']['namespaces']
            if not isinstance(watched, list) or any(not isinstance(name, str) for name in watched):
                raise ValueError('expected a list of strings')
        except (OSError, UnicodeError, yaml.YAMLError, KeyError, TypeError, ValueError):
            found.append(f'{RELOADER_FILE}: cannot read committed YAML; expected '
                         'spec.values.reloader.namespaces to be a list of strings')
        else:
            apps_watched = {name for name in watched if Instance.of_namespace(name)}
            found += [f'{RELOADER_FILE}: {name} is watched, not rendered' for name in sorted(apps_watched - shared.reloaded)]
            found += [f'{RELOADER_FILE}: {name} is rendered, not watched' for name in sorted(shared.reloaded - apps_watched)]
    if (root / REGISTRY_FILE).is_file():
        registered = {str(name) for name in yamlio.load((root / REGISTRY_FILE).read_text()).get('resources') or []
                      if Instance.of_unit(str(name).removesuffix('.yaml'))}
        found += [f'{REGISTRY_FILE}: {name} is registered, not rendered' for name in sorted(registered - shared.units)]
        found += [f'{REGISTRY_FILE}: {name} is rendered, not registered' for name in sorted(shared.units - registered)]
    return found


def check(root: Path) -> list[str]:
    """The platform's instances, and the generated fixtures where this root has them."""
    found = problems(root)
    if (root / FIXTURES / 'apps').is_dir():
        found += [f'{FIXTURES}: {line}' for line in problems(root / FIXTURES)]
    return found
