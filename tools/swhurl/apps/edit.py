"""Edit generated app instances (Git edits only, like app-new): promote, scale, remove.

    app-promote APP [--from staging] [--to prod]   copy the image (tag and digest) between environments
    app-scale APP ENV [--replicas N] [--cpu Q] [--memory Q] [--memory-limit Q]
    app-remove APP ENV                             delete the instance's files and unregister its unit

Each edits files under the repository root and checks the result against the
app policy; commit and push to apply. A HelmRelease is edited only if it
round-trips unchanged through the generator's YAML writer, so a hand-edited
file is refused rather than reformatted.
"""
from __future__ import annotations

import argparse
import re
import shutil
import sys
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps.contract import ENVIRONMENTS
from swhurl.apps.new import NAME_RE, check_generated, dump

PRUNE_DISABLED = 'kustomize.toolkit.fluxcd.io/prune'
CPU_RE = re.compile(r'^\d+(\.\d+)?m?$')
MEMORY_RE = re.compile(r'^\d+(Ki|Mi|Gi)$')
MAX_REPLICAS = 10


class EditError(Exception):
    pass


def instance_dir(root: Path, app: str, env: str) -> Path:
    if not NAME_RE.match(app) or env not in ENVIRONMENTS:
        raise EditError(f'no instance {app}/{env}: APP must be a DNS label and ENV one of {", ".join(ENVIRONMENTS)}')
    path = root / 'apps' / app / env
    if not (path / 'helmrelease.yaml').is_file():
        raise EditError(f'no instance {app}/{env} (missing {path.relative_to(root)}/helmrelease.yaml)')
    return path


def load_release(instance: Path) -> dict:
    text = (instance / 'helmrelease.yaml').read_text()
    docs = list(yaml.safe_load_all(text))
    if dump(docs) != text or len(docs) != 1:
        raise EditError(f'{instance / "helmrelease.yaml"} was edited by hand (it does not round-trip); edit it yourself')
    return docs[0]


def save_release(instance: Path, release: dict) -> None:
    (instance / 'helmrelease.yaml').write_text(dump([release]))


def container(release: dict) -> dict:
    return release['spec']['values']['controllers']['main']['containers']['main']


def promote(root: Path, app: str, source: str, target: str) -> Path:
    if source == target:
        raise EditError('--from and --to must differ')
    src, dst = instance_dir(root, app, source), instance_dir(root, app, target)
    image = container(load_release(src))['image']
    if not image.get('digest'):
        raise EditError(f'{app}/{source} has no image digest; promote only a digest-pinned image')
    release = load_release(dst)
    current = container(release)['image']
    if current.get('repository') != image.get('repository'):
        raise EditError(f'{app}/{target} uses {current.get("repository")}, not {image.get("repository")}; '
                        'change the repository by hand')
    if (current.get('tag'), current.get('digest')) == (image.get('tag'), image['digest']):
        raise EditError(f'{app}/{target} already runs {image.get("tag")}@{image["digest"]}')
    current.update({k: image[k] for k in ('tag', 'digest') if k in image})
    save_release(dst, release)
    print(f'[OK] {app}/{target}: image {image.get("tag")}@{image["digest"][:19]}… (from {source})')
    return dst


