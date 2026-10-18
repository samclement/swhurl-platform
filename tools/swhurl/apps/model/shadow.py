"""The shadow check (docs/plan.md section 24, stage 1): read every instance's records from its
manifests, render them, and compare the result with what is committed, as parsed objects.

A difference means the model cannot yet describe that instance exactly; make check-apps fails on it.
"""
from __future__ import annotations

import re
from pathlib import Path

from swhurl import yamlio
from swhurl.apps import egress, events
from swhurl.apps.instance import UNITS, Instance
from swhurl.apps.model import legacy, render
from swhurl.apps.model.draft import RenderError

FIXTURES = Path('tests/fixtures/apps')
"""The generated fixtures (tests/fixtures/apps.sh) are a root of their own; their staging does not follow images."""
RELOADER_FILE = Path('platform/reloader/helmrelease.yaml')
REGISTRY_FILE = UNITS / 'kustomization.yaml'
SHOWN = 120


def _normal(document: object) -> object:
    """Kustomize lists resources in no meaningful order: compare them as a set."""
    if isinstance(document, dict) and document.get('apiVersion', '').startswith('kustomize.config.k8s.io'):
        return {**document, 'resources': sorted(document.get('resources') or [])}
    return document


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


def _documents(path: Path) -> list:
    return [_normal(doc) for doc in yamlio.load_all(path.read_text()) if doc is not None]


def _committed(root: Path, folder: Path, unit: Path) -> dict[Path, Path]:
    found = {path.relative_to(root): path for path in sorted(folder.rglob('*.yaml'))
             if not path.name.endswith('.sops.yaml') and 'offers' not in path.relative_to(folder).parts}
    if unit.is_file():
        found[unit.relative_to(root)] = unit
    return found


def _reloader(root: Path) -> set[str] | None:
    path = root / RELOADER_FILE
    if not path.is_file():
        return None
    match = re.search(r'^\s*namespaces:\s*\[([^\]]*)\]', path.read_text(), re.M)
    return {name.strip() for name in (match[1] if match else '').split(',') if name.strip()}


def problems(root: Path, base: Path = Path('apps'), *, auto_deploy: bool = True) -> list[str]:
    world, found = legacy.world(root, base)
    platform = legacy.platform(root, auto_deploy=auto_deploy, base=base)
    for each in world:
        try:
            rendered = render.instance(each, world, platform)
        except RenderError as error:
            found.append(f'{each}: cannot render: {error}')
            continue
        committed = _committed(root, each.folder(root, base), root / UNITS / f'{each.unit}.yaml')
        for path in sorted(set(rendered) | set(committed)):
            if path not in committed:
                found.append(f'{path}: rendered, not committed')
            elif path not in rendered:
                found.append(f'{path}: committed, not rendered')
            else:
                found += [f'{path}: {line}' for line in
                          differences(_documents(committed[path]), [_normal(doc) for doc in rendered[path]])]
    try:
        shared = render.shared(world, platform)
    except RenderError as error:
        return [*found, f'shared files: cannot render: {error}']
    if shared.nats_users is not None and (root / events.USERS_FILE).is_file():
        configmap = yamlio.load((root / events.USERS_FILE).read_text())
        committed_users = yamlio.load(configmap['data']['values.yaml'])['config']['merge']['authorization']['users']
        found += [f'{events.USERS_FILE}: {line}' for line in differences(committed_users, shared.nats_users)]
    if shared.egress_proxy is not None and (root / egress.PROXY_FILE).is_file():
        found += [f'{egress.PROXY_FILE}: {line}' for line in
                  differences(_documents(root / egress.PROXY_FILE), shared.egress_proxy)]
    watched = _reloader(root)
    if watched is not None:
        apps_watched = {name for name in watched if Instance.of_namespace(name)}
        found += [f'{RELOADER_FILE}: {name} is watched, not rendered' for name in sorted(apps_watched - shared.reloaded)]
        found += [f'{RELOADER_FILE}: {name} is rendered, not watched' for name in sorted(shared.reloaded - apps_watched)]
    if (root / REGISTRY_FILE).is_file():
        registered = {str(name) for name in yamlio.load((root / REGISTRY_FILE).read_text()).get('resources') or []
                      if str(name).startswith('app-')}
        found += [f'{REGISTRY_FILE}: {name} is registered, not rendered' for name in sorted(registered - shared.units)]
        found += [f'{REGISTRY_FILE}: {name} is rendered, not registered' for name in sorted(shared.units - registered)]
    return found


def check(root: Path) -> list[str]:
    """The platform's instances, and the generated fixtures where this root has them."""
    found = problems(root)
    if (root / FIXTURES / 'apps').is_dir():
        found += [f'{FIXTURES}: {line}' for line in problems(root / FIXTURES, auto_deploy=False)]
    return found
