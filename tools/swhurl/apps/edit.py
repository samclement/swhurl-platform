"""Edit generated app instances (Git edits only, like app-new): promote, scale, remove.

    app-promote APP   create production from staging or update its image
    app-scale APP ENV [--replicas N] [--cpu Q] [--memory Q] [--memory-limit Q]
    app-expose APP ENV --exposure E [--host H]     change who can reach an instance
    app-team APP --team T                          move an app (every environment) to a registered team
    app-calls APP ENV [--add A,B] [--remove C]     change which apps an instance may call
    app-sync                                       rewrite what the platform derives in every instance
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
from swhurl.apps import ops, promotion
from swhurl.apps.contract import (
    AUTH_MIDDLEWARE,
    CALLERS_ENV,
    ENVIRONMENTS,
    EXPOSURE,
    EXPOSURES,
    TEAM,
    TOKENS_VOLUME,
    allowed_callers,
    declared_calls,
    default_host,
    identity_env,
    in_cookie_domain,
    service_env,
    service_url,
    signing_keys,
    teams,
    tokens_volume,
)
from swhurl.apps.new import NAME_RE, check_generated, depends_on, ingress_values, isolation_files
from swhurl.apps.yaml_file import EditError, YamlFile, mapping
from swhurl.run import CommandError, Runner

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


def isolation_edits(instance: Path, app: str, env: str, exposure: str, app_values: dict) -> list[tuple[Path, str]]:
    """The instance's quota and NetworkPolicy for ``exposure``, and its kustomization listing them."""
    port = service_port(app_values)
    callers = allowed_callers(mapping(container_of(app_values), 'env') if 'env' in container_of(app_values) else {})
    files = isolation_files(app, f'{app}-{env}', exposure, port, callers)
    edits = [(instance / name, text) for name, text in files.items()]
    kustomization = YamlFile(instance / 'kustomization.yaml')
    resources = kustomization.data.get('resources')
    if not isinstance(resources, list):
        raise EditError('kustomization.yaml: expected a resources list')
    missing = [name for name in files if name not in resources]
    if missing:
        resources.extend(missing)
        edits.append((kustomization.path, kustomization.render()))
    return edits


def container_of(app_values: dict) -> dict:
    return mapping(mapping(mapping(mapping(app_values, 'controllers'), 'main'), 'containers'), 'main')


def service_port(app_values: dict) -> int | None:
    """The app's HTTP port, or None for a worker."""
    if 'service' not in app_values:
        return None
    port = mapping(mapping(mapping(mapping(app_values, 'service'), 'main'), 'ports'), 'http').get('port')
    if not isinstance(port, int):
        raise EditError('service.main.ports.http.port: expected a port number; edit custom services by hand')
    return port


def instances_in(root: Path, env: str) -> list[str]:
    return sorted(p.parent.parent.name for p in (root / 'apps').glob(f'*/{env}/helmrelease.yaml'))


def sync_env(root: Path, env: str) -> list[Path]:
    """Rewrite what the platform derives in every instance of one environment; return the files changed.

    Derived from each instance itself: who it is (SWHURL_APP, SWHURL_ENV), and for a web app the token
    issuer and signing keys. Derived from what callers declare (their SWHURL_SERVICE_<APP>_URL
    variables): the caller's token files and the address's port; the callee's allowed callers and
    its NetworkPolicy. A declared call to an app with no instance in this environment is an error.
    """
    releases = {app: YamlFile(instance_dir(root, app, env) / 'helmrelease.yaml') for app in instances_in(root, env)}
    ports = {app: service_port(values(doc.data)) for app, doc in releases.items()}
    calls = {app: declared_calls(container(doc.data).get('env') or {}) for app, doc in releases.items()}
    for app, callees in calls.items():
        for callee in callees:
            if callee not in releases:
                raise EditError(f'{app}/{env} calls {callee}, which has no {env} instance')
            if ports[callee] is None:
                raise EditError(f'{app}/{env} calls {callee}, a worker: it has no address to call')
    keys = signing_keys(root)
    edits: list[tuple[Path, str]] = []
    for app, doc in releases.items():
        instance = doc.path.parent
        app_values, main = values(doc.data), container(doc.data)
        env_map = mapping(main, 'env', create=True)
        for index, (key, value) in enumerate(identity_env(app, env, ports[app] is not None, keys).items()):
            if key not in env_map:
                env_map.insert(index, key, value)
            elif env_map[key] != value:
                env_map[key] = value
        for callee in calls[app]:
            if env_map[service_env(callee)] != service_url(callee, ports[callee]):
                env_map[service_env(callee)] = service_url(callee, ports[callee])
        callers = sorted(caller for caller, callees in calls.items() if app in callees)
        if callers and env_map.get(CALLERS_ENV) != ','.join(callers):
            env_map[CALLERS_ENV] = ','.join(callers)
        elif not callers:
            env_map.pop(CALLERS_ENV, None)
        persistence = mapping(app_values, 'persistence', create=True)
        if calls[app] and persistence.get(TOKENS_VOLUME) != tokens_volume(calls[app]):
            persistence[TOKENS_VOLUME] = tokens_volume(calls[app])
        elif not calls[app]:
            persistence.pop(TOKENS_VOLUME, None)
        exposure = mapping(mapping(YamlFile(instance / 'namespace.yaml').data, 'metadata'), 'labels').get(EXPOSURE)
        edits.append((doc.path, doc.render()))
        edits += isolation_edits(instance, app, env, exposure, app_values)
    changed = [(path, text) for path, text in edits if not path.exists() or path.read_text() != text]
    for path, text in changed:
        path.write_text(text)
    return [path for path, _ in changed]