def scale(root: Path, app: str, env: str, *, replicas: int | None = None, cpu: str | None = None,
          memory: str | None = None, memory_limit: str | None = None) -> Path:
    if replicas is None and not (cpu or memory or memory_limit):
        raise EditError('give at least one of --replicas, --cpu, --memory, --memory-limit')
    if replicas is not None and not 0 <= replicas <= MAX_REPLICAS:
        raise EditError(f'--replicas must be 0 to {MAX_REPLICAS}')
    for value, pattern, flag in ((cpu, CPU_RE, '--cpu'), (memory, MEMORY_RE, '--memory'),
                                 (memory_limit, MEMORY_RE, '--memory-limit')):
        if value and not pattern.match(value):
            raise EditError(f'{flag} {value!r} is not a quantity like ' + ('100m or 0.5' if flag == '--cpu' else '64Mi'))
    instance = instance_dir(root, app, env)
    release = load_release(instance)
    controller = release['spec']['values']['controllers']['main']
    resources = container(release).setdefault('resources', {})
    changes = []
    if replicas is not None and controller.get('replicas', 1) != replicas:
        # Keep the key order the generator uses: replicas goes before containers.
        controller_items = {'replicas': replicas, **{k: v for k, v in controller.items() if k != 'replicas'}}
        controller.clear()
        controller.update(controller_items)
        changes.append(f'replicas {replicas}')
    for section, key, value in (('requests', 'cpu', cpu), ('requests', 'memory', memory), ('limits', 'memory', memory_limit)):
        if value and resources.setdefault(section, {}).get(key) != value:
            resources[section][key] = value
            changes.append(f'{section}.{key} {value}')
    if not changes:
        raise EditError(f'{app}/{env} already has those settings')
    save_release(instance, release)
    print(f'[OK] {app}/{env}: {", ".join(changes)}')
    return instance


def remove(root: Path, app: str, env: str) -> None:
    instance = instance_dir(root, app, env)
    namespace_file = instance / 'namespace.yaml'
    keeps_data = namespace_file.exists() and PRUNE_DISABLED in namespace_file.read_text()
    unit_file = root / 'clusters/home' / f'app-{app}-{env}.yaml'
    shutil.rmtree(instance)
    print(f'[OK] removed {instance.relative_to(root)}/')
    if not any((root / 'apps' / app).iterdir()):
        (root / 'apps' / app).rmdir()
    if unit_file.exists():
        unit_file.unlink()
        print(f'[OK] removed {unit_file.relative_to(root)}')
    registry = root / 'clusters/home/kustomization.yaml'
    lines = registry.read_text().splitlines(keepends=True)
    kept = [line for line in lines if line.strip() != f'- {unit_file.name}']
    if kept != lines:
        registry.write_text(''.join(kept))
        print(f'[OK] unregistered {unit_file.name} in clusters/home/kustomization.yaml')
    unwatch_namespace(root, f'{app}-{env}')
    if keeps_data:
        print(f'[WARN] {app}-{env} has a retained volume: Flux keeps its namespace and claim after removal. '
              'Delete the data only on purpose: make destroy-data (docs/operations.md#lifecycle)')


def unwatch_namespace(root: Path, namespace: str) -> None:
    path = root / 'platform/reloader/helmrelease.yaml'
    if not path.exists():
        return
    text = path.read_text()
    match = re.search(r'^(\s*namespaces:\s*)\[([^\]]*)\]', text, re.M)
    current = [n.strip() for n in match[2].split(',') if n.strip()] if match else []
    if namespace in current:
        current.remove(namespace)
        path.write_text(text[:match.start()] + f'{match[1]}[{", ".join(current)}]' + text[match.end():])
        print(f'[OK] Reloader no longer watches {namespace}')


def run(action, check_dir: bool) -> int:
    """Run an edit; on success check the edited instance against the app policy."""
    try:
        result = action()
    except EditError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 2
    if check_dir and result:
        return check_generated(result)
    print('[INFO] Next: make check-apps, then commit, push and make flux-reconcile')
    return 0


def main_promote(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-promote', description='Copy an app image (tag and digest) between environments')
    p.add_argument('app')
    p.add_argument('--from', dest='source', default='staging', choices=ENVIRONMENTS)
    p.add_argument('--to', dest='target', default='prod', choices=ENVIRONMENTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: promote(args.root.resolve(), args.app, args.source, args.target), True)


def main_scale(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-scale', description='Change replicas or resources of an app instance')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--replicas', type=int)
    p.add_argument('--cpu')
    p.add_argument('--memory')
    p.add_argument('--memory-limit')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: scale(args.root.resolve(), args.app, args.env, replicas=args.replicas, cpu=args.cpu,
                             memory=args.memory, memory_limit=args.memory_limit), True)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-remove', description='Delete an app instance from Git (Flux uninstalls it)')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: remove(args.root.resolve(), args.app, args.env), False)
