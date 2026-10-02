"""Edit generated app instances (Git edits only, like app-new): promote, scale, remove.

    app-promote APP [--from staging] [--to prod]   copy the image (tag and digest) between environments
    app-scale APP ENV [--replicas N] [--cpu Q] [--memory Q] [--memory-limit Q]
    app-remove APP ENV                             delete the instance's files and unregister its unit

Each edits files under the repository root and checks the result against the
app policy; commit and push to apply. Handwritten YAML retains its comments,
quotes and key order. Unsupported custom structures are refused explicitly.
"""
from __future__ import annotations

import argparse
import re
import shutil
import sys
from pathlib import Path

from swhurl import ROOT
from swhurl.apps.contract import (
    AUTH_MIDDLEWARE,
    ENVIRONMENTS,
    EXPOSURE,
    EXPOSURES,
    default_host,
    in_cookie_domain,
)
from swhurl.apps.new import NAME_RE, check_generated, depends_on, ingress_values
from swhurl.apps.ops import image_reference
from swhurl.apps.yaml_file import EditError, YamlFile, mapping

PRUNE_DISABLED = 'kustomize.toolkit.fluxcd.io/prune'
CPU_RE = re.compile(r'^\d+(\.\d+)?m?$')
MEMORY_RE = re.compile(r'^\d+(Ki|Mi|Gi)$')
MAX_REPLICAS = 10


def instance_dir(root: Path, app: str, env: str) -> Path:
    if not NAME_RE.match(app) or env not in ENVIRONMENTS:
        raise EditError(f'no instance {app}/{env}: APP must be a DNS label and ENV one of {", ".join(ENVIRONMENTS)}')
    path = root / 'apps' / app / env
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


def promote(root: Path, app: str, source: str, target: str, *, expect_image: str | None = None) -> Path:
    if source == target:
        raise EditError('--from and --to must differ')
    src, dst = instance_dir(root, app, source), instance_dir(root, app, target)
    image = mapping(container(load_release(src)), 'image')
    if expect_image is not None and image_reference(image) != expect_image:
        raise EditError(f'{app}/{source} image changed since it was reviewed; refresh staging and review it again')
    if not image.get('digest'):
        raise EditError(f'{app}/{source} has no image digest; promote only a digest-pinned image')
    document = YamlFile(dst / 'helmrelease.yaml')
    current = mapping(container(document.data), 'image')
    if current.get('repository') != image.get('repository'):
        raise EditError(f'{app}/{target} uses {current.get("repository")}, not {image.get("repository")}; '
                        'change the repository by hand')
    if (current.get('tag'), current.get('digest')) == (image.get('tag'), image['digest']):
        raise EditError(f'{app}/{target} already runs {image.get("tag")}@{image["digest"]}')
    current.update({k: str(image[k]) for k in ('tag', 'digest') if k in image})
    document.save()
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
    document = YamlFile(instance / 'helmrelease.yaml')
    main = controller(document.data)
    resources = mapping(container(document.data), 'resources', create=True) if cpu or memory or memory_limit else None
    changes = []
    if replicas is not None and main.get('replicas', 1) != replicas:
        if 'replicas' in main:
            main['replicas'] = replicas
        else:
            main.insert(0, 'replicas', replicas)
        changes.append(f'replicas {replicas}')
    for section, key, value in (('requests', 'cpu', cpu), ('requests', 'memory', memory), ('limits', 'memory', memory_limit)):
        if value and mapping(resources, section, create=True).get(key) != value:
            resources[section][key] = value
            changes.append(f'{section}.{key} {value}')
    if not changes:
        raise EditError(f'{app}/{env} already has those settings')
    document.save()
    print(f'[OK] {app}/{env}: {", ".join(changes)}')
    return instance