def sync(root: Path) -> Path | None:
    changed = [path for env in ENVIRONMENTS for path in sync_env(root, env)]
    for path in changed:
        print(f'[OK] rewrote {path.relative_to(root)}')
    if not changed:
        print('[OK] every instance already has what the platform derives')
    return None


def calls(root: Path, app: str, env: str, *, add: list[str] | None = None, remove: list[str] | None = None) -> Path:
    """Change which apps ``app/env`` may call: its side here, the callees' side through sync_env."""
    add, remove = add or [], remove or []
    if not add and not remove:
        raise EditError('give --add and/or --remove with app names')
    instance = instance_dir(root, app, env)
    document = YamlFile(instance / 'helmrelease.yaml')
    env_map = mapping(container(document.data), 'env', create=True)
    current = declared_calls(env_map)
    for callee in add:
        if callee == app:
            raise EditError('an app does not declare calls to itself')
        if callee in current:
            raise EditError(f'{app}/{env} already calls {callee}')
        port = service_port(values(YamlFile(instance_dir(root, callee, env) / 'helmrelease.yaml').data))
        if port is None:
            raise EditError(f'{callee} is a worker: it has no address to call')
        env_map[service_env(callee)] = service_url(callee, port)
    for callee in remove:
        if callee not in current:
            raise EditError(f'{app}/{env} does not call {callee}')
        del env_map[service_env(callee)]
    before = document.path.read_text()
    document.save()
    try:
        sync_env(root, env)
    except EditError:
        document.path.write_text(before)
        raise
    now = declared_calls(mapping(container(YamlFile(instance / 'helmrelease.yaml').data), 'env'))
    print(f'[OK] {app}/{env} calls: {", ".join(now) or "nothing"}')
    return instance


def callers_of(root: Path, app: str, env: str) -> list[str]:
    """The other instances of ``env`` that declare a call to ``app``."""
    found = []
    for other in instances_in(root, env):
        release = YamlFile(root / 'apps' / other / env / 'helmrelease.yaml').data
        if other != app and app in declared_calls(container(release).get('env') or {}):
            found.append(other)
    return found


def team(root: Path, app: str, name: str) -> Path:
    """Move every environment of an app to a team, and give each its quota and NetworkPolicy if it lacks them."""
    if name not in teams(root):
        raise EditError(f'unknown team {name!r}; registered in apps/teams.yaml: {", ".join(teams(root))}')
    found = [env for env in ENVIRONMENTS if (root / 'apps' / app / env / 'helmrelease.yaml').is_file()]
    if not found:
        instance_dir(root, app, ENVIRONMENTS[0])  # raises, naming what is missing
    edits: list[tuple[Path, str]] = []
    for env in found:
        instance = instance_dir(root, app, env)
        namespace = YamlFile(instance / 'namespace.yaml')
        labels = mapping(mapping(namespace.data, 'metadata'), 'labels')
        if labels.get(TEAM) != name:
            labels[TEAM] = name
            edits.append((namespace.path, namespace.render()))
        wanted = isolation_edits(instance, app, env, labels.get(EXPOSURE),
                                 values(YamlFile(instance / 'helmrelease.yaml').data))
        edits += [(path, text) for path, text in wanted if not path.exists() or path.read_text() != text]
    if not edits:
        raise EditError(f'{app} already belongs to {name}')
    for path, text in edits:
        path.write_text(text)
    print(f'[OK] {app}: team {name} ({", ".join(found)})')
    return root / 'apps' / app / found[0]


def promote(root: Path, app: str, source: str = 'staging', target: str = 'prod', *,
            expect_image: str | None = None, expect_config: str | None = None,
            host: str | None = None, runner=None) -> Path:
    if (source, target) != ('staging', 'prod'):
        raise EditError('promotion must differ between environments and always goes staging → prod')
    src, dst = promotion.read(root, app)
    decision = promotion.decide(src, dst)
    if expect_image is not None and decision.image != expect_image:
        raise EditError(f'{app}/staging image changed since it was reviewed; refresh staging and review it again')
    if expect_config is not None and promotion.fingerprint(root, app, 'staging') != expect_config:
        raise EditError('staging configuration changed since it was reviewed; refresh and review it again')
    if decision.outcome == 'same':
        raise EditError(f'{app}/prod already runs this digest; same image in both')
    if decision.outcome == 'create':
        missing = [callee for callee in declared_calls(container(src).get('env') or {})
                   if not (root / 'apps' / callee / target / 'helmrelease.yaml').is_file()]
        if missing:
            raise EditError(f'{app} calls {", ".join(missing)}, which has no production instance: promote that first')
        result = promotion.create(root, app, src, host=host, runner=runner)
        sync_env(root, target)  # production's own callers and callees
        print(f'[OK] created {app}/prod from reviewed staging settings (independent storage and credentials)')
        if container(src).get('envFrom'):
            print(f'[INFO] Set production secrets with sops apps/{app}/prod/secret.sops.yaml before merging')
        return result
    if host:
        raise EditError('--host applies only to first production; change existing routes with app-expose')
    document = YamlFile(root / 'apps' / app / 'prod/helmrelease.yaml')
    current = mapping(container(document.data), 'image')
    image = promotion.image_of(src)
    current.update({k: str(image[k]) for k in ('tag', 'digest') if k in image})
    if 'tag' not in image:
        current.pop('tag', None)
    document.save()
    print(f'[OK] {app}/prod: image {decision.image} (from staging)' + (' [rollback]' if decision.rollback else ''))
    return root / 'apps' / app / 'prod'


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
    edits += isolation_edits(instance, app, env, exposure, app_values)
    for path, text in edits:
        path.write_text(text)
    print(f'[OK] {app}/{env}: {current} -> {exposure}' + (f' at https://{host}' if host else ' (no route)'))
    return instance


def remove(root: Path, app: str, env: str) -> None:
    instance = instance_dir(root, app, env)
    callers = callers_of(root, app, env)
    if callers:
        raise EditError(f'{app}/{env} is called by {", ".join(callers)}: remove those calls first '
                        f'(make app-calls APP=<caller> ENV={env} ARGS="--remove {app}")')
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
    for path in sync_env(root, env):  # the apps it called no longer admit it
        print(f'[OK] rewrote {path.relative_to(root)}')
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


def main_promote(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-promote', description='Copy an app image (tag and digest) between environments')
    p.add_argument('app')
    p.add_argument('--expect-image', help='refuse if the source no longer names this exact repository, tag and digest')
    p.add_argument('--expect-config', help='refuse if the reviewed staging configuration hash changed')
    p.add_argument('--host', help='distinct production hostname for first public promotion')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    runner = runner or Runner()

    def change():
        root = args.root.resolve()
        source, target = promotion.read(root, args.app)
        decision = promotion.decide(source, target)
        if args.expect_image and args.expect_image != decision.image:
            raise EditError('staging image changed since it was reviewed; refresh and review it again')
        if decision.outcome == 'same':
            raise EditError('production already runs this digest; same image in both')
        status = ops.gather_status(runner, ops.Instance(args.app, 'staging'))
        problem = promotion.source_problem(status, decision.image, status.applied_revision if status else '')
        if problem:
            raise EditError(problem)
        return promote(root, args.app, expect_image=args.expect_image,
                       expect_config=args.expect_config, host=args.host, runner=runner)

    try:
        return run(change, True)
    except CommandError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1



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


def main_team(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-team', description='Move an app to a team registered in apps/teams.yaml')
    p.add_argument('app')
    p.add_argument('--team', required=True)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: team(args.root.resolve(), args.app, args.team), True)


def main_calls(argv: list[str] | None = None) -> int:
    def names(text: str) -> list[str]:
        return [name.strip() for name in text.split(',') if name.strip()]
    p = argparse.ArgumentParser(prog='swhurl app-calls', description='Change which apps an instance may call')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--add', type=names, default=[], help='comma-separated apps to call (each needs this environment)')
    p.add_argument('--remove', type=names, default=[], help='comma-separated apps to stop calling')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: calls(args.root.resolve(), args.app, args.env, add=args.add, remove=args.remove), True)


def main_sync(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-sync', description='Rewrite what the platform derives in every instance')
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: sync(args.root.resolve()), False)


def main_remove(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog='swhurl app-remove', description='Delete an app instance from Git (Flux uninstalls it)')
    p.add_argument('app')
    p.add_argument('env', choices=ENVIRONMENTS)
    p.add_argument('--root', type=Path, default=ROOT)
    args = p.parse_args(argv)
    return run(lambda: remove(args.root.resolve(), args.app, args.env), False)