def expose(root: Path, app: str, env: str, exposure: str, host: str | None = None) -> Path:
    """Change who can reach an instance: its route, sign-in, Namespace label and unit dependencies."""
    instance = instance_dir(root, app, env)
    document = YamlFile(instance / 'helmrelease.yaml')
    app_values = values(document.data)
    ingress = mapping(app_values, 'ingress') if 'ingress' in app_values else None
    route = mapping(ingress, 'main') if ingress and 'main' in ingress else None
    if route:
        hosts, tls = route.get('hosts'), route.get('tls')
        if (not isinstance(hosts, list) or len(hosts) != 1 or not isinstance(hosts[0], dict)
                or not isinstance(hosts[0].get('host'), str)
                or not isinstance(tls, list) or len(tls) != 1 or not isinstance(tls[0], dict)
                or tls[0].get('hosts') != [hosts[0]['host']]):
            raise EditError('main ingress: expected one host and matching TLS entry; edit custom routes by hand')
        # These sequences are edited in place, so shared values are ambiguous too.
        for value in (hosts, hosts[0], tls, tls[0], tls[0]['hosts']):
            if value.yaml_anchor() or getattr(value, 'merge', None):
                raise EditError('main ingress uses a YAML anchor or merge; edit shared settings by hand')
    current_host = route['hosts'][0]['host'] if route else None
    namespace_path, unit_path = instance / 'namespace.yaml', root / 'clusters/home' / f'app-{app}-{env}.yaml'
    namespace, unit = YamlFile(namespace_path), YamlFile(unit_path)
    labels = mapping(mapping(namespace.data, 'metadata'), 'labels')
    unit_spec = mapping(unit.data, 'spec')
    current = labels.get(EXPOSURE)
    if exposure != 'private' and 'service' not in app_values:
        raise EditError(f'{app}/{env} is a worker (no Service): it can only be private')
    if exposure == 'authenticated-web':
        host = host or (current_host if current_host and in_cookie_domain(current_host) else default_host(app, env))
        if not in_cookie_domain(host):
            raise EditError(f'signed-in hosts must be under {default_host("x", "prod")[2:]} (shared sign-in cookie)')
    elif exposure == 'public':
        host = host or (current_host if current_host and not in_cookie_domain(current_host) else None)
        if not host:
            raise EditError('a public instance needs --host, outside the sign-in cookie domain')
        if in_cookie_domain(host):
            raise EditError(f'public hosts must be outside {default_host("x", "prod")[2:]}: the sign-in cookie would reach them')
    elif host:
        raise EditError('a private instance has no route; drop --host')
    if (exposure, host) == (current, current_host):
        raise EditError(f'{app}/{env} is already {exposure}' + (f' at {host}' if host else ''))
    issuer = ((route or {}).get('annotations') or {}).get('cert-manager.io/cluster-issuer', 'letsencrypt-prod')
    ingress = mapping(app_values, 'ingress', create=True)
    if exposure == 'private':
        if any(key != 'main' for key in ingress):
            raise EditError('private exposure would leave additional ingress routes; edit custom routes by hand')
        ingress.pop('main', None)
        if not ingress:
            app_values.pop('ingress')
    elif route:
        route['hosts'][0]['host'] = host
        route['tls'][0]['hosts'][0] = host
        annotations = mapping(route, 'annotations', create=True)
        key = 'traefik.ingress.kubernetes.io/router.middlewares'
        middleware_value = annotations.get(key, '')
        if not isinstance(middleware_value, str):
            raise EditError(f'{key}: expected a comma-separated middleware string')
        middlewares = [m.strip() for m in middleware_value.split(',') if m.strip()]
        middlewares = [m for m in middlewares if m != AUTH_MIDDLEWARE]
        if exposure == 'authenticated-web':
            middlewares.insert(0, AUTH_MIDDLEWARE)
        if middlewares:
            annotations[key] = ','.join(middlewares)
        else:
            annotations.pop(key, None)
    else:
        ingress['main'] = ingress_values(app, host, exposure, issuer)
    labels[EXPOSURE] = exposure
    dependencies = unit_spec.setdefault('dependsOn', [])
    if not isinstance(dependencies, list) or any(not isinstance(d, dict) or 'name' not in d for d in dependencies):
        raise EditError('dependsOn: expected a list of named Flux dependencies')
    if getattr(dependencies, 'yaml_anchor', lambda: None)():
        raise EditError('dependsOn uses a YAML anchor; edit shared settings by hand')
    if exposure != 'authenticated-web':
        for index in reversed(range(len(dependencies))):
            if dependencies[index]['name'] == 'platform-oauth2-proxy':
                del dependencies[index]
    for name in depends_on(exposure):
        if not any(d['name'] == name for d in dependencies):
            dependencies.append({'name': name})
    # Render every file before writing any, so unsupported YAML cannot cause a
    # partially saved exposure edit. Policy still validates the resulting files.
    edits = [(file.path, file.render()) for file in (document, namespace, unit)]
    for path, text in edits:
        path.write_text(text)
    print(f'[OK] {app}/{env}: {current} -> {exposure}' + (f' at https://{host}' if host else ' (no route)'))
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
    p.add_argument('--expect-image', help='refuse if the source no longer names this exact repository, tag and digest')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: promote(args.root.resolve(), args.app, args.source, args.target,
                               expect_image=args.expect_image), True)


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


def main_expose(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-expose', description='Change who can reach an app instance')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--exposure', required=True, choices=EXPOSURES,
                   help='private: no route; authenticated-web: Google sign-in; public: no sign-in, host outside the domain')
    p.add_argument('--host', help='default for authenticated-web: the current host if signed-in, else the derived one')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: expose(args.root.resolve(), args.app, args.env, args.exposure, args.host), True)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-remove', description='Delete an app instance from Git (Flux uninstalls it)')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: remove(args.root.resolve(), args.app, args.env), False)
